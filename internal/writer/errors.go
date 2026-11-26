package writer

import "errors"

// ErrBusy means another writer holds the lock.
var ErrBusy = errors.New("另一个写入进程正在写，稍后再试")
