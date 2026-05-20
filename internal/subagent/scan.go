// Package subagent reads Claude Code sidechain/subagent transcripts and returns
// small parent-session rollups for dashboard display.
package subagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ActiveAfter is the recency window used for the MVP active/done heuristic.
// Claude Code does not currently expose a stable subagent status file, so a
// recently modified transcript means "active"; older transcripts mean "done".
const ActiveAfter = 2 * time.Minute

// Rollup is the one-line summary cc-cockpit renders below a parent session.
type Rollup struct {
	Total             int
	Active            int
	Done              int
	LatestDescription string
	LatestActivity    time.Time
}

type metaFile struct {
	Description string `json:"description"`
	AgentType   string `json:"agentType"`
	ToolUseID   string `json:"toolUseId"`
}

type agentSummary struct {
	description  string
	lastActivity time.Time
}

// ScanParentTranscript scans the sibling sidechain directory for a parent
// transcript: <parent>.jsonl -> <parent>/subagents/agent-*.{jsonl,meta.json}.
// Missing/empty dirs return ok=false; malformed individual agents are skipped.
func ScanParentTranscript(parentTranscriptPath string, now time.Time) (Rollup, bool) {
	if parentTranscriptPath == "" {
		return Rollup{}, false
	}
	base := strings.TrimSuffix(parentTranscriptPath, filepath.Ext(parentTranscriptPath))
	subdir := filepath.Join(base, "subagents")
	entries, err := os.ReadDir(subdir)
	if err != nil {
		return Rollup{}, false
	}
	byID := map[string]agentSummary{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".jsonl")
		info, err := e.Info()
		if err != nil {
			continue
		}
		desc := readDescription(filepath.Join(subdir, id+".meta.json"))
		if desc == "" {
			desc = id
		}
		byID[id] = agentSummary{description: desc, lastActivity: info.ModTime()}
	}
	if len(byID) == 0 {
		return Rollup{}, false
	}
	agents := make([]agentSummary, 0, len(byID))
	for _, a := range byID {
		agents = append(agents, a)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].lastActivity.After(agents[j].lastActivity) })
	out := Rollup{Total: len(agents), LatestDescription: agents[0].description, LatestActivity: agents[0].lastActivity}
	for _, a := range agents {
		if now.Sub(a.lastActivity) <= ActiveAfter {
			out.Active++
		} else {
			out.Done++
		}
	}
	return out, true
}

func readDescription(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var m metaFile
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	return strings.TrimSpace(m.Description)
}
