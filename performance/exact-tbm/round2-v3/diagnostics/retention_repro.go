// Linux/cgroup-v2 memory regression diagnostic. This is a 256 MiB experiment,
// not a 2 GiB formal reproduction or a performance/E measurement.
// Example: retention-repro -bins base=/abs/base,candidate=/abs/candidate -work-dir /tmp/new-retention-run
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const memoryLimit = 256 << 20
const maxResponse = 2 << 20

var abortLaterVersions bool // Never run another workload while a child remains unreaped.

type version struct{ Name, Binary string }
type response struct {
	Type     string              `json:"type"`
	ID       json.RawMessage     `json:"id"`
	OK       bool                `json:"ok"`
	Affected *int64              `json:"affected_rows"`
	Columns  []string            `json:"columns"`
	Rows     [][]json.RawMessage `json:"rows"`
	Error    struct {
		Code string `json:"code"`
	} `json:"error"`
}
type readResult struct {
	line []byte
	err  error
}
type processExit struct {
	Code   int    `json:"exit_code"`
	Signal string `json:"signal,omitempty"`
	Error  string `json:"error,omitempty"`
}
type process struct {
	cmd          *exec.Cmd
	in           io.WriteCloser
	out          io.ReadCloser
	reads        chan readResult
	stopReads    chan struct{}
	stopReadOnce sync.Once
	done         chan struct{}
	exit         processExit
	cgroup       string
	seq          int64
	timeout      time.Duration
}
type recorder struct {
	f    *os.File
	name string
	err  error
}

func (r *recorder) emit(phase string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["phase"], fields["candidate"], fields["utc"] = phase, r.name, time.Now().UTC().Format(time.RFC3339Nano)
	b, err := json.Marshal(fields)
	if err != nil {
		r.err = err
		return
	}
	b = append(b, '\n')
	if _, err = r.f.Write(b); err != nil && r.err == nil {
		r.err = err
	}
	if err = r.f.Sync(); err != nil && r.err == nil {
		r.err = err
	}
	if _, err = os.Stdout.Write(b); err != nil && r.err == nil {
		r.err = err
	}
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func readNumber(path string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
}
func counters(path string) (map[string]int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := map[string]int64{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			return nil, fmt.Errorf("invalid counter line in %s", path)
		}
		n, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			return nil, err
		}
		m[f[0]] = n
	}
	return m, nil
}
func telemetry(cgroup, dbdir string) map[string]any {
	m := map[string]any{}
	failures := map[string]string{}
	for _, name := range []string{"memory.current", "memory.peak"} {
		n, err := readNumber(filepath.Join(cgroup, name))
		if err != nil {
			failures[name] = err.Error()
		} else {
			m[name] = n
		}
	}
	for _, name := range []string{"memory.stat", "memory.events", "cpu.stat", "cgroup.events"} {
		v, err := counters(filepath.Join(cgroup, name))
		if err != nil {
			failures[name] = err.Error()
		} else {
			m[name] = v
		}
	}
	st, err := os.Stat(filepath.Join(dbdir, "data.log"))
	if err == nil {
		m["log_bytes"] = st.Size()
	} else {
		failures["log_bytes"] = err.Error()
	}
	if len(failures) > 0 {
		m["telemetry_errors"] = failures
	}
	return m
}
func makeCgroup(name string) (string, error) {
	parent := "/sys/fs/cgroup"
	if err := os.WriteFile(filepath.Join(parent, "cgroup.subtree_control"), []byte("+cpu +memory +pids"), 0644); err != nil {
		return "", err
	}
	path := filepath.Join(parent, fmt.Sprintf("seekdb-retention-%d-%s", os.Getpid(), name))
	if err := os.Mkdir(path, 0755); err != nil {
		return "", err
	}
	for key, value := range map[string]string{"cpu.max": "200000 100000", "memory.max": "268435456", "memory.swap.max": "0", "pids.max": "128", "memory.oom.group": "1"} {
		if err := os.WriteFile(filepath.Join(path, key), []byte(value), 0644); err != nil {
			return path, err
		}
		b, err := os.ReadFile(filepath.Join(path, key))
		if err != nil {
			return path, err
		}
		if strings.TrimSpace(string(b)) != value {
			return path, fmt.Errorf("cgroup limit readback mismatch: %s", key)
		}
	}
	return path, nil
}

