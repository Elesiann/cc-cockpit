package dashboard

import (
	"github.com/elesiann/cc-cockpit/internal/subagent"
	"time"
)

func loadSubagentRollups(samples []TaggedState, now time.Time) map[string]subagent.Rollup {
	out := make(map[string]subagent.Rollup)
	for _, sample := range samples {
		for sid, sess := range sample.State.Sessions {
			path := jsonRawString(sess.TranscriptPath, "")
			if path == "" {
				path = deriveTranscriptPath(sid, jsonRawString(sess.Cwd, ""))
			}
			if path == "" {
				continue
			}
			if rollup, ok := subagent.ScanParentTranscript(path, now); ok {
				out[sid] = rollup
			}
		}
	}
	return out
}
