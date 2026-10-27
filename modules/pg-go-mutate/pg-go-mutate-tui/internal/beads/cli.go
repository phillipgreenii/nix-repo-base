package beads

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// CLIClient is the production BdClient: it shells out to the real `bd`.
//
// Every call is `bd -C <Dir> create ...`, so the bead lands in the tracker of
// the repository that holds the package (the repo where the fix will land), not
// in whatever repository the process happens to run from.
type CLIClient struct {
	// Bin is the bd executable; "" means "bd" resolved from PATH (never a
	// nixpkgs runtimeDep, so the machine's wrapped bd is the one that runs).
	Bin string
	// Dir is the repository root whose tracker receives the bead.
	Dir string
	// Env, when non-nil, is the whole environment of the bd child. nil inherits
	// the parent's. Tests pass a hermetic allowlist here.
	Env []string
	// Actor is passed as --actor ("" omits it).
	Actor string
}

// Create files one task bead and returns its id. The body goes in on stdin
// (--body-file -), so its size and its backticks or `$` never touch argv or a
// shell. --silent makes bd print only the id, so no JSON envelope setting can
// change the parse.
func (c *CLIClient) Create(title, body string, labels []string, priority int) (string, error) {
	bin := c.Bin
	if bin == "" {
		bin = "bd"
	}
	args := []string{"-C", c.Dir}
	if c.Actor != "" {
		args = append(args, "--actor", c.Actor)
	}
	args = append(args, "create", "--title", title, "--body-file", "-",
		"--type", "task", "-p", strconv.Itoa(priority), "-l", strings.Join(labels, ","), "--silent")
	cmd := exec.Command(bin, args...)
	cmd.Env = c.Env
	cmd.Stdin = strings.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("beads: bd create in %s: %w: %s", c.Dir, err, strings.TrimSpace(stderr.String()))
	}
	fields := strings.Fields(stdout.String())
	if len(fields) == 0 {
		return "", fmt.Errorf("beads: bd create in %s printed no bead id", c.Dir)
	}
	return fields[len(fields)-1], nil
}
