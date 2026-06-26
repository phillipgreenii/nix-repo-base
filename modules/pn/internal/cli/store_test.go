package cli

import (
	"bytes"
	"testing"
)

func TestStoreAudit_HasFullFlag(t *testing.T) {
	root := newRootCmd("1.0.0")
	root.SetArgs([]string{"store", "audit", "--help"})
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("--full")) {
		t.Fatalf("audit --help missing --full:\n%s", buf.String())
	}
}

func TestStoreDeepClean_HasFlags(t *testing.T) {
	root := newRootCmd("1.0.0")
	root.SetArgs([]string{"store", "deepclean", "--help"})
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"--dry-run", "--keep-since", "--keep"} {
		if !bytes.Contains(buf.Bytes(), []byte(f)) {
			t.Errorf("deepclean --help missing %s", f)
		}
	}
}
