// cc-cockpit — Go port (work in progress).
//
// During Phase 1 of the bash → Go migration, only --version is wired up here.
// All other subcommands remain served by the bash binary at
// .cc-cockpit/bin/cc-cockpit until they're individually ported.
package main

import (
	"fmt"
	"os"
)

const Version = "0.1.0-mvp"

func main() {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "--version", "-v":
			fmt.Printf("cc-cockpit %s\n", Version)
			return
		}
	}
	fmt.Fprintln(os.Stderr, "cc-cockpit: Go port in progress — only --version is implemented.")
	fmt.Fprintln(os.Stderr, "Use the bash binary at .cc-cockpit/bin/cc-cockpit for full functionality.")
	os.Exit(2)
}
