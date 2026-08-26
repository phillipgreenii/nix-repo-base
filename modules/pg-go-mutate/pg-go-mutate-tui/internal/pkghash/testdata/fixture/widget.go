// Package fixture is test fixture content for the pgm_pkg_hash / pkghash.Compute
// cross-check (Task 5, Step 6). It is never built or vetted: `go build`/`go vet`
// skip directories named "testdata" by convention.
package fixture

// Widget is a fixture type that gives the fixture directory real, varied
// content to hash.
type Widget struct {
	ID   int
	Name string
}

// Describe returns a human-readable label for the widget.
func (w Widget) Describe() string {
	return "widget:" + w.Name
}
