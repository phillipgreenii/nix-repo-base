package beads

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/ledger"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/report"
)

// Filer files one triage bead per package, once every file of that package has
// a recorded status in the ledger, and never before (design, "Bead filing").
//
// The worker pool calls OnRecorded after each ledger append. A package is
// complete when every non-test .go file directly in its directory has a record
// and all those records carry the same package hash: a hash that differs means
// the package was edited between two of its files' analyses, so no single
// consistent run exists to hand to an agent, and the package waits for the
// re-analysis the ledger will queue.
type Filer struct {
	// Ledger is the sweep's ledger; the Filer only reads it.
	Ledger *ledger.Ledger
	// NewClient returns the bd client for the tracker at repoRoot.
	NewClient func(repoRoot string) BdClient
	// RepoLabels maps a repository directory name to its canonical bead label
	// (the nix-rendered config's repoLabels). A repository not listed is
	// resolved from its own CLAUDE.md, never from a path component.
	RepoLabels map[string]string
	// FiledPath is the JSON-lines file recording which (module, package,
	// package hash) already has a bead, so a restart does not file a second.
	FiledPath string
	// Log receives one line per filed bead and per failure. It carries no
	// counts. nil discards.
	Log *slog.Logger

	mu      sync.Mutex
	filed   map[string]struct{}
	loaded  bool
	pending map[string]struct{} // package dirs whose filing failed; retried on the next event
}

type filedRecord struct {
	Module      string    `json:"module"`
	PkgPath     string    `json:"pkg_path"`
	PackageHash string    `json:"package_hash"`
	BeadID      string    `json:"bead_id"`
	Timestamp   time.Time `json:"timestamp"`
}

func (f *Filer) logger() *slog.Logger {
	if f.Log != nil {
		return f.Log
	}
	return slog.New(slog.DiscardHandler)
}

// OnRecorded is the worker pool's hook: file has just been recorded in the
// ledger. Failures are logged and kept for retry on the next event; they never
// reach the caller, so a bd outage cannot stop the sweep.
func (f *Filer) OnRecorded(file string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dirs := []string{filepath.Dir(file)}
	for d := range f.pending {
		if d != dirs[0] {
			dirs = append(dirs, d)
		}
	}
	sort.Strings(dirs[1:])
	for _, dir := range dirs {
		if err := f.fileIfComplete(dir); err != nil {
			if f.pending == nil {
				f.pending = map[string]struct{}{}
			}
			f.pending[dir] = struct{}{}
			f.logger().Error("could not file the triage bead", "package_dir", dir, "error", err.Error())
			continue
		}
		delete(f.pending, dir)
	}
}

