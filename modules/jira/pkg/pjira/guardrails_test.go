package pjira

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoForbiddenStrings asserts the generic core stays generic: no ZR strings,
// no OS-specific command names, no pg-pr import. Scans pkg/pjira and cmd/pjira.
func TestNoForbiddenStrings(t *testing.T) {
	forbidden := []string{"ziprecruiter", "zr-jira", "security find-generic-password", "secret-tool", "/pg-pr/", "provider/issues"}
	roots := []string{".", "../../cmd/pjira"}
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			if strings.HasSuffix(path, "guardrails_test.go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			low := strings.ToLower(string(b))
			for _, f := range forbidden {
				if strings.Contains(low, strings.ToLower(f)) {
					t.Errorf("%s contains forbidden token %q (the generic core must stay tenant/OS/pg-pr agnostic)", path, f)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}
