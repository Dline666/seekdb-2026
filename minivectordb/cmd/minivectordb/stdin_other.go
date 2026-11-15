//go:build !(linux && amd64)

package main

import (
	"io"
	"os"
)

// newStdin returns the reader for request lines and a function that makes a
// pending or future Read fail promptly; the signal watcher calls it.
func newStdin(logf func(string, ...any)) (io.Reader, func()) {
	return os.Stdin, func() { os.Stdin.Close() }
}
