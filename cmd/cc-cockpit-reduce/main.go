// cc-cockpit-reduce is the Go drop-in for .cc-cockpit/reduce-state.sh.
//
// Reads events.jsonl on stdin, writes the reduced state as pretty-printed
// JSON to stdout. Used during the migration for differential testing
// against the bash reducer; will be folded into cc-cockpit's dashboard
// loop when the dashboard itself is ported.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/elesiann/cc-cockpit/internal/state"
)

func main() {
	st := state.Reduce(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(st); err != nil {
		fmt.Fprintln(os.Stderr, "cc-cockpit-reduce:", err)
		os.Exit(1)
	}
}