func lineLimited(rd *bufio.Reader) ([]byte, error) {
	var full []byte
	for {
		b, err := rd.ReadSlice('\n')
		if len(full)+len(b) > maxResponse {
			return nil, fmt.Errorf("response exceeds %d bytes", maxResponse)
		}
		full = append(full, b...)
		if err == nil {
			return full, nil
		}
		if err != bufio.ErrBufferFull {
			return nil, err
		}
	}
}
func launch(binaryPath, dbdir, cgroup, stderrPath string, timeout time.Duration) (*process, error) {
	errfile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	cg, err := os.Open(cgroup)
	if err != nil {
		errfile.Close()
		return nil, err
	}
	cmd := exec.Command(binaryPath, "--data-dir", dbdir, "--sync", "fdatasync", "--mode", "exact")
	for _, entry := range os.Environ() {
		key := strings.SplitN(entry, "=", 2)[0]
		if key == "GOMEMLIMIT" || key == "GOMAXPROCS" || key == "GOGC" || strings.HasPrefix(key, "MVDB_") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "GOMAXPROCS=2", "GOMEMLIMIT=161061273", "GOGC=20", "MVDB_SYNC=fdatasync")
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(cg.Fd()), Setpgid: true}
	cmd.Stderr = errfile
	in, err := cmd.StdinPipe()
	if err != nil {
		cg.Close()
		errfile.Close()
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		cg.Close()
		errfile.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		in.Close()
		out.Close()
		cg.Close()
		errfile.Close()
		return nil, err
	}
	cg.Close()
	p := &process{cmd: cmd, in: in, out: out, reads: make(chan readResult, 8), stopReads: make(chan struct{}), done: make(chan struct{}), cgroup: cgroup, timeout: timeout}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer close(p.reads)
		rd := bufio.NewReaderSize(out, 64<<10)
		for {
			b, err := lineLimited(rd)
			select {
			case p.reads <- readResult{b, err}:
			case <-p.stopReads:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	// Read stdout through EOF before Wait closes its pipe. Stderr goes
	// straight to a file, so it cannot fill a pipe and block the child.
	go func() {
		<-readDone
		err := cmd.Wait()
		p.exit.Code = -1
		if cmd.ProcessState != nil {
			p.exit.Code = cmd.ProcessState.ExitCode()
			if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				p.exit.Signal = status.Signal().String()
			}
		}
		if err != nil {
			p.exit.Error = err.Error()
		}
		errfile.Sync()
		errfile.Close()
		close(p.done)
	}()
	return p, nil
}
func (p *process) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}
func (p *process) kill() {
	p.in.Close()
	p.stopReadOnce.Do(func() { close(p.stopReads) })
	// The path is the freshly created per-version cgroup, not a parent.
	if err := os.WriteFile(filepath.Join(p.cgroup, "cgroup.kill"), []byte("1"), 0644); err != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		_ = p.cmd.Process.Kill()
	}
	// Also release a blocked stdout read/send if a child inherited the pipe
	// or emitted unsolicited responses; Wait must still record the exit.
	p.out.Close()
}
func (p *process) wait() (processExit, bool) {
	select {
	case <-p.done:
		return p.exit, true
	case <-time.After(5 * time.Second):
		return processExit{Code: -1, Error: "process not reaped within 5 seconds"}, false
	}
}
func (p *process) receive(timer *time.Timer) (response, error) {
	select {
	case rr, ok := <-p.reads:
		if !ok {
			return response{}, errors.New("stdout closed")
		}
		if rr.err != nil {
			return response{}, fmt.Errorf("read stdout: %w", rr.err)
		}
		var r response
		if err := json.Unmarshal(rr.line, &r); err != nil {
			return r, fmt.Errorf("invalid response JSON (%d bytes): %w", len(rr.line), err)
		}
		return r, nil
	case <-timer.C:
		p.kill()
		return response{}, errors.New("request/startup timeout; kill requested")
	}
}
func (p *process) ready() error {
	timer := time.NewTimer(p.timeout)
	defer timer.Stop()
	r, err := p.receive(timer)
	if err != nil {
		return err
	}
	if r.Type != "ready" {
		return errors.New("first response is not ready")
	}
	return nil
}
func (p *process) request(op, sql string) (response, int, error) {
	p.seq++
	b, err := json.Marshal(struct {
		ID   int64  `json:"id"`
		Op   string `json:"op"`
		SQL  string `json:"sql,omitempty"`
		Mode string `json:"search_mode"`
	}{p.seq, op, sql, "exact"})
	if err != nil {
		return response{}, 0, err
	}
	b = append(b, '\n')
	timer := time.NewTimer(p.timeout)
	defer timer.Stop()
	written := make(chan error, 1)
	go func() {
		n, e := p.in.Write(b)
		if e == nil && n != len(b) {
			e = io.ErrShortWrite
		}
		written <- e
	}()
	select {
	case err = <-written:
		if err != nil {
			return response{}, len(b), fmt.Errorf("write request: %w", err)
		}
	case <-timer.C:
		p.kill()
		return response{}, len(b), errors.New("request write timeout; kill requested")
	}
	r, err := p.receive(timer)
	if err != nil {
		return r, len(b), err
	}
	var id int64
	if json.Unmarshal(r.ID, &id) != nil || id != p.seq {
		return r, len(b), errors.New("response id mismatch")
	}
	if !r.OK {
		return r, len(b), fmt.Errorf("database rejected request, code=%q", r.Error.Code)
	}
	return r, len(b), nil
}

