// Package workspace handles .cc-cockpit/workspace.json.
package workspace

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var slugRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// Workspace is the on-disk shape of .cc-cockpit/workspace.json.
type Workspace struct {
	Name  string            `json:"name"`
	Repos map[string]string `json:"repos"`
}

func ValidSlug(s string) bool {
	return slugRe.MatchString(s)
}

// SlugFromPath builds a slug from a path's basename: non-[a-zA-Z0-9._-]
// runes become '-', leading non-alnums are stripped, empty becomes
// "workspace".
func SlugFromPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	base := filepath.Base(abs)
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s := b.String()
	for len(s) > 0 {
		c := s[0]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			break
		}
		s = s[1:]
	}
	if s == "" {
		return "workspace"
	}
	return s
}

// FindRoot walks up from start looking for .cc-cockpit/workspace.json.
// Returns "" if none is found.
func FindRoot(start string) string {
	d, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(d, ".cc-cockpit", "workspace.json")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

func Load(root string) (*Workspace, error) {
	path := filepath.Join(root, ".cc-cockpit", "workspace.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ws Workspace
	if err := json.Unmarshal(raw, &ws); err != nil {
		return nil, fmt.Errorf("workspace.json must be a valid JSON object: %w", err)
	}
	return &ws, nil
}

func (ws *Workspace) Save(root string) error {
	dir := filepath.Join(root, ".cc-cockpit")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "workspace.json")
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(ws); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// AddRepo validates and adds (label, path) to ws. Path may be relative
// (resolved against root) or absolute; either way it must resolve inside
// root and point to an existing git repo.
func (ws *Workspace) AddRepo(root, label, path string) error {
	if !ValidSlug(label) {
		return fmt.Errorf("invalid repo label %q (must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$)", label)
	}
	if _, ok := ws.Repos[label]; ok {
		return fmt.Errorf("duplicate repo label %q; pass explicit labels like api=packages/api", label)
	}
	rel, _, err := resolveRepo(root, path)
	if err != nil {
		return fmt.Errorf("repo %q: %w", label, err)
	}
	if ws.Repos == nil {
		ws.Repos = make(map[string]string)
	}
	ws.Repos[label] = rel
	return nil
}

// CheckRepo validates a repo entry stored in workspace.json.
func CheckRepo(root, rel string) error {
	_, _, err := resolveRepo(root, rel)
	return err
}

func resolveRepo(root, path string) (relPath, absPath string, err error) {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", fmt.Errorf("cannot canonicalize workspace root: %w", err)
	}
	if filepath.IsAbs(path) {
		absPath = filepath.Clean(path)
	} else {
		absPath = filepath.Clean(filepath.Join(root, path))
	}
	if real, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = real
	}
	relPath, err = filepath.Rel(rootReal, absPath)
	if err != nil || relPath == "." || relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("resolves outside workspace root: %s", path)
	}
	info, err := os.Stat(absPath)
	if err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("dir does not exist: %s", path)
	}
	if err := exec.Command("git", "-C", absPath, "rev-parse", "--git-dir").Run(); err != nil {
		return "", "", fmt.Errorf("not a git repo: %s", path)
	}
	return relPath, absPath, nil
}

// DiscoverRepos returns absolute paths of child git repos at depths 1–3
// under root (.git/ at depths 2–4 to match `find -mindepth 2 -maxdepth 4`).
func DiscoverRepos(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate read errors per file (matches find's looseness)
		}
		rel, _ := filepath.Rel(root, path)
		var depth int
		if rel != "." {
			depth = strings.Count(filepath.ToSlash(rel), "/") + 1
		}
		if d.IsDir() && d.Name() == ".git" {
			if depth >= 2 && depth <= 4 {
				found = append(found, filepath.Dir(path))
			}
			return filepath.SkipDir
		}
		if d.IsDir() && depth >= 4 {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(found)
	return found, nil
}
