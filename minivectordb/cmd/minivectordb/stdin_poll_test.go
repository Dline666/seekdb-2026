package main

import (
	"testing"
	"time"
)

// The enable decision is a pure function so it can be exercised on any OS;
// the syscall side (Fstat, poll) is tested on Linux only.
func TestNewStdinGuards(t *testing.T) {
	cases := []struct {
		name          string
		isFifo        bool
		ncpu, procs   int
		spinUS, force string
		want          time.Duration
		wantOn        bool
	}{
		{"pipe default off", true, 4, 4, "", "", 0, false},
		{"pipe explicit window", true, 4, 4, "500", "", 500 * time.Microsecond, true},
		{"pipe tiny window", true, 2, 2, "1", "", time.Microsecond, true},
		{"not a fifo", false, 4, 4, "", "", 0, false},
		{"window zero", true, 4, 4, "0", "", 0, false},
		{"window negative", true, 4, 4, "-5", "", 0, false},
		{"window zero wins over force", true, 4, 4, "0", "1", 0, false},
		{"unparsable means default off", true, 4, 4, "soon", "", 0, false},
		{"empty means default off", true, 4, 4, "", "", 0, false},
		{"overflow disabled", true, 4, 4, "9223372036854775807", "", 0, false},
		{"force still needs explicit window", true, 4, 4, "", "1", 0, false},
		{"unparsable still off on one cpu", true, 1, 1, "soon", "", 0, false},
		{"single ncpu", true, 1, 2, "1000", "", 0, false},
		{"single procs", true, 2, 1, "1000", "", 0, false},
		{"force overrides cpu check", true, 1, 1, "1000", "1", time.Millisecond, true},
		{"force needs fifo too", false, 1, 1, "1000", "1", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, on := pollEnabled(c.isFifo, c.ncpu, c.procs, c.spinUS, c.force)
			if on != c.wantOn || (on && w != c.want) {
				t.Fatalf("pollEnabled(%v,%d,%d,%q,%q) = (%v,%v), want (%v,%v)",
					c.isFifo, c.ncpu, c.procs, c.spinUS, c.force, w, on, c.want, c.wantOn)
			}
		})
	}
}
