package workspace

import (
	"testing"
)

// FuzzParseConfig ensures ParseConfig never panics on arbitrary bytes.
func FuzzParseConfig(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("[workspace]\nname = \"x\""))
	f.Add([]byte("[repos.foo]\nurl = \"github:o/foo\""))
	f.Add([]byte("[[hooks]]\nwhen = [\"pre-update\"]\nrun = [\"foo\"]"))
	f.Add([]byte("\x00\x01malformed"))

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ParseConfig(data)
	})
}

// FuzzResolveHookPath ensures resolveHookPath never panics on arbitrary input.
func FuzzResolveHookPath(f *testing.F) {
	f.Add("/absolute", "/workspace")
	f.Add("./relative", "/workspace")
	f.Add("path-name", "/workspace")
	f.Add("", "")
	f.Add("\x00null", "")

	f.Fuzz(func(t *testing.T, cmd, root string) {
		_, _ = resolveHookPath(cmd, root)
	})
}