func makeInsert(first int64, rows int, payload string) string {
	var b strings.Builder
	b.Grow(rows*(len(payload)+40) + 40)
	b.WriteString("INSERT INTO t VALUES ")
	for row := 0; row < rows; row++ {
		if row > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "(%d,'", first+int64(row))
		b.WriteString(payload)
		b.WriteString("','[0.25]')")
	}
	return b.String()
}
func pointCheck(p *process, pk int64) error {
	r, _, err := p.request("execute", fmt.Sprintf("SELECT id,payload,v FROM t WHERE id=%d", pk))
	if err != nil {
		return err
	}
	if len(r.Rows) != 1 || len(r.Rows[0]) != 3 {
		return errors.New("point row shape mismatch")
	}
	var id int64
	var payload string
	var vector []float64
	if json.Unmarshal(r.Rows[0][0], &id) != nil || json.Unmarshal(r.Rows[0][1], &payload) != nil || json.Unmarshal(r.Rows[0][2], &vector) != nil {
		return errors.New("point row decoding failed")
	}
	if id != pk || len(payload) != 60000 || strings.Count(payload, "x") != 60000 || len(vector) != 1 || vector[0] != 0.25 {
		return errors.New("point row content mismatch")
	}
	return nil
}
func verifyRows(p *process, acked, attempted int64) (map[string]any, error) {
	m := map[string]any{"acked_rows": acked, "attempted_rows": attempted, "count_method": "SELECT id ORDER BY id; count independently in external driver; COUNT aggregate unsupported"}
	r, _, err := p.request("execute", "SELECT id FROM t ORDER BY id")
	if err != nil {
		return m, err
	}
	count := int64(len(r.Rows))
	m["observed_rows"] = count
	m["extra_unacknowledged_rows"] = count - acked
	if count != acked && count != attempted {
		return m, fmt.Errorf("row count %d is neither acknowledged %d nor full attempted %d", count, acked, attempted)
	}
	h := sha256.New()
	var encoded [8]byte
	for i, row := range r.Rows {
		var id int64
		if len(row) != 1 || json.Unmarshal(row[0], &id) != nil || id != int64(i+1) {
			return m, fmt.Errorf("primary key census mismatch at row %d", i)
		}
		binary.LittleEndian.PutUint64(encoded[:], uint64(id))
		h.Write(encoded[:])
	}
	m["primary_key_census_sha256"] = hex.EncodeToString(h.Sum(nil))
	if acked > 0 {
		if err := pointCheck(p, acked); err != nil {
			return m, fmt.Errorf("last acknowledged point: %w", err)
		}
		m["last_acknowledged_pk_checked"] = acked
	}
	if count > acked {
		if err := pointCheck(p, count); err != nil {
			return m, fmt.Errorf("last unacknowledged point: %w", err)
		}
		m["last_unacknowledged_pk_checked"] = count
	}
	m["validation_passed"] = true
	return m, nil
}
func closeProcess(p *process, rec *recorder, stage, cgroup, dbdir string) bool {
	graceful := false
	if p.alive() {
		_, _, err := p.request("shutdown", "")
		if err == nil {
			graceful = true
		} else {
			rec.emit(stage+"_shutdown_error", map[string]any{"error": err.Error(), "telemetry": telemetry(cgroup, dbdir)})
			p.kill()
		}
	}
	exit, reaped := p.wait()
	if !reaped {
		p.kill()
		exit, reaped = p.wait()
	}
	if !reaped {
		abortLaterVersions = true
	}
	p.in.Close()
	rec.emit(stage+"_exit", map[string]any{"graceful_shutdown_acknowledged": graceful, "reaped": reaped, "process": exit, "telemetry": telemetry(cgroup, dbdir)})
	return reaped && graceful && exit.Code == 0
}

