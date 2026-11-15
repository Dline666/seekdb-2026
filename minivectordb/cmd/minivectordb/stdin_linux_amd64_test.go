//go:build linux && amd64

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// readLikeMain consumes lines from rd exactly the way the request loop in
// main.go does, including the over-1 MiB line fallback.
func readLikeMain(rd *bufio.Reader) ([]byte, error) {
	b, err := rd.ReadSlice('\n')
	if err == bufio.ErrBufferFull {
		head := append([]byte(nil), b...)
		var rest []byte
		rest, err = rd.ReadBytes('\n')
		b = append(head, rest...)
	}
	return b, err
}

// TestPollReaderMatchesBlocking drives a pollReader through os.Pipe while a
// writer delivers a random byte stream in random chunks with sleeps both
// inside and beyond the spin window, and checks that every byte arrives in
// order and the stream ends in io.EOF.
func TestPollReaderMatchesBlocking(t *testing.T) {
	sleeps := []time.Duration{0, 20 * time.Microsecond, 200 * time.Microsecond, 2 * time.Millisecond, 5 * time.Millisecond}
	for _, window := range []time.Duration{time.Microsecond, 300 * time.Microsecond, time.Millisecond} {
		t.Run(window.String(), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(window) + 7))
			for i := 0; i < 100; i++ {
				// Streams up to 2 MiB, biased small: most scripts are a few
				// chunks, a few are large.
				total := rng.Intn(64 << 10)
				if rng.Intn(10) == 0 {
					total = (64 << 10) + rng.Intn((2<<20)-(64<<10))
				}
				data := make([]byte, total)
				rng.Read(data)

				// Precompute the chunk/sleep plan so the writer goroutine
				// touches no shared state.
				type step struct {
					n int
					d time.Duration
				}
				var plan []step
				for pos := 0; pos < total; {
					n := 1 + rng.Intn(256<<10)
					if n > total-pos {
						n = total - pos
					}
					plan = append(plan, step{n: n, d: sleeps[rng.Intn(len(sleeps))]})
					pos += n
				}

				pr, pw, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				go func() {
					pos := 0
					for _, s := range plan {
						for off := 0; off < s.n; {
							m, err := pw.Write(data[pos+off : pos+s.n])
							if err != nil {
								return
							}
							off += m
						}
						pos += s.n
						if s.d > 0 {
							time.Sleep(s.d)
						}
					}
					pw.Close()
				}()

				rd := bufio.NewReaderSize(&pollReader{fd: int(pr.Fd()), window: window}, 1<<20)
				var got bytes.Buffer
				got.Grow(total)
				for {
					b, err := readLikeMain(rd)
					got.Write(b)
					if err != nil {
						if err != io.EOF {
							t.Fatalf("script %d: read error %v", i, err)
						}
						break
					}
				}
				pr.Close()
				if !bytes.Equal(got.Bytes(), data) {
					t.Fatalf("script %d: got %d bytes, want %d (equal=%v)", i, got.Len(), total, bytes.Equal(got.Bytes(), data))
				}
			}
		})
	}
}

