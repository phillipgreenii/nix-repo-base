package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/config"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/logdir"
)

// fileOwner is the real SpoolOwner.
func fileOwner(fi os.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

// checkSpool prints the spool lines of --check and reports whether the spool
// directory is writable. A backlog is information, not a failure: the doctor
// reports a stuck one.
func (r *runner) checkSpool(res config.Resolved) bool {
	err := r.spoolWritable(res)
	if err != nil {
		fmt.Fprintf(r.Stdout, "spool-dir: %s (from %s) (writable: no: %v)\n", res.SpoolDir, res.SpoolSource, err)
		return false
	}
	fmt.Fprintf(r.Stdout, "spool-dir: %s (from %s) (writable: yes)\n", res.SpoolDir, res.SpoolSource)
	n, oldest := spoolBacklog(res.SpoolDir, r.Now())
	if n == 0 {
		fmt.Fprintln(r.Stdout, "spool-pending: 0 files")
	} else {
		fmt.Fprintf(r.Stdout, "spool-pending: %d files, oldest %s\n", n, oldest.Round(time.Second))
	}
	return true
}

func (r *runner) spoolWritable(res config.Resolved) error {
	if res.Root {
		owner := r.SpoolOwner
		if owner == nil {
			owner = fileOwner
		}
		if err := logdir.PrepareRootSpool(res.SpoolDir, owner); err != nil {
			return err
		}
	} else if err := logdir.Prepare(res.SpoolDir, r.Euid); err != nil {
		return err
	}
	f, err := os.CreateTemp(res.SpoolDir, ".check-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// spoolBacklog counts the finished run files waiting in the spool's signal
// directories and returns the age of the oldest.
func spoolBacklog(dir string, now time.Time) (n int, oldest time.Duration) {
	for _, sig := range []string{"traces", "metrics"} {
		ents, err := os.ReadDir(filepath.Join(dir, sig))
		if err != nil {
			continue
		}
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".") || !strings.HasSuffix(e.Name(), ".jsonl") || !e.Type().IsRegular() {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			n++
			if age := now.Sub(fi.ModTime()); age > oldest {
				oldest = age
			}
		}
	}
	return n, oldest
}
