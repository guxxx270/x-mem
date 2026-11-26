// Package config finds x-mem's folders: the personal layer under the home
// folder, the project layer of the repository you are in, and the state folder.
package config

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
)

// Layers says where the notes and the cache live for one invocation.
type Layers struct {
	Home     string // ~/.x-mem, or $X_MEM_HOME
	Personal string // Home/personal
	State    string // Home/state (never in git)
	Project  string // <repo>/.x-mem, or "" outside a project
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
	h := sha1.Sum([]byte(l.Personal + "\x00" + l.Project))
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
	return out
}

// Root is one layer folder.
type Root struct {
	Name string
	Dir  string
}

// SkipDir reports whether a folder inside a layer holds no notes.
func SkipDir(name string) bool {
	return name == "inbox" || name == "state" || name == "node_modules" || (len(name) > 0 && name[0] == '.')
}
