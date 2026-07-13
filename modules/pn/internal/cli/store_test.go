package cli

import (
	"bytes"
	"strings"
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
	for _, f := range []string{"--dry-run", "--keep-since", "--keep", "--yes"} {
		if !bytes.Contains(buf.Bytes(), []byte(f)) {
			t.Errorf("deepclean --help missing %s", f)
		}
	}
}

// TestConfirmDeepClean exercises the confirmation gate (bead pg2-w0y8u) without
// touching the real runner: a dry run or --yes proceeds; a non-interactive run
// without --yes is refused; an interactive run honors the y/N answer.
func TestConfirmDeepClean(t *testing.T) {
	cases := []struct {
		name          string
		dryRun, yes   bool
		interactive   bool
		input         string
		wantProceed   bool
		wantErr       bool
		wantAbortHint bool // error text should point at --yes
	}{
		{name: "dry run proceeds without prompt", dryRun: true, wantProceed: true},
		{name: "--yes proceeds without prompt", yes: true, wantProceed: true},
		{name: "non-interactive without --yes is refused", interactive: false, wantErr: true, wantAbortHint: true},
		{name: "interactive yes", interactive: true, input: "y\n", wantProceed: true},
		{name: "interactive full yes", interactive: true, input: "YES\n", wantProceed: true},
		{name: "interactive no", interactive: true, input: "n\n", wantProceed: false},
		{name: "interactive empty defaults to no", interactive: true, input: "\n", wantProceed: false},
		{name: "interactive EOF is no", interactive: true, input: "", wantProceed: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var errOut bytes.Buffer
			proceed, err := confirmDeepClean(strings.NewReader(c.input), &errOut, c.dryRun, c.yes, c.interactive)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, c.wantErr)
			}
			if c.wantAbortHint && (err == nil || !strings.Contains(err.Error(), "--yes")) {
				t.Errorf("refusal error should mention --yes; got %v", err)
			}
			if proceed != c.wantProceed {
				t.Errorf("proceed = %v, want %v", proceed, c.wantProceed)
			}
		})
	}
}

// TestReadYes covers the affirmative parsing directly.
func TestReadYes(t *testing.T) {
	for in, want := range map[string]bool{
		"y\n": true, "Y\n": true, "yes\n": true, "  Yes  \n": true,
		"n\n": false, "no\n": false, "": false, "maybe\n": false,
	} {
		if got := readYes(strings.NewReader(in)); got != want {
			t.Errorf("readYes(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestStoreDeepClean_RejectsPositionalArgs verifies cobra.NoArgs rejects a
// stray positional argument before RunE (and the real runner) can run.
func TestStoreDeepClean_RejectsPositionalArgs(t *testing.T) {
	root := newRootCmd("1.0.0")
	root.SetArgs([]string{"store", "deepclean", "unexpected"})
	root.SilenceErrors = true
	root.SilenceUsage = true
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	if err := root.Execute(); err == nil {
		t.Fatal("expected error for unexpected positional arg to `store deepclean`")
	}
}

// TestStoreAudit_RejectsPositionalArgs verifies cobra.NoArgs on `store audit`.
func TestStoreAudit_RejectsPositionalArgs(t *testing.T) {
	root := newRootCmd("1.0.0")
	root.SetArgs([]string{"store", "audit", "unexpected"})
	root.SilenceErrors = true
	root.SilenceUsage = true
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	if err := root.Execute(); err == nil {
		t.Fatal("expected error for unexpected positional arg to `store audit`")
	}
}

// TestStoreDeepClean_NonInteractiveRefused verifies that a bare `store
// deepclean` in a non-interactive session (as under `go test`) is refused
// before the real runner is constructed, so no privileged command runs.
func TestStoreDeepClean_NonInteractiveRefused(t *testing.T) {
	orig := isInteractive
	isInteractive = func() bool { return false }
	t.Cleanup(func() { isInteractive = orig })

	root := newRootCmd("1.0.0")
	root.SetArgs([]string{"store", "deepclean"})
	root.SilenceErrors = true
	root.SilenceUsage = true
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	err := root.Execute()
	if err == nil {
		t.Fatal("expected non-interactive deepclean without --yes to be refused")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("refusal should mention --yes; got %v", err)
	}
}