// TestPollReaderInterrupt checks that closing the reader interrupts a Read
// both while it is still spinning and after it has gone to sleep.
func TestPollReaderInterrupt(t *testing.T) {
	mustErrClosed := func(t *testing.T, r *pollReader, closeAfter time.Duration) {
		t.Helper()
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer pr.Close()
		defer pw.Close()
		r.fd = int(pr.Fd())
		done := make(chan error, 1)
		go func() {
			_, err := r.Read(make([]byte, 64))
			done <- err
		}()
		time.Sleep(closeAfter)
		r.closed.Store(true)
		select {
		case err := <-done:
			if !errors.Is(err, os.ErrClosed) {
				t.Fatalf("Read returned %v, want os.ErrClosed", err)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("Read did not return os.ErrClosed within 500 ms")
		}
	}
	t.Run("during spin", func(t *testing.T) {
		// A 5 s window keeps the reader in the spin phase; the close must
		// still end the Read promptly.
		mustErrClosed(t, &pollReader{window: 5 * time.Second}, 10*time.Millisecond)
	})
	t.Run("while asleep", func(t *testing.T) {
		// A 1 us window sends the reader to the timed poll almost at once;
		// the close lands while it is parked there.
		mustErrClosed(t, &pollReader{window: time.Microsecond}, 300*time.Millisecond)
	})
}

// TestPollReaderEOFAndZero covers immediate EOF, a zero-length read, and a
// data read followed by EOF.
func TestPollReaderEOFAndZero(t *testing.T) {
	t.Run("immediate EOF", func(t *testing.T) {
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		pw.Close()
		defer pr.Close()
		// A huge window must not matter: hangup is instantly ready.
		r := &pollReader{fd: int(pr.Fd()), window: time.Hour}
		n, err := r.Read(make([]byte, 8))
		if n != 0 || err != io.EOF {
			t.Fatalf("Read = (%d, %v), want (0, io.EOF)", n, err)
		}
	})
	t.Run("zero length read", func(t *testing.T) {
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer pr.Close()
		defer pw.Close()
		r := &pollReader{fd: int(pr.Fd()), window: time.Microsecond}
		if n, err := r.Read(nil); n != 0 || err != nil {
			t.Fatalf("Read(nil) = (%d, %v), want (0, nil)", n, err)
		}
	})
	t.Run("data then EOF", func(t *testing.T) {
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer pr.Close()
		if _, err := pw.Write([]byte("hello")); err != nil {
			t.Fatal(err)
		}
		pw.Close()
		r := &pollReader{fd: int(pr.Fd()), window: time.Microsecond}
		buf := make([]byte, 16)
		n, err := r.Read(buf)
		if n != 5 || err != nil || string(buf[:n]) != "hello" {
			t.Fatalf("Read = (%d, %v) %q, want (5, nil) \"hello\"", n, err, buf[:n])
		}
		if n, err := r.Read(buf); n != 0 || err != io.EOF {
			t.Fatalf("Read after data = (%d, %v), want (0, io.EOF)", n, err)
		}
	})
}

// runSession drives the real binary through the request script on pipes and
// returns every response line plus the process exit state.
type sessionResult struct {
	responses []string
	trailing  string // stdout bytes after the last expected response
	exitCode  int
}

func runSession(t *testing.T, bin, spinUS string, requests [][]byte, end string) sessionResult {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command(bin, "--data-dir", dir, "--mode", "exact", "--sync", "none")
	cmd.Env = append(os.Environ(), "MVDB_SPIN_FORCE=1", "MVDB_SPIN_US="+spinUS)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	killer := time.AfterFunc(120*time.Second, func() { cmd.Process.Kill() })
	defer killer.Stop()

	rd := bufio.NewReader(stdout)
	readLine := func(ctx string) string {
		t.Helper()
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("%s: reading response: %v (stderr: %s)", ctx, err, stderr.String())
		}
		return line
	}

	res := sessionResult{responses: []string{readLine("ready")}}
	rng := rand.New(rand.NewSource(99))
	for i, req := range requests {
		// Deliver each request in random-sized pieces with pauses like a
		// pipe relay chain, then read exactly one response.
		for off := 0; off < len(req); {
			n := 1 + rng.Intn(64<<10)
			if n > len(req)-off {
				n = len(req) - off
			}
			if _, err := stdin.Write(req[off : off+n]); err != nil {
				t.Fatalf("request %d: write: %v", i, err)
			}
			off += n
			time.Sleep(time.Duration(rng.Intn(2000)) * time.Microsecond)
		}
		res.responses = append(res.responses, readLine(fmt.Sprintf("request %d", i)))
	}

	switch end {
	case "shutdown":
		if _, err := stdin.Write([]byte(`{"id":9000,"op":"shutdown"}` + "\n")); err != nil {
			t.Fatal(err)
		}
		res.responses = append(res.responses, readLine("shutdown"))
	case "sigterm":
		time.Sleep(50 * time.Millisecond) // let the process go idle
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
	case "eof":
		stdin.Close()
	}
	// Drain stdout to EOF (the process closes it on exit) before reaping.
	rest, _ := io.ReadAll(rd)
	err = cmd.Wait()
	res.trailing = stderr.String()
	if len(rest) > 0 {
		t.Fatalf("unexpected trailing stdout: %q", rest)
	}
	if err == nil {
		res.exitCode = 0
	} else if ee, ok := err.(*exec.ExitError); ok {
		res.exitCode = ee.ExitCode()
	} else {
		t.Fatalf("wait: %v", err)
	}
	return res
}

