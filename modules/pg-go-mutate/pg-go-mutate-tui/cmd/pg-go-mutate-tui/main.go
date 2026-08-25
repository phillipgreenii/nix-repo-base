package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("pg-go-mutate-tui", flag.ContinueOnError)
	root := fs.String("root", "", "root directory to scan (required)")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		fmt.Println(version)
		return 0
	}
	if *root == "" {
		fmt.Fprintln(os.Stderr, "pg-go-mutate-tui: --root is required")
		return 2
	}
	// A later task wires discovery/ledger/queue/worker-pool/TUI here.
	return 0
}
