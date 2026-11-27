package extract

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The text of each document is kept under the state folder, so a document
// is read once and again only when its size or modification time changes.
// Each file holds one JSON line about the source, then the text.

type cacheHead struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
	Err   string `json:"error,omitempty"`
}

// CacheDir is where extracted text lives under a state folder.
func CacheDir(state string) string { return filepath.Join(state, "extract") }

// CacheFile is the cache file for a document.
func CacheFile(state, path string) string {
	h := sha1.Sum([]byte(path))
	return filepath.Join(CacheDir(state), hex.EncodeToString(h[:10])+".txt")
}

// Cached returns the text read earlier from this version of the document:
// ok is false when it has not been read yet; errMsg says why a document
// could not be read.
func Cached(state, path string, size, mtime int64) (text, errMsg string, ok bool) {
	data, err := os.ReadFile(CacheFile(state, path))
	if err != nil {
		return "", "", false
	}
	head, body, found := strings.Cut(string(data), "\n")
	var h cacheHead
	if !found && head == "" || json.Unmarshal([]byte(head), &h) != nil {
		return "", "", false
	}
	if h.Path != path || h.Size != size || h.MTime != mtime {
		return "", "", false
	}
	return body, h.Err, true
}

// CachedHead reports whether a document has been read, without its text.
func CachedHead(state, path string, size, mtime int64) (errMsg string, ok bool) {
	f, err := os.Open(CacheFile(state, path))
	if err != nil {
		return "", false
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(f, 4096).ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	var h cacheHead
	if json.Unmarshal([]byte(strings.TrimSpace(line)), &h) != nil || h.Path != path || h.Size != size || h.MTime != mtime {
		return "", false
	}
	return h.Err, true
}

// Store keeps a document's text (or why it could not be read).
func Store(state, path string, size, mtime int64, text string, readErr error) error {
	if err := os.MkdirAll(CacheDir(state), 0o700); err != nil {
		return err
	}
	h := cacheHead{Path: path, Size: size, MTime: mtime}
	if readErr != nil {
		h.Err = readErr.Error()
		text = ""
	}
	head, _ := json.Marshal(h)
	file := CacheFile(state, path)
	tmp := fmt.Sprintf("%s.%d.tmp", file, os.Getpid())
	if err := os.WriteFile(tmp, append(append(head, '\n'), text...), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// Prune removes cached text of documents that no longer exist.
func Prune(state string) int {
	files, _ := filepath.Glob(filepath.Join(CacheDir(state), "*.txt"))
	n := 0
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		line, _ := bufio.NewReaderSize(fh, 4096).ReadString('\n')
		fh.Close()
		var h cacheHead
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &h) != nil {
			continue
		}
		if _, err := os.Stat(h.Path); os.IsNotExist(err) {
			if os.Remove(f) == nil {
				n++
			}
		}
	}
	return n
}

// Lock keeps two background readers from working at once. A lock older
// than an hour is taken to be left over from a crash.
type Lock struct{ path string }

func TryLock(state string) (*Lock, bool) {
	if err := os.MkdirAll(CacheDir(state), 0o700); err != nil {
		return nil, false
	}
	p := filepath.Join(CacheDir(state), "reading.lock")
	for i := 0; i < 2; i++ {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().Format(time.RFC3339))
			f.Close()
			return &Lock{p}, true
		}
		if st, err := os.Stat(p); err == nil && time.Since(st.ModTime()) > time.Hour {
			os.Remove(p)
			continue
		}
		return nil, false
	}
	return nil, false
}

// Busy says whether a background reader holds the lock.
func Busy(state string) bool {
	st, err := os.Stat(filepath.Join(CacheDir(state), "reading.lock"))
	return err == nil && time.Since(st.ModTime()) < time.Hour
}

// Touch keeps the lock fresh during a long run.
func (l *Lock) Touch() { now := time.Now(); os.Chtimes(l.path, now, now) }

func (l *Lock) Release() { os.Remove(l.path) }
