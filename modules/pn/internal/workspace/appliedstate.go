package workspace

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type AppliedState struct {
	AppliedRef string `json:"applied_ref"`
	Dirty      bool   `json:"dirty"`
	AppliedAt  string `json:"applied_at"`
}

func appliedStateDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "pn-workspace", "applied")
}

func appliedStateFile(repoDir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(repoDir)))
	return filepath.Join(appliedStateDir(), fmt.Sprintf("%x", sum))
}

func writeAppliedState(repoDir string, st AppliedState) error {
	if err := os.MkdirAll(appliedStateDir(), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	final := appliedStateFile(repoDir)
	tmp, err := os.CreateTemp(appliedStateDir(), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op if rename succeeded
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, final)
}

func readAppliedState(repoDir string) (AppliedState, bool, error) {
	var st AppliedState
	data, err := os.ReadFile(appliedStateFile(repoDir))
	if os.IsNotExist(err) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, false, err
	}
	return st, true, nil
}