// fileIfComplete files the bead for the package in dir when it is complete and
// not yet filed. The caller holds f.mu.
func (f *Filer) fileIfComplete(dir string) error {
	projectRoot := findUp(dir, "go.mod")
	if projectRoot == "" {
		return nil
	}
	files, err := sourceFiles(dir)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	records, err := f.Ledger.Replay()
	if err != nil {
		return err
	}
	hash := ""
	for i, file := range files {
		r, ok := records[file]
		if !ok {
			return nil // not every file has a recorded status yet
		}
		if i == 0 {
			hash = r.PackageHash
		} else if r.PackageHash != hash {
			return nil // analysed at two different package contents
		}
	}

	pkgPath, err := filepath.Rel(projectRoot, dir)
	if err != nil {
		return err
	}
	repoRoot := findUp(projectRoot, ".git")
	if repoRoot == "" {
		return fmt.Errorf("no repository root above %s", projectRoot)
	}
	module, err := filepath.Rel(repoRoot, projectRoot)
	if err != nil {
		return err
	}
	key := strings.Join([]string{module, pkgPath, hash}, "\x00")
	if err := f.loadFiled(); err != nil {
		return err
	}
	if _, done := f.filed[key]; done {
		return nil
	}

	label, err := ResolveRepoLabel(repoRoot, f.RepoLabels)
	if err != nil {
		return err
	}
	t := Triage{Module: module, PkgPath: pkgPath}
	for _, file := range files {
		r := records[file]
		rel, err := filepath.Rel(projectRoot, file)
		if err != nil {
			return err
		}
		fr := FileResult{File: rel, Status: r.Status, ReportPath: r.ReportPath}
		if r.Status == "done" && r.ReportPath != "" {
			w, err := report.ParseWorklist(r.ReportPath)
			if err != nil {
				fr.ReportUnreadable = true
			} else {
				fr.Worklist = w
			}
		}
		t.Files = append(t.Files, fr)
	}
	id, err := FileTriageBead(f.NewClient(repoRoot), t, label)
	if err != nil {
		return err
	}
	if err := f.markFiled(key, filedRecord{Module: module, PkgPath: pkgPath, PackageHash: hash, BeadID: id, Timestamp: time.Now().UTC()}); err != nil {
		// The bead exists; only the restart-dedupe marker failed. Report it,
		// but do not retry, which would file a second bead.
		f.filed[key] = struct{}{}
		f.logger().Error("filed the triage bead but could not record it", "bead", id, "error", err.Error())
	}
	f.logger().Info("filed triage bead", "bead", id, "module", module, "package", pkgPath)
	return nil
}

func (f *Filer) loadFiled() error {
	if f.loaded {
		return nil
	}
	f.filed = map[string]struct{}{}
	fh, err := os.Open(f.FiledPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			f.loaded = true
			return nil
		}
		return err
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var r filedRecord
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		f.filed[strings.Join([]string{r.Module, r.PkgPath, r.PackageHash}, "\x00")] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	f.loaded = true
	return nil
}

func (f *Filer) markFiled(key string, r filedRecord) error {
	f.filed[key] = struct{}{}
	if err := os.MkdirAll(filepath.Dir(f.FiledPath), 0o755); err != nil {
		return err
	}
	fh, err := os.OpenFile(f.FiledPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer fh.Close()
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = fh.Write(append(line, '\n'))
	return err
}

// sourceFiles lists the non-test .go files directly in dir, the same set
// discover.DiscoverPackages reports as a package's Files.
func sourceFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		files = append(files, filepath.Join(dir, n))
	}
	return files, nil
}

// findUp returns the nearest directory at or above start that contains an
// entry named marker (a file or a directory; a linked worktree's .git is a
// file), or "" when there is none.
func findUp(start, marker string) string {
	dir := start
	for {
		if _, err := os.Lstat(filepath.Join(dir, marker)); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

var claudeLabelRE = regexp.MustCompile("MUST carry the repo label `([^`]+)`")

// ResolveRepoLabel returns the canonical bead label of the repository at
// repoRoot. It looks the repository's directory name up in configured (the
// nix-rendered repoLabels), and otherwise reads the repository's own
// CLAUDE.md, whose "Beads Labels" section states "MUST carry the repo label
// `<label>`". It MUST NOT derive a label from a path component: a repository's
// canonical label does not always match its checkout directory (the
// nix-repo-base clone's label is `base`), so a derived one would mislabel it.
// A repository with neither is an error, and no bead is filed for it.
func ResolveRepoLabel(repoRoot string, configured map[string]string) (string, error) {
	if l := strings.TrimSpace(configured[filepath.Base(repoRoot)]); l != "" {
		return l, nil
	}
	raw, err := os.ReadFile(filepath.Join(repoRoot, "CLAUDE.md"))
	if err != nil {
		return "", fmt.Errorf("no repoLabels entry for %q and no readable CLAUDE.md: %w", filepath.Base(repoRoot), err)
	}
	m := claudeLabelRE.FindSubmatch(raw)
	if m == nil {
		return "", fmt.Errorf("no repoLabels entry for %q and its CLAUDE.md states no repo label", filepath.Base(repoRoot))
	}
	return string(m[1]), nil
}