// TestBinaryPollingEndToEnd runs the real binary with forced polling at two
// window sizes and compares its responses, byte for byte, against the same
// binary with polling switched off, for all three ways a session can end.
func TestBinaryPollingEndToEnd(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "minivectordb")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	vec := func(n int, base float64) string {
		var b strings.Builder
		b.WriteByte('[')
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "%v", base+float64(i)/10)
			if i+1 < n {
				b.WriteByte(',')
			}
		}
		b.WriteByte(']')
		return b.String()
	}
	var requests [][]byte
	id := 0
	req := func(sql string) {
		id++
		q, err := json.Marshal(sql)
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, []byte(fmt.Sprintf("{\"id\":%d,\"op\":\"execute\",\"sql\":%s}\n", id, q)))
	}
	req("CREATE TABLE docs (id BIGINT PRIMARY KEY, embedding VECTOR(8))")
	for i := 1; i <= 50; i++ {
		req(fmt.Sprintf("INSERT INTO docs VALUES (%d, '%s')", i, vec(8, float64(i))))
	}
	// A ~300 KB multi-row INSERT.
	var big strings.Builder
	big.WriteString("INSERT INTO docs VALUES ")
	for i := 0; i < 2000; i++ {
		if i > 0 {
			big.WriteByte(',')
		}
		fmt.Fprintf(&big, "(%d,'%s')", 1000+i, vec(8, 1.1111111111111111))
	}
	req(big.String())
	for i := 0; i < 200; i++ {
		req(fmt.Sprintf("SELECT * FROM docs WHERE id = %d", 1+i*9))
	}
	if len(big.String()) < 250<<10 {
		t.Fatalf("big INSERT only %d bytes", len(big.String()))
	}

	// Compare responses and clean exits against polling switched off for
	// the two ends where the baseline itself terminates. SIGTERM is checked
	// separately below.
	var baseResponses []string
	for _, end := range []string{"shutdown", "eof"} {
		base := runSession(t, bin, "0", requests, end)
		baseResponses = base.responses
		if base.exitCode != 0 {
			t.Fatalf("%s baseline: exit %d, want 0 (stderr: %s)", end, base.exitCode, base.trailing)
		}
		for _, spin := range []string{"1", "1000"} {
			got := runSession(t, bin, spin, requests, end)
			if got.exitCode != 0 {
				t.Fatalf("%s spin=%s: exit %d, want 0 (stderr: %s)", end, spin, got.exitCode, got.trailing)
			}
			if len(got.responses) != len(base.responses) {
				t.Fatalf("%s spin=%s: %d responses, want %d", end, spin, len(got.responses), len(base.responses))
			}
			for i := range base.responses {
				if got.responses[i] != base.responses[i] {
					t.Fatalf("%s spin=%s: response %d differs:\n got %q\nwant %q", end, spin, i, got.responses[i], base.responses[i])
				}
			}
		}
	}
	// SIGTERM while idle must exit 0 with no further output. This is only
	// asserted for the polling runs: the blocking baseline relies on
	// os.Stdin.Close() interrupting a pending read, which a FIFO read()
	// survives on Linux (the call holds the open file description), so the
	// baseline may stay parked in read until stdin's write end closes.
	for _, spin := range []string{"1", "1000"} {
		got := runSession(t, bin, spin, requests, "sigterm")
		if got.exitCode != 0 {
			t.Fatalf("sigterm spin=%s: exit %d, want 0 (stderr: %s)", spin, got.exitCode, got.trailing)
		}
		for i := range baseResponses {
			if got.responses[i] != baseResponses[i] {
				t.Fatalf("sigterm spin=%s: response %d differs:\n got %q\nwant %q", spin, i, got.responses[i], baseResponses[i])
			}
		}
	}
}
