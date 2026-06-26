package store

import (
	"context"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestFormatSize(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
		{54440673280, "50.7 GB"},
	}
	for _, c := range cases {
		if got := formatSize(c.in); got != c.want {
			t.Errorf("formatSize(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStoreSize_ParsesDiskutilBytes(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("df", []string{"/nix/store"}, exec.Result{Stdout: []byte(
		"Filesystem 1K-blocks Used Available Use% Mounted on\n/dev/disk3s7 1 1 1 1% /nix/store\n")}, nil)
	f.AddResponse("diskutil", []string{"info", "/dev/disk3s7"}, exec.Result{Stdout: []byte(
		"   Volume Used Space:         50.7 GB (54440673280 Bytes) (exactly X 512-Byte-Units)\n")}, nil)
	if got := storeSize(context.Background(), f); got != "50.7 GB" {
		t.Fatalf("storeSize = %q, want 50.7 GB", got)
	}
}

func TestStoreSize_NoVolumeUsedSpace(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("df", []string{"/nix/store"}, exec.Result{Stdout: []byte("h\n/dev/disk3s7 1 1 1 1% /nix/store\n")}, nil)
	f.AddResponse("diskutil", []string{"info", "/dev/disk3s7"}, exec.Result{Stdout: []byte("   Volume Name: Nix Store\n")}, nil)
	if got := storeSize(context.Background(), f); got != "0 B" {
		t.Fatalf("storeSize = %q, want 0 B", got)
	}
}

func TestProfileClosureSize_UnknownOnError(t *testing.T) {
	f := exec.NewFakeRunner() // no response scripted → Run errors
	if got := profileClosureSize(context.Background(), f, "/nope"); got != "unknown" {
		t.Fatalf("profileClosureSize = %q, want unknown", got)
	}
}

func TestRuntimeRootsSummary_LsofOnly(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("nix-store", []string{"--gc", "--print-roots"}, exec.Result{Stdout: []byte(
		"{lsof} -> /nix/store/aaa-pkg\n/some/file -> /nix/store/bbb-pkg\n")}, nil)
	f.AddResponse("nix", []string{"path-info", "-S", "/nix/store/aaa-pkg"}, exec.Result{Stdout: []byte(
		"/nix/store/aaa-pkg 1048576\n")}, nil)
	got := runtimeRootsSummary(context.Background(), f)
	// 1048576 bytes = 1.0 MB; one lsof-only path → singular "path"
	want := "1 store path held only by running processes (up to 1.0 MB reclaimable)\n" +
		"  Tip: Restarting applications and re-running may free additional space"
	if got != want {
		t.Fatalf("runtimeRootsSummary = %q, want %q", got, want)
	}
}
