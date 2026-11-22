package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const defaultMaxBytes int64 = 8 * 1024 * 1024

type Config struct {
	Dir      string
	Role     string
	MaxBytes int64
	Now      func() time.Time
}

type Logger struct {
	mu       sync.Mutex
	dir      string
	role     string
	maxBytes int64
	now      func() time.Time
	file     *os.File
	date     string
	index    int
	bytes    int64
	closed   bool
}

func New(cfg Config) (*Logger, error) {
	dir := strings.TrimSpace(cfg.Dir)
	if dir == "" {
		dir = "logs"
	}
	role := strings.TrimSpace(cfg.Role)
	if role == "" {
		role = "app"
	}
	role = strings.NewReplacer("/", "_", "\\", "_", " ", "_").Replace(role)
	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	l := &Logger{dir: dir, role: role, maxBytes: maxBytes, now: now}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	if err := l.openLocked(now()); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Logger) openLocked(now time.Time) error {
	date := now.Format("2006-01-02")
	if l.file != nil && l.date == date && l.bytes < l.maxBytes {
		return nil
	}
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
	if l.date != date {
		l.index = 0
	}
	l.date = date
	for {
		name := fmt.Sprintf("%s-%s.log", l.role, date)
		if l.index > 0 {
			name = fmt.Sprintf("%s-%s-%d.log", l.role, date, l.index)
		}
		path := filepath.Join(l.dir, name)
		info, err := os.Stat(path)
		if err == nil && info.Size() >= l.maxBytes {
			l.index++
			continue
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		l.file = file
		if info != nil {
			l.bytes = info.Size()
		} else {
			l.bytes = 0
		}
		return nil
	}
}

func (l *Logger) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return 0, os.ErrClosed
	}
	if err := l.openLocked(l.now()); err != nil {
		return 0, err
	}
	if l.bytes > 0 && l.bytes+int64(len(p)) > l.maxBytes {
		_ = l.file.Close()
		l.file = nil
		l.index++
		if err := l.openLocked(l.now()); err != nil {
			return 0, err
		}
	}
	n, err := l.file.Write(p)
	l.bytes += int64(n)
	return n, err
}

func (l *Logger) Printf(format string, args ...any) {
	_, _ = fmt.Fprintf(l, "%s [%s] %s\n", l.now().Format(time.RFC3339), l.role, fmt.Sprintf(format, args...))
}

func (l *Logger) CurrentPath() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return ""
	}
	return l.file.Name()
}

func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

var _ io.Writer = (*Logger)(nil)
