package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func evalArgs(root, repo string) []string {
	return []string{"eval", filepath.Join(root, repo) + "#" + installPreCommitHooksAttr, "--apply", "_: true"}
}

const initHooksToml = `
[repos.has]
url = "github:o/has"

[repos.lacks]
url = "github:o/lacks"

[repos.broken]
url = "github:o/broken"

[repos.declared]
url = "github:o/declared"

[[repos.declared.hooks]]
when = ["post-clone"]
run = ["{nix_run install-pre-commit-hooks}"]

[repos.other]
url = "github:o/other"

[[repos.other.hooks]]
when = ["post-apply"]
run = ["echo hi"]
`

// initHooksWorkspace builds a workspace where "has" and "other" expose the
// output, "lacks" and "broken" do not (broken via an eval error), and "declared"
// already declares the hook (no eval scripted: any eval for it would fail the
// call-count assertion).
func initHooksWorkspace(t *testing.T) (*Workspace, *exec.FakeRunner, string) {
	t.Helper()
	root := t.TempDir()
	for _, n := range []string{"has", "lacks", "broken", "declared", "other"} {
		mkGitRepo(t, root, n)
	}
	writeFile(t, filepath.Join(root, ConfigFileName), initHooksToml)
	f := exec.NewFakeRunner()
	f.AddResponse("nix", evalArgs(root, "has"), exec.Result{}, nil)
	f.AddResponse("nix", evalArgs(root, "other"), exec.Result{}, nil)
	f.AddResponse("nix", evalArgs(root, "broken"), exec.Result{}, errors.New("eval failed"))
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(w.Close)
	return w, f, root
}

func TestInstallHookCandidates(t *testing.T) {
	w, f, root := initHooksWorkspace(t)
	got := w.InstallHookCandidates(context.Background())
	if strings.Join(got, ",") != "has,other" {
		t.Fatalf("candidates = %v, want [has other]", got)
	}
	for _, c := range f.Calls() {
		if strings.Contains(strings.Join(c.Args, " "), filepath.Join(root, "declared")) {
			t.Errorf("declared repo was evaluated: %v", c.Args)
		}
	}
	if n := len(f.Calls()); n != 4 {
		t.Errorf("eval calls = %d, want 4 (has, lacks, broken, other)", n)
	}
}

func TestDetectInstallHooks_YesWritesDedicatedEntry(t *testing.T) {
	w, _, root := initHooksWorkspace(t)
	var out bytes.Buffer
	added, err := w.DetectInstallHooks(context.Background(), &out, InitHooksOptions{Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(added, ",") != "has,other" {
		t.Fatalf("added = %v", added)
	}
	if !strings.Contains(out.String(), "comments are dropped") {
		t.Errorf("missing comment-loss note:\n%s", out.String())
	}
	data, err := os.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfig(data) // re-parses and passes hook validation
	if err != nil {
		t.Fatalf("written file does not parse: %v\n%s", err, data)
	}
	has := cfg.Repos["has"].Hooks
	if len(has) != 1 || strings.Join(has[0].When, ",") != "post-clone,post-rebase,post-update,post-upgrade" ||
		len(has[0].Run) != 1 || has[0].Run[0] != "{nix_run install-pre-commit-hooks}" {
		t.Errorf("has hooks = %+v", has)
	}
	// Existing hook on "other" is untouched; the new one is appended.
	other := cfg.Repos["other"].Hooks
	if len(other) != 2 || other[0].Run[0] != "echo hi" || strings.Join(other[0].When, ",") != "post-apply" ||
		other[1].Run[0] != "{nix_run install-pre-commit-hooks}" {
		t.Errorf("other hooks = %+v", other)
	}
	if len(cfg.Repos["declared"].Hooks) != 1 || len(cfg.Repos["lacks"].Hooks) != 0 {
		t.Errorf("unrelated repos modified")
	}
}

func TestDetectInstallHooks_NonInteractiveWithoutYesWritesNothing(t *testing.T) {
	w, _, root := initHooksWorkspace(t)
	before, _ := os.ReadFile(filepath.Join(root, ConfigFileName))
	var out bytes.Buffer
	added, err := w.DetectInstallHooks(context.Background(), &out, InitHooksOptions{})
	if err != nil || len(added) != 0 {
		t.Fatalf("added=%v err=%v", added, err)
	}
	after, _ := os.ReadFile(filepath.Join(root, ConfigFileName))
	if !bytes.Equal(before, after) {
		t.Error("config was modified")
	}
	if s := out.String(); !strings.Contains(s, "advisory") || !strings.Contains(s, "--yes") ||
		!strings.Contains(s, "has") || !strings.Contains(s, "other") {
		t.Errorf("advisory = %q", s)
	}
}

func TestDetectInstallHooks_InteractivePerCandidate(t *testing.T) {
	w, _, root := initHooksWorkspace(t)
	var out bytes.Buffer
	added, err := w.DetectInstallHooks(context.Background(), &out, InitHooksOptions{
		Confirm: func(repo string) bool { return repo == "other" },
	})
	if err != nil || strings.Join(added, ",") != "other" {
		t.Fatalf("added=%v err=%v", added, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, ConfigFileName))
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repos["has"].Hooks) != 0 || len(cfg.Repos["other"].Hooks) != 2 {
		t.Errorf("declined repo changed or accepted repo not written: %+v", cfg.Repos)
	}
}

func TestDetectInstallHooks_InteractiveAllDeclinedWritesNothing(t *testing.T) {
	w, _, root := initHooksWorkspace(t)
	before, _ := os.ReadFile(filepath.Join(root, ConfigFileName))
	var out bytes.Buffer
	if _, err := w.DetectInstallHooks(context.Background(), &out, InitHooksOptions{Confirm: func(string) bool { return false }}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, ConfigFileName))
	if !bytes.Equal(before, after) {
		t.Error("config was modified")
	}
}

// The hook write happens even when Init itself reported "no changes".
func TestDetectInstallHooks_AfterInitNoChanges(t *testing.T) {
	root := t.TempDir()
	mkGitRepo(t, root, "has")
	writeFile(t, filepath.Join(root, ConfigFileName), "[repos.has]\nurl = \"github:o/has\"\n")
	f := exec.NewFakeRunner()
	f.AddResponse("nix", evalArgs(root, "has"), exec.Result{}, nil)
	w, err := Open(root, f)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	var out bytes.Buffer
	if err := w.Init(context.Background(), &out, InitOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no changes") {
		t.Fatalf("expected Init no changes: %s", out.String())
	}
	if added, err := w.DetectInstallHooks(context.Background(), &out, InitHooksOptions{Yes: true}); err != nil || len(added) != 1 {
		t.Fatalf("added=%v err=%v", added, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, ConfigFileName))
	if !strings.Contains(string(data), "install-pre-commit-hooks") {
		t.Errorf("hook not written:\n%s", data)
	}
}
