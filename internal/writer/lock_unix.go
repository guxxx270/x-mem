//go:build !windows

package writer

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Lock is held by whoever is writing notes. There is exactly one writer at a
// time, so two processes never edit the store or run git at once.
type Lock struct{ f *os.File }

// Acquire waits up to timeout for the writer lock.
func Acquire(state string, timeout time.Duration) (*Lock, error) {
	if err := os.MkdirAll(state, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(state, "writer.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			f.Truncate(0)
			fmt.Fprintf(f, "%d\n", os.Getpid())
			return &Lock{f}, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, ErrBusy
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Release lets the next writer in.
func (l *Lock) Release() {
	if l != nil && l.f != nil {
		syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		l.f.Close()
	}
}