func runVersion(v version, work string, timeout time.Duration, startRows int) (success bool) {
	expectedBatches := (startRows-16)/2 + 1
	expectedRows := int64(startRows+16) * int64(expectedBatches) / 2
	dir := filepath.Join(work, v.Name)
	if err := os.Mkdir(dir, 0755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return false
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return false
	}
	defer f.Close()
	rec := &recorder{f: f, name: v.Name}
	dbdir := filepath.Join(dir, "db")
	if err = os.Mkdir(dbdir, 0755); err != nil {
		rec.emit("setup_failure", map[string]any{"error": err.Error()})
		return false
	}
	sha, err := hashFile(v.Binary)
	if err != nil {
		rec.emit("setup_failure", map[string]any{"error": err.Error()})
		return false
	}
	cgroup, err := makeCgroup(v.Name)
	if err != nil {
		rec.emit("setup_failure", map[string]any{"error": err.Error(), "cgroup": cgroup})
		return false
	}
	for _, name := range []string{"memory.current", "memory.peak"} {
		if _, err = readNumber(filepath.Join(cgroup, name)); err != nil {
			rec.emit("setup_failure", map[string]any{"error": err.Error(), "cgroup": cgroup})
			return false
		}
	}
	for _, name := range []string{"memory.stat", "memory.events", "cpu.stat", "cgroup.events"} {
		if _, err = counters(filepath.Join(cgroup, name)); err != nil {
			rec.emit("setup_failure", map[string]any{"error": err.Error(), "cgroup": cgroup})
			return false
		}
	}
	// Preserve cgroup and data paths for postmortem inspection; do not delete.
	rec.emit("config", map[string]any{"binary": v.Binary, "binary_sha256": sha, "data_dir": dbdir, "cgroup": cgroup, "memory_limit_bytes": memoryLimit, "cpu_max": "200000 100000", "memory_swap_max": 0, "pids_max": 128, "memory_oom_group": 1, "database_env": []string{"GOMAXPROCS=2", "GOMEMLIMIT=161061273", "GOGC=20", "MVDB_SYNC=fdatasync"}, "sync": "fdatasync", "search_mode": "exact", "request_timeout_s": timeout.Seconds(), "max_response_bytes": maxResponse, "batch_rows": "start_rows through 16 inclusive, decrement by 2", "start_rows": startRows, "end_rows": 16, "row_step": -2, "expected_batches": expectedBatches, "expected_rows": expectedRows, "payload_bytes_per_row": 60000, "vector": [1]float64{0.25}, "not_2gib_formal_reproduction": true, "no_performance_E": true, "external_generator_and_oracle": true, "memory_peak_scope": "whole_version_cumulative_across_initial_and_recovery", "data_and_cgroup_preserved": true, "input_padding_bytes": 0})
	acked, attempted := int64(0), int64(0)
	batchesAcked := 0
	initialOK := true
	tableCreated := false
	p, err := launch(v.Binary, dbdir, cgroup, filepath.Join(dir, "initial.stderr.txt"), timeout)
	if err != nil {
		initialOK = false
		rec.emit("initial_launch_failure", map[string]any{"error": err.Error(), "acked_rows": acked, "telemetry": telemetry(cgroup, dbdir)})
	} else {
		err = p.ready()
		if err != nil {
			initialOK = false
			rec.emit("initial_startup_failure", map[string]any{"error": err.Error(), "acked_rows": acked, "telemetry": telemetry(cgroup, dbdir)})
			p.kill()
		} else {
			rec.emit("initial_ready", map[string]any{"pid": p.cmd.Process.Pid, "telemetry": telemetry(cgroup, dbdir)})
			_, _, err = p.request("execute", "CREATE TABLE t(id BIGINT PRIMARY KEY,payload VARCHAR(60000),v VECTOR(1))")
			if err != nil {
				initialOK = false
				rec.emit("create_failure", map[string]any{"error": err.Error(), "telemetry": telemetry(cgroup, dbdir)})
			} else {
				tableCreated = true
			}
			payload := strings.Repeat("x", 60000)
			for rows := startRows; tableCreated && rows >= 16; rows -= 2 {
				attempted = acked + int64(rows)
				sql := makeInsert(acked+1, rows, payload)
				r, requestBytes, e := p.request("execute", sql)
				if e == nil && (r.Affected == nil || *r.Affected != int64(rows)) {
					e = errors.New("affected_rows mismatch")
				}
				fields := map[string]any{"batch_rows": rows, "attempted_rows": attempted, "request_bytes": requestBytes, "request_exceeds_1mib": requestBytes > 1<<20, "raw_newline_count_in_json": 1, "acked_rows_before": acked, "telemetry": telemetry(cgroup, dbdir)}
				if e != nil {
					initialOK = false
					fields["error"] = e.Error()
					fields["acked_rows"] = acked
					rec.emit("insert_failure", fields)
					break
				}
				acked = attempted
				batchesAcked++
				fields["acked_rows"] = acked
				fields["batches_acknowledged"] = batchesAcked
				rec.emit("insert_acknowledged", fields)
			}
			if tableCreated && p.alive() {
				proof, e := verifyRows(p, acked, attempted)
				proof["telemetry"] = telemetry(cgroup, dbdir)
				if e != nil {
					initialOK = false
					proof["validation_passed"] = false
					proof["error"] = e.Error()
				}
				rec.emit("initial_live_validation", proof)
			} else {
				initialOK = false
				rec.emit("initial_live_validation_skipped", map[string]any{"reason": "process exited or table creation failed", "acked_rows": acked, "telemetry": telemetry(cgroup, dbdir)})
			}
		}
		if !closeProcess(p, rec, "initial", cgroup, dbdir) {
			initialOK = false
		}
	}
	rec.emit("initial_summary", map[string]any{"completed_all_inserts": initialOK && batchesAcked == expectedBatches && acked == expectedRows, "acked_rows": acked, "attempted_rows": attempted, "batches_acknowledged": batchesAcked, "start_rows": startRows, "expected_batches": expectedBatches, "expected_rows": expectedRows, "telemetry": telemetry(cgroup, dbdir)})
	if abortLaterVersions {
		rec.emit("version_complete", map[string]any{"diagnostic_completed": false, "full_workflow_passed": false, "recovery_skipped": "initial process not reaped; refusing concurrent recovery or next version", "acked_rows": acked, "attempted_rows": attempted, "telemetry": telemetry(cgroup, dbdir)})
		return false
	}
	// Recovery always uses the exact same private log, even after OOM or an
	// ambiguous response. An unacknowledged final batch may fully commit.
	recoveryOK := false
	p, err = launch(v.Binary, dbdir, cgroup, filepath.Join(dir, "recovery.stderr.txt"), timeout)
	if err != nil {
		rec.emit("recovery_launch_failure", map[string]any{"error": err.Error(), "acked_rows": acked, "telemetry": telemetry(cgroup, dbdir)})
	} else {
		if err = p.ready(); err != nil {
			rec.emit("recovery_startup_failure", map[string]any{"error": err.Error(), "acked_rows": acked, "telemetry": telemetry(cgroup, dbdir)})
			p.kill()
		} else {
			rec.emit("recovery_ready", map[string]any{"pid": p.cmd.Process.Pid, "telemetry": telemetry(cgroup, dbdir)})
			proof, e := verifyRows(p, acked, attempted)
			proof["telemetry"] = telemetry(cgroup, dbdir)
			if e != nil {
				proof["validation_passed"] = false
				proof["error"] = e.Error()
			} else {
				recoveryOK = true
			}
			rec.emit("recovery_validation", proof)
		}
		if !closeProcess(p, rec, "recovery", cgroup, dbdir) {
			recoveryOK = false
		}
	}
	finalSHA, hashErr := hashFile(v.Binary)
	unchanged := hashErr == nil && finalSHA == sha
	success = initialOK && batchesAcked == expectedBatches && acked == expectedRows && recoveryOK && unchanged && rec.err == nil
	fields := map[string]any{"diagnostic_completed": !abortLaterVersions, "full_workflow_passed": success, "all_inserts_passed": initialOK && batchesAcked == expectedBatches && acked == expectedRows, "recovery_validation_passed": recoveryOK, "acked_rows": acked, "attempted_rows": attempted, "batches_acknowledged": batchesAcked, "start_rows": startRows, "expected_batches": expectedBatches, "expected_rows": expectedRows, "binary_unchanged": unchanged, "binary_sha256_after": finalSHA, "telemetry": telemetry(cgroup, dbdir), "not_2gib_formal_reproduction": true, "no_performance_E": true}
	if hashErr != nil {
		fields["binary_hash_error"] = hashErr.Error()
	}
	if rec.err != nil {
		fields["artifact_error"] = rec.err.Error()
	}
	rec.emit("version_complete", fields)
	return success && rec.err == nil
}

