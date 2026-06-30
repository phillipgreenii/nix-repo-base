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
		{1024, "1.0 KiB"},
		{1048576, "1.0 MiB"},
		{1073741824, "1.0 GiB"},
		{54440673280, "50.7 GiB"},
	}
	for _, c := range cases {
		if got := formatSize(c.in); got != c.want {
			t.Errorf("formatSize(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// fullDiskutilInfo is a verbatim `diskutil info <device>` block for an APFS
// /nix/store volume. The "Disk Size" / "Container Total Space" lines (the whole
// container, 494384795648 B ≈ 460.4 GiB) precede "Volume Used Space"
// (67730395136 B ≈ 63.1 GiB). storeSize MUST report the latter — a regex that
// grabs the first "(NNN Bytes)" match instead returns the container total.
const fullDiskutilInfo = `   Device Identifier:         disk3s7
   Device Node:               /dev/disk3s7
   Whole:                     No
   Part of Whole:             disk3

   Volume Name:               Nix Store
   Mounted:                   Yes
   Mount Point:               /nix

   File System Personality:   APFS
   Type (Bundle):             apfs

   Disk Size:                 494.4 GB (494384795648 Bytes) (exactly 965595304 512-Byte-Units)
   Device Block Size:         4096 Bytes

   Volume Used Space:         63.1 GB (67730395136 Bytes) (exactly 132285928 512-Byte-Units)
   Container Total Space:     494.4 GB (494384795648 Bytes) (exactly 965595304 512-Byte-Units)
   Container Free Space:      38.3 GB (38289293312 Bytes) (exactly 74783776 512-Byte-Units)
   Allocation Block Size:     4096 Bytes
`

func TestStoreSize_ParsesDiskutilBytes(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("df", []string{"/nix/store"}, exec.Result{Stdout: []byte(
		"Filesystem 1K-blocks Used Available Use% Mounted on\n/dev/disk3s7 1 1 1 1% /nix/store\n")}, nil)
	f.AddResponse("diskutil", []string{"info", "/dev/disk3s7"}, exec.Result{Stdout: []byte(fullDiskutilInfo)}, nil)
	// Must report Volume Used Space (63.1 GiB), NOT Disk Size / Container Total
	// (460.4 GiB), which is the first "(NNN Bytes)" match in the full output.
	if got := storeSize(context.Background(), f); got != "63.1 GiB" {
		t.Fatalf("storeSize = %q, want 63.1 GiB (Volume Used Space, not container total)", got)
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
	// 1048576 bytes = 1.0 MiB; one lsof-only path → singular "path"
	want := "1 store path held only by running processes (up to 1.0 MiB reclaimable)\n" +
		"  Tip: Restarting applications and re-running may free additional space"
	if got != want {
		t.Fatalf("runtimeRootsSummary = %q, want %q", got, want)
	}
}
