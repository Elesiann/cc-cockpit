// Package recap extracts Claude Code's native session recap — the
// `away_summary` event Claude writes into its transcript ~3 minutes after a
// session goes quiet. cc-cockpit surfaces it in `watch` so an operator
// juggling several sessions can see "what was that one doing?" without
// re-opening it.
//
// This package only reads. It never writes a transcript and never invokes
// `claude` — the recap is already on disk, generated for free by Claude Code
// itself. That keeps cc-cockpit's "observation, not orchestration" principle
// intact.
package recap

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"time"
)

// Recap is one extracted away_summary: the recap text plus when Claude wrote
// it. Age lets the renderer show a "(5m ago)" hint so a stale recap is
// visibly stale.
type Recap struct {
	Text string
	At   time.Time
}

// awaySummaryLine is the subset of a transcript event we care about. Claude
// Code transcripts are JSONL; the recap is a system event:
//
//	{"type":"system","subtype":"away_summary","content":"Goal: ...","timestamp":"..."}
type awaySummaryLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

// disableHint is the trailing parenthetical Claude appends to every recap.
// It's UI chrome for the interactive session, noise in the cockpit table.
const disableHint = "(disable recaps in /config)"

// Read returns the most recent away_summary in the transcript at path.
// ok is false when the file is missing, unreadable, or carries no recap yet
// (a session whose first quiet period hasn't elapsed). A missing transcript
// is a normal, expected state — not an error.
//
// The whole file is scanned because away_summary events are sparse and may
// sit anywhere; transcripts are typically a few MB at most, and the runtime
// layer caches on mtime so this is not a per-tick cost.
func Read(path string) (Recap, bool) {
	if path == "" {
		return Recap{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return Recap{}, false
	}
	defer f.Close()

	var latest Recap
	found := false

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		// Cheap pre-filter: skip the JSON parse for the ~99% of lines
		// that can't possibly be the recap event.
		if !strings.Contains(string(line), "away_summary") {
			continue
		}
		var ev awaySummaryLine
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type != "system" || ev.Subtype != "away_summary" {
			continue
		}
		text := Clean(ev.Content)
		if text == "" {
			continue
		}
		at, _ := time.Parse(time.RFC3339, ev.Timestamp)
		// Last writer wins: transcripts are append-only and ordered, so
		// the final away_summary line is the freshest recap.
		latest = Recap{Text: text, At: at}
		found = true
	}
	if err := sc.Err(); err != nil {
		return Recap{}, false
	}
	return latest, found
}

// Clean trims the recap content for table display: strips the trailing
// "(disable recaps in /config)" hint and collapses internal whitespace so a
// multi-line recap renders predictably.
func Clean(content string) string {
	s := strings.TrimSpace(content)
	s = strings.TrimSuffix(s, disableHint)
	s = strings.TrimSpace(s)
	return strings.Join(strings.Fields(s), " ")
}
