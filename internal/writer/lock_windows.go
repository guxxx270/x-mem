//go:build windows

package writer

import (
	"os"
	"path/filepath"
	"time"
)

// Lock is held by whoever is writing notes.
type Lock struct{ path string }

// Acquire waits up to timeout for the writer lock. On Windows it is a file
// created exclusively; a lock older than ten minutes is taken as stale.
func Acquire(state string, timeout time.Duration) (*Lock, error) {
	if err := os.MkdirAll(state, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(state, "writer.lock.d")
	deadline := time.Now().Add(timeout)
	for {
		if err := os.Mkdir(path, 0o700); err == nil {
			return &Lock{path}, nil
		}
		if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) > 10*time.Minute {
			os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, ErrBusy
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Release lets the next writer in.
func (l *Lock) Release() {
	if l != nil {
		os.Remove(l.path)
	}
}
