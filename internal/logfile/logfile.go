// Package logfile 是带轮转的日志文件写入器：写到上限就把当前文件改名成 .1 再重开。
//
// 剪贴板管理器是常驻进程，一跑好几天；只在启动时查一次大小不够用。
package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Writer 是轮转日志写入器（并发安全）。
type Writer struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

// Open 打开（或新建）path 的日志文件；已超过 max 就先轮转一次。
func Open(path string, max int64) (*Writer, error) {
	w := &Writer{path: path, max: max}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if st, err := os.Stat(path); err == nil && st.Size() > max {
		rotate(path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	w.f = f
	if st, err := f.Stat(); err == nil {
		w.size = st.Size()
	}
	return w, nil
}

// Write 实现 io.Writer；这一笔会把文件顶过上限就先轮转。
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, os.ErrClosed
	}
	if w.max > 0 && w.size+int64(len(p)) > w.max {
		if err := w.reopen(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// Close 关掉文件。
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// reopen 轮转并重开（调用方持锁）。
func (w *Writer) reopen() error {
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}
	rotate(w.path)
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	w.f = f
	w.size = 0
	return nil
}

// rotate 把当前文件挪成 .1（旧的 .1 直接丢弃，只留一份历史）。
func rotate(path string) {
	if _, err := os.Stat(path); err != nil {
		return
	}
	old := path + ".1"
	_ = os.Remove(old)
	_ = os.Rename(path, old)
}

// String 方便日志里报自己（测试用）。
func (w *Writer) String() string {
	return fmt.Sprintf("logfile(%s max=%d)", w.path, w.max)
}
