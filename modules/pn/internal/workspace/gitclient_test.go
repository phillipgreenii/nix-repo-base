// internal/workspace/gitclient_test.go
package workspace

import (
	"context"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
)

// fakeGitReader is a hand-rolled test double for gitReader (design section
// 4's D4: "with role interfaces this small, a hand-rolled fake is a few
// lines"). Zero-value fields return the zero result with no error unless
// explicitly set.
type fakeGitReader struct {
	commonDir        string
	commonDirErr     error
	currentBranch    string
	currentBranchErr error // set to gitclient.ErrDetachedHEAD to simulate a detached HEAD
	remoteURL        map[string]string
	remoteURLErr     map[string]error
	refExists        map[string]bool
	refExistsErr     map[string]error
	hasUpstreamVal   bool
	hasUpstreamErr   error
	commitsAhead     int
	commitsAheadErr  error
}

var _ gitReader = (*fakeGitReader)(nil)

func (f *fakeGitReader) Toplevel(ctx context.Context) (string, error) {
	return "", nil
}

func (f *fakeGitReader) CommonDir(ctx context.Context) (string, error) {
	return f.commonDir, f.commonDirErr
}

func (f *fakeGitReader) CurrentBranch(ctx context.Context) (string, error) {
	return f.currentBranch, f.currentBranchErr
}

func (f *fakeGitReader) RemoteURL(ctx context.Context, remote string) (string, error) {
	if err, ok := f.remoteURLErr[remote]; ok {
		return "", err
	}
	return f.remoteURL[remote], nil
}

func (f *fakeGitReader) RefExists(ctx context.Context, ref string) (bool, error) {
	if err, ok := f.refExistsErr[ref]; ok {
		return false, err
	}
	return f.refExists[ref], nil
}

func (f *fakeGitReader) HasUpstream(ctx context.Context) (bool, error) {
	return f.hasUpstreamVal, f.hasUpstreamErr
}

func (f *fakeGitReader) CommitsAhead(ctx context.Context, base, tip string) (int, error) {
	return f.commitsAhead, f.commitsAheadErr
}

// stubGitOpener installs a gitOpener that returns readers keyed by dir, for
// the remainder of the calling test (restored via t.Cleanup). A dir with no
// entry gets gitclient.ErrNotARepository, matching gitclient.New's real
// failure mode for a path outside a git repository.
func stubGitOpener(t *testing.T, readers map[string]*fakeGitReader) {
	t.Helper()
	prev := openGitReader
	openGitReader = func(ctx context.Context, dir string) (gitReader, error) {
		if r, ok := readers[dir]; ok {
			return r, nil
		}
		return nil, gitclient.ErrNotARepository
	}
	t.Cleanup(func() { openGitReader = prev })
}