func main() {
	bins := flag.String("bins", "", "exactly two entries: name=/absolute/binary,name=/absolute/binary; run serially in this order")
	work := flag.String("work-dir", "", "new output directory; data logs and cgroups are preserved")
	timeout := flag.Duration("timeout", 30*time.Second, "deadline for startup or each complete request including pipe write")
	startRows := flag.Int("start-rows", 128, "even initial batch size from 16 through 512; decrement by 2 through 16")
	flag.Parse()
	fail := func(err error) {
		b, _ := json.Marshal(map[string]any{"phase": "driver_failure", "error": err.Error(), "not_2gib_formal_reproduction": true})
		fmt.Println(string(b))
		os.Exit(2)
	}
	if *timeout <= 0 || *work == "" {
		fail(errors.New("positive timeout and a new work-dir are required"))
	}
	if *startRows < 16 || *startRows > 512 || *startRows%2 != 0 {
		fail(errors.New("start-rows must be an even integer from 16 through 512"))
	}
	parts := strings.Split(*bins, ",")
	if len(parts) != 2 {
		fail(errors.New("exactly two bins are required"))
	}
	names := map[string]bool{}
	versions := make([]version, 0, 2)
	validName := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	for _, part := range parts {
		x := strings.SplitN(part, "=", 2)
		if len(x) != 2 || !validName.MatchString(x[0]) || names[x[0]] {
			fail(errors.New("invalid or duplicate binary label"))
		}
		path, err := filepath.Abs(x[1])
		if err != nil {
			fail(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			fail(err)
		}
		if !info.Mode().IsRegular() {
			fail(errors.New("binary must be a regular file"))
		}
		names[x[0]] = true
		versions = append(versions, version{x[0], path})
	}
	path, err := filepath.Abs(*work)
	if err != nil {
		fail(err)
	}
	if err = os.Mkdir(path, 0755); err != nil {
		fail(fmt.Errorf("work-dir must be new with an existing parent: %w", err))
	}
	all := true
	for _, v := range versions {
		if !runVersion(v, path, *timeout, *startRows) {
			all = false
		}
		if abortLaterVersions {
			break
		}
	}
	expectedBatches := (*startRows-16)/2 + 1
	expectedRows := int64(*startRows+16) * int64(expectedBatches) / 2
	b, _ := json.Marshal(map[string]any{"phase": "driver_complete", "work_dir": path, "both_full_workflows_passed": all, "start_rows": *startRows, "expected_batches": expectedBatches, "expected_rows": expectedRows, "aborted_due_to_unreaped_child": abortLaterVersions, "not_2gib_formal_reproduction": true, "no_performance_E": true, "data_preserved": true})
	fmt.Println(string(b))
	if !all {
		os.Exit(1)
	}
}
