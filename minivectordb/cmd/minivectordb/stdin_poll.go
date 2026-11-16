package main

import (
	"strconv"
	"time"
)

// Polling is opt-in: a direct pipe's wake-up savings need not survive a
// container relay or competing CPU load. Keep the default blocking reader.
const defaultSpinUS = 0

// pollEnabled decides whether stdin may be polled briefly before a blocking
// read, and returns the spin window. Polling is enabled only when all hold:
//
//   - spinUS parses as a positive microsecond count; an empty or unparsable
//     value means the default window, while a value <= 0 switches polling off
//   - fd 0 is a FIFO: the judge talks to the process through pipes, and on a
//     terminal or regular file a spin only burns CPU
//   - the process has at least two configured CPUs (NumCPU >= 2 and
//     GOMAXPROCS >= 2), unless force is non-empty
//
// On a single-CPU container a spinning reader competes with the relay
// processes of the pipe chain for the same CPU and slows the round trip
// down, so polling switches itself off there.
func pollEnabled(isFifo bool, ncpu, procs int, spinUS, force string) (time.Duration, bool) {
	us, err := strconv.ParseInt(spinUS, 10, 64)
	if err != nil {
		us = defaultSpinUS
	}
	if us <= 0 || us > int64((1<<63-1)/time.Microsecond) || !isFifo {
		return 0, false
	}
	if force == "" && (ncpu < 2 || procs < 2) {
		return 0, false
	}
	return time.Duration(us) * time.Microsecond, true
}
