package exec

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Call records a single invocation made through FakeRunner.
type Call struct {
	Name string
	Args []string
	Opts RunOptions
}

// FakeRunner is a Runner for tests. It returns scripted Results based on the
// (name, args) of each call. Unmatched calls fail loudly.
type FakeRunner struct {
	mu        sync.Mutex
	responses []fakeResponse
	calls     []Call
}

type fakeResponse struct {
	name   string
	args   []string
	result Result
	err    error
}

// NewFakeRunner returns an empty FakeRunner. Use AddResponse to script results.
func NewFakeRunner() *FakeRunner {
	return &FakeRunner{}
}

// AddResponse registers a scripted (result, err) pair for the given (name, args).
// Multiple responses for the same (name, args) are consumed FIFO. If err is
// non-nil, that error is returned to the caller.
func (f *FakeRunner) AddResponse(name string, args []string, result Result, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses = append(f.responses, fakeResponse{name: name, args: args, result: result, err: err})
}

// Run looks up a scripted response. Fails the test (returns error) if none matches.
func (f *FakeRunner) Run(_ context.Context, name string, args []string, opts RunOptions) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Name: name, Args: append([]string{}, args...), Opts: opts})

	for i, r := range f.responses {
		if r.name == name && argsEqual(r.args, args) {
			f.responses = append(f.responses[:i], f.responses[i+1:]...)
			return r.result, r.err
		}
	}
	return Result{}, fmt.Errorf("FakeRunner: no scripted response for: %s %s", name, strings.Join(args, " "))
}

// Calls returns a snapshot of recorded calls in invocation order.
func (f *FakeRunner) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Call, len(f.calls))
	copy(out, f.calls)
	return out
}

// Reset clears recorded calls and pending responses.
func (f *FakeRunner) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
	f.responses = nil
}

func argsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ensure FakeRunner satisfies Runner at compile time
var _ Runner = (*FakeRunner)(nil)
