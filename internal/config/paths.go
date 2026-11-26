// Package config finds x-mem's folders: the personal layer under the home
// folder, the project layer of the repository you are in, and the state folder.
package config

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Layers says where the notes and the cache live for one invocation.
type Layers struct {
	Home     string // ~/.x-mem, or $X_MEM_HOME
	Personal string // Home/personal
	State    string // Home/state (never in git)
	Project  string // <repo>/.x-mem, or "" outside a project
	KB       []KB   // knowledge-base folders: reference material, searchable, never written
}

// KB is a folder of reference material (an Obsidian vault, a docs repo):
// searchable with sources, but never counted as something you decided.
type KB struct {
	Path    string   `json:"path"`
	Exclude []string `json:"exclude,omitempty"` // sub-folders or files to leave out, relative to Path
}

// LoadKB reads the knowledge-base folders from config.json in the x-mem home.
func LoadKB(home string) []KB {
	var cfg struct {
		KB []json.RawMessage `json:"kb"`
	}
	data, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil || json.Unmarshal(data, &cfg) != nil {
		return nil
	}
	var out []KB
	for _, raw := range cfg.KB {
		var k KB
		var s string
		if json.Unmarshal(raw, &s) == nil {
			k.Path = s
		} else if json.Unmarshal(raw, &k) != nil {
			continue
		}
		if k.Path = expand(k.Path); k.Path != "" {
			out = append(out, k)
		}
	}
	return out
}

// SaveKB writes the knowledge-base list into config.json, keeping the
// file's other settings.
func SaveKB(home string, kbs []KB) error {
	path := filepath.Join(home, "config.json")
	cfg := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("%s 不是合法的 JSON：%v", path, err)
		}
	}
	if len(kbs) == 0 {
		delete(cfg, "kb")
	} else {
		cfg["kb"] = kbs
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func expand(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, p[2:])
		}
	}
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// DirName is the folder name of a project layer inside a repository.
const DirName = ".x-mem"

// Resolve works out the layers for the given working directory.
func Resolve(cwd string) (Layers, error) {
	home := os.Getenv("X_MEM_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return Layers{}, err
		}
		home = filepath.Join(h, DirName)
	}
	home, _ = filepath.Abs(home)
	l := Layers{
		Home:     home,
		Personal: filepath.Join(home, "personal"),
		State:    filepath.Join(home, "state"),
		KB:       LoadKB(home),
	}
	if p := os.Getenv("X_MEM_PROJECT"); p != "" {
		l.Project, _ = filepath.Abs(p)
		return l, nil
	}
	if cwd != "" {
		l.Project = FindProject(cwd, home)
	}
	return l, nil
}

// FindProject walks up from dir looking for a .x-mem folder that is not the
// x-mem home itself.
func FindProject(dir, home string) string {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		cand := filepath.Join(dir, DirName)
		if cand != home {
			if st, err := os.Stat(cand); err == nil && st.IsDir() {
				return cand
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// RepoRoot returns the nearest ancestor of dir that holds a .git entry.
func RepoRoot(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("not inside a git repository")
		}
		dir = parent
	}
}

// Key identifies this combination of layers, so each project gets its own
// cache file.
func (l Layers) Key() string {
	key := l.Personal + "\x00" + l.Project
	for _, k := range l.KB {
		key += "\x00" + k.Path + "\x01" + strings.Join(k.Exclude, "\x02")
	}
	h := sha1.Sum([]byte(key))
	return hex.EncodeToString(h[:6])
}

// Roots lists the layer roots that exist, personal first.
func (l Layers) Roots() []Root {
	var out []Root
	if st, err := os.Stat(l.Personal); err == nil && st.IsDir() {
		out = append(out, Root{Name: "personal", Dir: l.Personal})
	}
	if l.Project != "" {
		if st, err := os.Stat(l.Project); err == nil && st.IsDir() {
			out = append(out, Root{Name: "project", Dir: l.Project})
		}
	}
	for _, k := range l.KB {
		if st, err := os.Stat(k.Path); err == nil && st.IsDir() && k.Path != l.Personal && k.Path != l.Project {
			out = append(out, Root{Name: "kb", Dir: k.Path, Exclude: k.Exclude})
		}
	}
	return out
}

// Root is one layer folder.
type Root struct {
	Name    string // personal, project or kb
	Dir     string
	Exclude []string // kb only: relative paths left out
}

// Excluded reports whether a path relative to the root is left out.
func (r Root) Excluded(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, e := range r.Exclude {
		e = strings.Trim(filepath.ToSlash(e), "/")
		if e != "" && (rel == e || strings.HasPrefix(rel, e+"/")) {
			return true
		}
	}
	return false
}

// SkipDir reports whether a folder inside a layer holds no notes.
func SkipDir(name string) bool {
	return name == "inbox" || name == "state" || name == "node_modules" || (len(name) > 0 && name[0] == '.')
}
