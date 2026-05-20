package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/elesiann/cc-cockpit/internal/recap"
)

type recapCacheEntry struct {
	mtime time.Time
	text  string
	ok    bool
}

type recapCache struct {
	byPath map[string]recapCacheEntry
}

func newRecapCache() *recapCache {
	return &recapCache{byPath: make(map[string]recapCacheEntry)}
}

// load returns session_id → recap text for every session whose transcript has
// a native Claude Code away_summary. Missing recaps are omitted. Transcript
// files are only re-read when their mtime changes, so watch's 500ms loop stays
// cheap even with several active sessions.
func (c *recapCache) load(samples []TaggedState) map[string]string {
	out := make(map[string]string)
	for _, sample := range samples {
		for sid, sess := range sample.State.Sessions {
			path := jsonRawString(sess.TranscriptPath, "")
			if path == "" {
				path = deriveTranscriptPath(sid, jsonRawString(sess.Cwd, ""))
			}
			if path == "" {
				continue
			}
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			entry, cached := c.byPath[path]
			if !cached || !entry.mtime.Equal(info.ModTime()) {
				r, ok := recap.Read(path)
				entry = recapCacheEntry{mtime: info.ModTime(), ok: ok}
				if ok {
					entry.text = r.Text
				}
				c.byPath[path] = entry
			}
			if entry.ok && entry.text != "" {
				out[sid] = entry.text
			}
		}
	}
	return out
}

func deriveTranscriptPath(sessionID, cwd string) string {
	if sessionID == "" || cwd == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	clean := filepath.Clean(cwd)
	// Claude Code stores transcripts under ~/.claude/projects/<cwd-slug>/
	// where an absolute path like /home/gio/devportal/platform becomes
	// -home-gio-devportal-platform. This fallback keeps recaps working for
	// sessions started before cc-cockpit began persisting transcript_path.
	slug := strings.ReplaceAll(clean, string(os.PathSeparator), "-")
	return filepath.Join(home, ".claude", "projects", slug, sessionID+".jsonl")
}
