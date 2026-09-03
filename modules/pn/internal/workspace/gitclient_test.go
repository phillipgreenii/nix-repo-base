// internal/workspace/gitclient_test.go
package workspace

import (
	"context"
	"fmt"
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
	status           []gitclient.StatusEntry
	statusErr        error
	isTracked        map[string]bool
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

func (f *fakeGitReader) Status(ctx context.Context) ([]gitclient.StatusEntry, error) {
	return f.status, f.statusErr
}

func (f *fakeGitReader) IsTracked(ctx context.Context, path string) (bool, error) {
	return f.isTracked[path], nil
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

// fakeGitMutator is a hand-rolled test double for gitMutator (mirroring
// fakeGitReader's rationale above — design section 4's D4). Handle-returning
// methods use gitclient.NewFakeHandle so callers can .Wait()/.AttachStream()
// the result exactly like a real *gitclient.Handle (bead pg2-f1cq7's "new
// testing problem", solved by NewFakeHandle rather than a HandleLike
// interface). Zero-value fields succeed with no output unless set.
type fakeGitMutator struct {
	// name and log, when both set, make every call append "<name>:<method>"
	// to *log — a shared ordering trace across multiple fakeGitMutator
	// instances (one per repo), for tests asserting call order between repos.
	name string
	log  *[]string

	fetchErr    error
	fetchStdout string
	fetchStderr string

	createWorktreeErr  error
	createWorktrees    []string                          // "path@branch" per call, in order
	createWorktreeOpts []gitclient.CreateWorktreeOptions // parallel to createWorktrees

	syncErr  error
	syncOpts []gitclient.SyncOptions

	restorePathErr error
	restorePaths   []string

	addErr   error
	addCalls [][]string

	commitErr  error
	commitMsgs []string

	pushErr  error
	pushOpts []gitclient.PushOptions

	listBranches    []string
	listBranchesErr error

	addRemoteErr error
	addRemotes   map[string]string
}

var _ gitMutator = (*fakeGitMutator)(nil)

// record appends "<name>:<method>" to *f.log when both are set — see the
// name/log fields' doc comment.
func (f *fakeGitMutator) record(method string) {
	if f.log != nil {
		*f.log = append(*f.log, f.name+":"+method)
	}
}

func (f *fakeGitMutator) Fetch(ctx context.Context, opts gitclient.FetchOptions) (*gitclient.Handle, error) {
	f.record("fetch")
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	return gitclient.NewFakeHandle([]byte(f.fetchStdout), []byte(f.fetchStderr), nil), nil
}

func (f *fakeGitMutator) CreateWorktree(ctx context.Context, path, branch string, opts gitclient.CreateWorktreeOptions) (*gitclient.Handle, error) {
	f.createWorktrees = append(f.createWorktrees, path+"@"+branch)
	f.createWorktreeOpts = append(f.createWorktreeOpts, opts)
	if f.createWorktreeErr != nil {
		return nil, f.createWorktreeErr
	}
	return gitclient.NewFakeHandle(nil, nil, nil), nil
}

func (f *fakeGitMutator) RemoveWorktree(ctx context.Context, path string, force bool) error {
	return nil
}

func (f *fakeGitMutator) PruneWorktrees(ctx context.Context) error {
	return nil
}

func (f *fakeGitMutator) Sync(ctx context.Context, opts gitclient.SyncOptions) (*gitclient.Handle, error) {
	f.record("sync")
	f.syncOpts = append(f.syncOpts, opts)
	if f.syncErr != nil {
		return nil, f.syncErr
	}
	return gitclient.NewFakeHandle(nil, nil, nil), nil
}

func (f *fakeGitMutator) RestorePath(ctx context.Context, path string) error {
	f.restorePaths = append(f.restorePaths, path)
	return f.restorePathErr
}

func (f *fakeGitMutator) Add(ctx context.Context, paths ...string) error {
	f.addCalls = append(f.addCalls, append([]string(nil), paths...))
	return f.addErr
}

func (f *fakeGitMutator) Commit(ctx context.Context, message string) (*gitclient.Handle, error) {
	f.commitMsgs = append(f.commitMsgs, message)
	if f.commitErr != nil {
		return nil, f.commitErr
	}
	return gitclient.NewFakeHandle(nil, nil, nil), nil
}

func (f *fakeGitMutator) Push(ctx context.Context, opts gitclient.PushOptions) (*gitclient.Handle, error) {
	f.record("push")
	f.pushOpts = append(f.pushOpts, opts)
	if f.pushErr != nil {
		return nil, f.pushErr
	}
	return gitclient.NewFakeHandle(nil, nil, nil), nil
}

func (f *fakeGitMutator) ListBranches(ctx context.Context) ([]string, error) {
	return f.listBranches, f.listBranchesErr
}

func (f *fakeGitMutator) AddRemote(ctx context.Context, name, url string) error {
	if f.addRemoteErr != nil {
		return f.addRemoteErr
	}
	if f.addRemotes == nil {
		f.addRemotes = map[string]string{}
	}
	f.addRemotes[name] = url
	return nil
}

// stubGitMutatorOpener installs a gitMutatorOpener that returns mutators
// keyed by dir, mirroring stubGitOpener.
func stubGitMutatorOpener(t *testing.T, mutators map[string]*fakeGitMutator) {
	t.Helper()
	prev := openGitMutator
	openGitMutator = func(ctx context.Context, dir string) (gitMutator, error) {
		if m, ok := mutators[dir]; ok {
			return m, nil
		}
		return nil, gitclient.ErrNotARepository
	}
	t.Cleanup(func() { openGitMutator = prev })
}

// stubGitCloner installs a gitCloner keyed by url, mirroring stubGitOpener.
// A url with no entry fails with a generic error, matching gitclient.Clone's
// behavior when the remote cannot be reached.
func stubGitCloner(t *testing.T, clones map[string]*fakeGitMutator) {
	t.Helper()
	prev := cloneGitRepo
	cloneGitRepo = func(ctx context.Context, url, dir string, opts gitclient.CloneOptions) (gitMutator, *gitclient.Handle, error) {
		m, ok := clones[url]
		if !ok {
			return nil, nil, fmt.Errorf("fake clone: no fixture for url %q", url)
		}
		return m, gitclient.NewFakeHandle(nil, nil, nil), nil
	}
	t.Cleanup(func() { cloneGitRepo = prev })
}
