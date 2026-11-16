//go:build linux && amd64

package main

import (
	"io"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// The judge's requests reach the container through a chain of pipe hops and
// each request waits for our previous response, so every round trip pays one
// wake-up from a blocking read. When the box has more than one CPU we can
// afford to poll stdin for a short window before sleeping: a pipe becomes
// readable as soon as a write lands, which skips the scheduler's wake-up
// path. The fd stays in blocking mode the whole time; nothing else in the
// process notices the difference.
type pollReader struct {
	fd     int
	window time.Duration
	closed atomic.Bool
}

// pollFD mirrors struct pollfd on linux/amd64: 8 bytes, no padding.
type pollFD struct {
	fd      int32
	events  int16
	revents int16
}

// pollIn is POLLIN. Any non-zero revents (POLLIN, POLLHUP, POLLERR,
// POLLNVAL) ends the wait; read() then reports the data or the error.
const pollIn = 0x0001

// newStdin returns the reader for request lines and a function that makes a
// pending or future Read fail promptly; the signal watcher calls it. The
// polling reader is used only when stdin is a pipe, MVDB_SPIN_US resolves to
// a positive microsecond window and the box has at least two CPUs (or
// MVDB_SPIN_FORCE overrides the CPU check); otherwise it is plain os.Stdin.
func newStdin(logf func(string, ...any)) (io.Reader, func()) {
	var st syscall.Stat_t
	isFifo := syscall.Fstat(0, &st) == nil && st.Mode&syscall.S_IFMT == syscall.S_IFIFO
	if w, ok := pollEnabled(isFifo, runtime.NumCPU(), runtime.GOMAXPROCS(0),
		os.Getenv("MVDB_SPIN_US"), os.Getenv("MVDB_SPIN_FORCE")); ok {
		r := &pollReader{fd: 0, window: w}
		logf("stdin: polling up to %d us before sleeping", w.Microseconds())
		return r, func() { r.closed.Store(true) }
	}
	return os.Stdin, func() { os.Stdin.Close() }
}

// wait polls the fd until it is readable, an error must be surfaced by the
// following read, or the reader is closed. It spin-polls for up to window
// first; if nothing arrives it parks on a timed 100 ms poll, waking often
// enough to notice a close.
func (r *pollReader) wait() error {
	pfd := pollFD{fd: int32(r.fd), events: pollIn}
	var start time.Time
	misses := 0
	for {
		if r.closed.Load() {
			return os.ErrClosed
		}
		pfd.revents = 0
		// Timeout 0, so RawSyscall never deschedules the goroutine.
		_, _, errno := syscall.RawSyscall(syscall.SYS_POLL, uintptr(unsafe.Pointer(&pfd)), 1, 0)
		switch {
		case errno == syscall.EINTR:
			continue
		case errno != 0, pfd.revents != 0:
			// Ready, or a poll error we let read() report.
			return nil
		}
		misses++
		if misses == 1 {
			start = time.Now()
		}
		if misses%8 == 0 && time.Since(start) >= r.window {
			break
		}
		syscall.RawSyscall(syscall.SYS_SCHED_YIELD, 0, 0, 0)
	}
	for {
		if r.closed.Load() {
			return os.ErrClosed
		}
		pfd.revents = 0
		// Blocking timeout: the ordinary Syscall form lets the scheduler
		// know this goroutine is parked in the kernel.
		_, _, errno := syscall.Syscall(syscall.SYS_POLL, uintptr(unsafe.Pointer(&pfd)), 1, 100)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 || pfd.revents != 0 {
			return nil
		}
	}
}

// Read implements io.Reader: wait for the fd to become readable (spinning
// first, then sleeping), then do the real blocking read.
func (r *pollReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := r.wait(); err != nil {
		return 0, err
	}
	for {
		n, err := syscall.Read(r.fd, p)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, io.EOF
		}
		return n, nil
	}
}
