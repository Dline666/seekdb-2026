package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

// TestReadRequestLineLongLine pins the reading of a request line that is
// longer than the reader buffer.
//
// ReadSlice returns ErrBufferFull and has already consumed the bytes it
// returned, so a naive fallback to ReadBytes keeps only the tail of the line.
// The first megabyte would be dropped: a request that was trimmed to nothing
// was answered with no response at all, and a request whose tail was a JSON
// fragment was answered with invalid_request. The protocol declares no limit
// on the request size, so the whole line has to come back.
func TestReadRequestLineLongLine(t *testing.T) {
	const bufSize = 1 << 20
	for _, n := range []int{
		bufSize - 2, bufSize - 1, bufSize, bufSize + 1,
		bufSize + 4096, 3*bufSize + 17,
	} {
		body := strings.Repeat("x", n)
		line := "{\"id\":\"1\",\"op\":\"execute\",\"sql\":\"INSERT INTO t VALUES (" + body + ")\"}\n"
		rd := bufio.NewReaderSize(bytes.NewReader([]byte(line)), bufSize)
		got, err := readRequestLine(rd)
		if err != nil {
			t.Fatalf("size %d: read error %v", n, err)
		}
		if string(got) != line {
			t.Fatalf("size %d: got %d bytes, want %d", n, len(got), len(line))
		}
	}
}

// TestReadRequestLineShortLine keeps the common path intact.
func TestReadRequestLineShortLine(t *testing.T) {
	rd := bufio.NewReaderSize(strings.NewReader("first\nsecond\n"), 1<<20)
	got, err := readRequestLine(rd)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first\n" {
		t.Fatalf("got %q", got)
	}
	got, err = readRequestLine(rd)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second\n" {
		t.Fatalf("got %q", got)
	}
}
