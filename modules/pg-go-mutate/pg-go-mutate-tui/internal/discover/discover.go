// Package discover walks the configured scan paths to find Go module roots
// ("projects") and, within a project, the packages (directories containing
// at least one non-test .go file) that pg-go-mutate-tui's queue schedules
// mutation runs against.
package discover

import (
	"os"
	"path/filepath"
	"strings"
)

// Package describes one directory within a project that pg-go-mutate-tui
// can queue a mutation run against.
//
//   - ProjectKey identifies the project (currently its root path) this
//     package belongs to.
//   - PkgPath is the package's path relative to the project root.
//   - AbsPath is the package directory's absolute path — the value Task 8's
//     queue passes directly to pkghash.Compute, so it must be a path that
//     function can open, not merely project-relative.
//   - Files lists the package's non-test .go source files; test files
//     inform pkghash but are not independently queued, so they are
//     excluded here.
type Package struct {
	ProjectKey string
	PkgPath    string
	AbsPath    string
	Files      []string
}

// DiscoverProjects walks each of scanPaths and returns one entry per Go
// module root found (a directory containing a go.mod file).
func DiscoverProjects(scanPaths []string) ([]string, error) {
	var projects []string
	for _, root := range scanPaths {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && d.Name() == "go.mod" {
				projects = append(projects, filepath.Dir(path))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return projects, nil
}

// DiscoverPackages walks projectRoot recursively and returns one Package per
// directory containing at least one non-test .go file. projectRoot is
// resolved to an absolute path before walking (even if the caller passed a
// relative one) so every resulting AbsPath is genuinely absolute, per the
// pkghash.Compute contract.
func DiscoverPackages(projectRoot string) ([]Package, error) {
	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, err
	}
	projectRoot = absRoot

	byDir := make(map[string]*Package)
	err = filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		dir := filepath.Dir(path)
		p, ok := byDir[dir]
		if !ok {
			rel, err := filepath.Rel(projectRoot, dir)
			if err != nil {
				return err
			}
			p = &Package{ProjectKey: projectRoot, PkgPath: rel, AbsPath: dir}
			byDir[dir] = p
		}
		p.Files = append(p.Files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	packages := make([]Package, 0, len(byDir))
	for _, p := range byDir {
		packages = append(packages, *p)
	}
	return packages, nil
}
