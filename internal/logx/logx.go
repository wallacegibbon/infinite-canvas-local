// Package logx is a minimal logger that matches the previous implementation's format.
//
// Format: [2006-01-02T15:04:05.000Z] [prefix] LEVEL message
package logx

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Logger is a concurrency-safe line logger.
type Logger struct {
	prefix string
	out    io.Writer
	mu     sync.Mutex
}

// New builds a logger; prefix is usually "gateway".
func New(prefix string) *Logger {
	return &Logger{prefix: prefix, out: os.Stdout}
}

// NewWithWriter injects an output sink for tests.
func NewWithWriter(prefix string, out io.Writer) *Logger {
	return &Logger{prefix: prefix, out: out}
}

// Info logs ordinary information.
func (l *Logger) Info(format string, args ...any) { l.write("INFO", format, args...) }

// Warn logs a recoverable problem.
func (l *Logger) Warn(format string, args ...any) { l.write("WARN", format, args...) }

// Error logs a failure.
func (l *Logger) Error(format string, args ...any) { l.write("ERROR", format, args...) }

func (l *Logger) write(level, format string, args ...any) {
	if l == nil {
		return
	}
	message := format
	if len(args) > 0 {
		message = fmt.Sprintf(format, args...)
	}
	line := fmt.Sprintf("[%s] [%s] %s %s\n", timestamp(), l.prefix, level, message)
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.out, line)
}

// timestamp renders millisecond UTC, matching JS Date#toISOString.
func timestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
