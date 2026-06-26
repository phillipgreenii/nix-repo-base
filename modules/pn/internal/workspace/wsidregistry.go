package workspace

import (
	"fmt"
	"os"
	"path/filepath"
)

func wsidRegistryDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "pn-workspace", "wsids")
}

// checkWsidUnique records wsid -> root in the machine-local registry and fails
// if a different root already claims this wsid (a same-machine duplicate). A
// re-claim from the same root is a no-op success. Cross-machine uniqueness is
// the operator's responsibility (the slug is human-chosen).
func checkWsidUnique(wsid, root string) error {
	root = filepath.Clean(root)
	file := filepath.Join(wsidRegistryDir(), wsid)
	if data, err := os.ReadFile(file); err == nil {
		stored := string(data)
		if stored != root {
			return fmt.Errorf("wsid %q already used by workspace %q (must be unique per machine)", wsid, stored)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(wsidRegistryDir(), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(wsidRegistryDir(), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if rename succeeded
	if _, err := tmp.WriteString(root); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, file)
}
