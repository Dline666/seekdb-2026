// Linux-only ROUND7 indexed-update comparison, forked from the large driver.
// Only the database is confined to 2 CPUs / 2 GiB / no swap.
// The streaming independent oracle runs before measured processes start.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func emit(v any) { b, err := json.Marshal(v); must(err); fmt.Println(string(b)) }
func number(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}
func fields(path string) map[string]int64 {
	b, _ := os.ReadFile(path)
	r := map[string]int64{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			n, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil {
				continue
			}
			if len(f) > 2 && f[2] == "kB" {
				n *= 1024
			}
			r[strings.TrimSuffix(f[0], ":")] = n
		}
	}
	return r
}
func ioBytes(cg, device string) (int64, int64) {
	b, err := os.ReadFile(cg + "/io.stat")
	must(err)
	var r, w int64
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != device {
			continue
		}
		for _, s := range f[1:] {
			kv := strings.SplitN(s, "=", 2)
			if len(kv) != 2 {
				continue
			}
			n, _ := strconv.ParseInt(kv[1], 10, 64)
			if kv[0] == "rbytes" {
				r = n
			}
			if kv[0] == "wbytes" {
				w = n
			}
		}
	}
	return r, w
}

func requireDeviceIO(cg, device string) {
	b, err := os.ReadFile(cg + "/io.stat")
	must(err)
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && f[0] == device {
			return
		}
	}
	panic("missing data-device io.stat after durable writes: " + device + " " + string(b))
}

type sample struct {
	Current int64            `json:"memory_current"`
	Stat    map[string]int64 `json:"memory_stat"`
	Proc    map[string]int64 `json:"proc_status"`
	Smaps   map[string]int64 `json:"smaps_rollup"`
}
type meter struct {
	c                        *child
	start                    time.Time
	r, w                     int64
	peakFD                   *os.File
	reset                    bool
	startup                  bool
	done                     chan struct{}
	wg                       sync.WaitGroup
	mu                       sync.Mutex
	peak                     sample
	maxRSS, maxAnon, maxFile int64
}

func (m *meter) sample() {
	s := sample{Current: number(m.c.cg + "/memory.current"), Stat: fields(m.c.cg + "/memory.stat"), Proc: fields(fmt.Sprintf("/proc/%d/status", m.c.cmd.Process.Pid)), Smaps: fields(fmt.Sprintf("/proc/%d/smaps_rollup", m.c.cmd.Process.Pid))}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.Current > m.peak.Current {
		m.peak = s
	}
	m.maxRSS = max(m.maxRSS, s.Proc["VmRSS"])
	m.maxAnon = max(m.maxAnon, s.Stat["anon"])
	m.maxFile = max(m.maxFile, s.Stat["file"])
}
func resetPeak(cg string) (*os.File, bool) {
	f, err := os.OpenFile(cg+"/memory.peak", os.O_RDWR, 0)
	must(err)
	if _, err = f.WriteString("0"); err != nil {
		f.Close()
		panic(err)
	}
	return f, true
}

func begin(c *child) *meter {
	m := &meter{c: c, start: time.Now(), done: make(chan struct{})}
	c.active = m
	if c.startupPending {
		m.start, m.r, m.w = c.started, c.startR, c.startW
		m.peakFD, m.reset, m.startup = c.startupPeak, c.startupReset, true
		c.startupPending, c.startupPeak = false, nil
	} else {
		m.r, m.w = ioBytes(c.cg, c.device)
		m.peakFD, m.reset = resetPeak(c.cg)
	}
	m.sample()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				m.sample()
			case <-m.done:
				return
			}
		}
	}()
	return m
}
func (m *meter) snapshot(phase string, elapsed float64, extra map[string]any) map[string]any {
	m.c.active = nil
	if m.startup {
		elapsed = time.Since(m.start).Seconds()
	}
	m.sample()
	close(m.done)
	m.wg.Wait()
	r, w := ioBytes(m.c.cg, m.c.device)
	if m.peakFD == nil || !m.reset {
		panic("phase memory peak was not reset through its retained FD")
	}
	_, err := m.peakFD.Seek(0, 0)
	must(err)
	peakText, err := io.ReadAll(m.peakFD)
	must(err)
	must(m.peakFD.Close())
	peak, err := strconv.ParseInt(strings.TrimSpace(string(peakText)), 10, 64)
	must(err)
	if peak <= 0 || r < m.r || w < m.w {
		panic("invalid phase peak or negative physical I/O delta")
	}
	scope := "phase_reset_fd"
	rec := map[string]any{"phase": phase, "t_s": elapsed, "wall_s": time.Since(m.start).Seconds(), "m_bytes": peak, "memory_peak_scope": scope, "sample_period_ms": 50, "sampled_peak": m.peak, "max_sampled_rss": m.maxRSS, "max_sampled_anon": m.maxAnon, "max_sampled_file": m.maxFile, "r_bytes": r - m.r, "w_bytes": w - m.w, "b_bytes": r - m.r + w - m.w, "device": m.c.device, "memory_events": fields(m.c.cg + "/memory.events"), "cpu_stat": fields(m.c.cg + "/cpu.stat"), "pid": m.c.cmd.Process.Pid}
	if m.startup {
		rec["startup_wall_s"] = elapsed
		rec["startup_meter_started_before_exec"] = true
	}
	for k, v := range extra {
		rec[k] = v
	}
	return rec
}

func checkedEmit(rec map[string]any) {
	emit(rec)
	events := rec["memory_events"].(map[string]int64)
	_, hasOOM := events["oom"]
	_, hasKill := events["oom_kill"]
	if !hasOOM || !hasKill || events["oom"] != 0 || events["oom_kill"] != 0 {
		panic("cgroup OOM in " + fmt.Sprint(rec["phase"]))
	}
}

func (m *meter) finish(phase string, elapsed float64, extra map[string]any) {
	checkedEmit(m.snapshot(phase, elapsed, extra))
}

type response struct {
	ID       int     `json:"id"`
	OK       bool    `json:"ok"`
	Type     string  `json:"type"`
	Rows     [][]any `json:"rows"`
	Affected int64   `json:"affected_rows"`
}
type child struct {
	cmd             *exec.Cmd
	in              io.WriteCloser
	rd              *bufio.Reader
	cg, device, dir string
	errfile         *os.File
	id              int
	stopped         bool
	active          *meter
	started         time.Time
	startR, startW  int64
	startupPeak     *os.File
	startupReset    bool
	startupPending  bool
}

var seq int
var sharedCG string
var currentChild *child

func start(binary, dir, device string) *child {
	seq++
	if sharedCG == "" {
		sharedCG = fmt.Sprintf("/sys/fs/cgroup/seekdb-round7-update-%d", os.Getpid())
		must(os.Mkdir(sharedCG, 0755))
		for k, v := range map[string]string{"cpu.max": "200000 100000", "memory.max": "2147483648", "memory.swap.max": "0", "pids.max": "128"} {
			must(os.WriteFile(sharedCG+"/"+k, []byte(v), 0644))
		}
	}
	cg := sharedCG
	f, err := os.Open(cg)
	must(err)
	defer f.Close()
	ef, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("stderr-%d.txt", seq)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	must(err)
	cmd := exec.Command(binary, "--data-dir", filepath.Join(dir, "db"), "--sync", "fdatasync", "--index-mem-mb", strconv.FormatInt(indexBudgetMB, 10))
	cmd.Env = databaseEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(f.Fd())}
	cmd.Stderr = ef
	in, err := cmd.StdinPipe()
	must(err)
	out, err := cmd.StdoutPipe()
	must(err)
	c := &child{cmd: cmd, in: in, rd: bufio.NewReaderSize(out, 1<<20), cg: cg, device: device, dir: dir, errfile: ef}
	c.startR, c.startW = ioBytes(cg, device)
	c.startupPeak, c.startupReset = resetPeak(cg)
	c.startupPending = true
	c.started = time.Now()
	must(cmd.Start())
	currentChild = c
	return c
}
func (c *child) read() []byte {
	b, err := c.rd.ReadBytes('\n')
	if err != nil {
		emit(map[string]any{"phase": "process_read_failure", "error": err.Error(), "memory_events": fields(c.cg + "/memory.events"), "m_bytes": number(c.cg + "/memory.peak"), "stderr": c.errfile.Name()})
		if c.active != nil {
			c.active.finish("failed_request", time.Since(c.active.start).Seconds(), map[string]any{"error": err.Error(), "stderr": c.errfile.Name()})
		}
		panic(err)
	}
	return b
}
func decode(b []byte) response {
	var r response
	must(json.Unmarshal(b, &r))
	if !r.OK {
		panic(string(b))
	}
	return r
}
func (c *child) ready() {
	b := c.read()
	var r response
	must(json.Unmarshal(b, &r))
	if r.Type != "ready" {
		panic(string(b))
	}
}
func (c *child) send(sql, op string) []byte {
	c.id++
	b, err := json.Marshal(map[string]any{"id": c.id, "op": op, "sql": sql, "search_mode": "exact"})
	must(err)
	_, err = c.in.Write(append(b, '\n'))
	must(err)
	b = c.read()
	return b
}
func (c *child) sql(sql string) response { return decode(c.send(sql, "execute")) }
func (c *child) stop(kill bool) {
	if c.stopped {
		return
	}
	if kill {
		must(c.cmd.Process.Kill())
	} else {
		r := decode(c.send("", "shutdown"))
		if r.ID != c.id {
			panic("shutdown response ID mismatch")
		}
	}
	c.in.Close()
	err := c.cmd.Wait()
	if !kill {
		must(err)
	} else {
		status, ok := c.cmd.ProcessState.Sys().(syscall.WaitStatus)
		if err == nil || !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
			panic(fmt.Sprintf("expected SIGKILL exit, got %v (%v)", err, c.cmd.ProcessState))
		}
	}
	c.errfile.Close()
	c.stopped = true
}
func (c *child) cleanup() {
	if c.startupPeak != nil {
		c.startupPeak.Close()
		c.startupPeak = nil
	}
	if !c.stopped {
		c.cmd.Process.Kill()
		c.cmd.Wait()
		c.in.Close()
		c.errfile.Close()
		c.stopped = true
	}
}

// SplitMix initializes a row-local xorshift stream. Dividing signed 8-bit
// integers by 16 is exact in float32 and float64 and gives short SQL literals.
func vector(dst []float32, seed uint64) {
	s := seed + 0x9e3779b97f4a7c15
	s = (s ^ (s >> 30)) * 0xbf58476d1ce4e5b9
	s = (s ^ (s >> 27)) * 0x94d049bb133111eb
	s ^= s >> 31
	for j := range dst {
		s ^= s << 13
		s ^= s >> 7
		s ^= s << 17
		dst[j] = float32(int(s>>56)-128) / 16
	}
}
func literal(v []float32) string {
	b := make([]byte, 0, len(v)*6)
	b = append(b, '[')
	for i, x := range v {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendFloat(b, float64(x), 'g', -1, 32)
	}
	return string(append(b, ']'))
}

func evictFixture(path string, extra map[string]any) map[string]any {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	must(err)
	defer f.Close()
	st, err := f.Stat()
	must(err)
	if st.Size() <= 0 {
		panic("empty recovery fixture")
	}
	must(f.Sync())
	_, _, errno := syscall.Syscall6(syscall.SYS_FADVISE64, f.Fd(), 0, 0, 4, 0, 0) // POSIX_FADV_DONTNEED
	if errno != 0 {
		panic(errno)
	}
	mapping, err := syscall.Mmap(int(f.Fd()), 0, int(st.Size()), syscall.PROT_NONE, syscall.MAP_SHARED)
	must(err)
	defer syscall.Munmap(mapping)
	pageSize := os.Getpagesize()
	pages := (len(mapping) + pageSize - 1) / pageSize
	resident := make([]byte, pages)
	_, _, errno = syscall.Syscall(syscall.SYS_MINCORE, uintptr(unsafe.Pointer(&mapping[0])), uintptr(len(mapping)), uintptr(unsafe.Pointer(&resident[0])))
	if errno != 0 {
		panic(errno)
	}
	n := 0
	for _, bits := range resident {
		if bits&1 != 0 {
			n++
		}
	}
	rec := map[string]any{"phase": "fixture_cache_evicted", "method": "file_sync_then_posix_fadvise_dontneed", "path": path, "file_bytes": st.Size(), "total_pages": pages, "resident_pages": n, "page_size": pageSize}
	for k, v := range extra {
		rec[k] = v
	}
	emit(rec)
	// A partial last page may remain. More means this run is not a cold-file
	// comparison and must not silently enter the results.
	if n > 1 {
		panic(fmt.Sprintf("fixture still has %d resident pages after fadvise", n))
	}
	return rec
}

const rounds, queryCount, topK = 5, 6, 10

var indexBudgetMB int64

func databaseEnv() []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "MVDB_") && !strings.HasPrefix(item, "GOMAXPROCS=") &&
			!strings.HasPrefix(item, "GOMEMLIMIT=") && !strings.HasPrefix(item, "GOGC=") {
			env = append(env, item)
		}
	}
	return append(env, "GOMAXPROCS=2", "GOMEMLIMIT=1288490188", "GOGC=20")
}

type model struct {
	rows, dim int
	updated   bool
}

func (m model) category() string {
	if m.updated {
		return "c8"
	}
	return "c9"
}
func (m model) seed(id int) uint64 {
	if m.updated && id == 1 {
		return uint64(m.rows + 100001)
	}
	if m.updated && id == m.rows {
		return uint64(m.rows + 100002)
	}
	return uint64(id)
}
func (m model) insert(first, last int) string {
	var b strings.Builder
	b.WriteString("INSERT INTO docs VALUES ")
	v := make([]float32, m.dim)
	for id := first; id <= last; id++ {
		if id > first {
			b.WriteByte(',')
		}
		vector(v, uint64(id))
		fmt.Fprintf(&b, "(%d,'c9','%s')", id, literal(v))
	}
	return b.String()
}

type hit struct {
	id      int
	squared float64
}
type query struct {
	label, sql string
	v          []float32
	want       []hit
}
type truth struct {
	m                  model
	qs                 []query
	norms              []float64
	sqlHash, truthHash string
}

// The independent oracle regenerates one row at a time and exhaustively
// computes scalar float64 L2 for every query. Only TopK and scalar norms are
// retained; no production code or baseline response is used as truth.
func prepareTruth(m model) truth {
	t := truth{m: m, qs: make([]query, queryCount), norms: make([]float64, m.rows)}
	for i := range t.qs {
		q := query{v: make([]float32, m.dim)}
		switch i {
		case 0:
			q.label = "zero"
		case 1:
			q.label = "random_a"
			vector(q.v, uint64(m.rows+200001))
		case 2:
			q.label = "original_id17"
			vector(q.v, 17)
		case 3:
			q.label = "new_front_vector"
			vector(q.v, uint64(m.rows+100001))
		case 4:
			q.label = "new_tail_vector"
			vector(q.v, uint64(m.rows+100002))
		case 5:
			q.label = "random_b"
			vector(q.v, uint64(m.rows+200005))
		}
		where := ""
		if i%2 == 1 {
			where = " WHERE category='" + m.category() + "'"
			q.label += "/category"
		} else {
			q.label += "/all"
		}
		q.sql = fmt.Sprintf("SELECT id,category,L2_DISTANCE(embedding,'%s') AS distance FROM docs%s ORDER BY distance LIMIT %d", literal(q.v), where, topK)
		t.qs[i] = q
	}
	v := make([]float32, m.dim)
	for id := 1; id <= m.rows; id++ {
		vector(v, m.seed(id))
		var norm float64
		for _, x := range v {
			norm += float64(x) * float64(x)
		}
		t.norms[id-1] = math.Sqrt(norm)
		for qi := range t.qs {
			q := &t.qs[qi]
			var d float64
			for k, x := range v {
				z := float64(x) - float64(q.v[k])
				d += z * z
			}
			at := sort.Search(len(q.want), func(j int) bool { return q.want[j].squared > d || (q.want[j].squared == d && q.want[j].id > id) })
			if at >= topK {
				continue
			}
			q.want = append(q.want, hit{})
			copy(q.want[at+1:], q.want[at:len(q.want)-1])
			q.want[at] = hit{id, d}
			if len(q.want) > topK {
				q.want = q.want[:topK]
			}
		}
	}
	var statements []string
	var expected [][][]any
	for _, q := range t.qs {
		statements = append(statements, q.sql)
		expected = append(expected, expectedRows(q, m.category()))
	}
	t.sqlHash = digestBytes([]byte(strings.Join(statements, "\n")))
	b, err := json.Marshal(expected)
	must(err)
	t.truthHash = digestBytes(b)
	return t
}
func expectedRows(q query, category string) [][]any {
	rows := make([][]any, len(q.want))
	for i, h := range q.want {
		rows[i] = []any{h.id, category, math.Sqrt(h.squared)}
	}
	return rows
}
func emitTruth(state string, t truth, seconds float64) {
	var statements, labels []string
	var expected [][][]any
	for _, q := range t.qs {
		statements = append(statements, q.sql)
		labels = append(labels, q.label)
		expected = append(expected, expectedRows(q, t.m.category()))
	}
	normJSON, err := json.Marshal(t.norms)
	must(err)
	emit(map[string]any{"phase": "oracle_definition", "state": state, "category": t.m.category(), "statements": statements, "labels": labels,
		"expected_topk": expected, "sql_sha256": t.sqlHash, "truth_sha256": t.truthHash, "norms_sha256": digestBytes(normJSON),
		"unique_queries": queryCount, "oracle_rows_per_query": t.m.rows, "oracle_s": seconds,
		"row_seed_rule": "initial: id; updated: id1=rows+100001, idRows=rows+100002, all other IDs unchanged",
		"query_seeds":   []any{"zero", t.m.rows + 200001, 17, t.m.rows + 100001, t.m.rows + 100002, t.m.rows + 200005},
		"oracle":        "streamed independent scalar float64 exhaustive L2; exact ordered PK ties; census norms are not elementwise vector comparison"})
}
func validateRow(row []any, id int, category string, distance float64) {
	if len(row) != 3 {
		panic("response row width mismatch")
	}
	pk, pkOK := row[0].(float64)
	cat, catOK := row[1].(string)
	got, dOK := row[2].(float64)
	if !pkOK || !catOK || !dOK || pk != float64(id) || cat != category || math.IsNaN(got) || math.IsInf(got, 0) || got < 0 ||
		(distance == 0 && got != 0) || math.Abs(got-distance) > 1e-12*math.Max(1, distance) {
		panic(fmt.Sprintf("truth mismatch: got %v want id=%d category=%s distance=%.17g", row, id, category, distance))
	}
}
func validateQuery(b []byte, id int, q query, category string) []byte {
	r := decode(b)
	if r.ID != id || len(r.Rows) != len(q.want) || len(q.want) != topK {
		panic("TopK response ID/count mismatch")
	}
	var previous float64
	var previousID int
	for i, want := range q.want {
		validateRow(r.Rows[i], want.id, category, math.Sqrt(want.squared))
		got := r.Rows[i][2].(float64)
		if i > 0 && (got < previous || (got == previous && want.id <= previousID)) {
			panic("TopK tie ordering mismatch")
		}
		previous, previousID = got, want.id
	}
	canonical, err := json.Marshal(r.Rows)
	must(err)
	return canonical
}
func queryBatch(c *child, t truth, phase string, extra map[string]any) string {
	meter := begin(c)
	responses := make([][]byte, len(t.qs))
	ids := make([]int, len(t.qs))
	perQuery := make([]float64, len(t.qs))
	started := time.Now()
	for i, q := range t.qs {
		ts := time.Now()
		responses[i] = c.send(q.sql, "execute")
		perQuery[i] = time.Since(ts).Seconds()
		ids[i] = c.id
	}
	elapsed := time.Since(started).Seconds()
	record := meter.snapshot(phase, elapsed, merge(extra, map[string]any{"sql_sha256": t.sqlHash, "truth_sha256": t.truthHash,
		"request_ids": ids, "per_query_t_s": perQuery, "reference_bad": 0, "reference_checked": queryCount, "reference_rows_checked": queryCount * topK}))
	h := sha256.New()
	for i, response := range responses {
		h.Write(validateQuery(response, ids[i], t.qs[i], t.m.category()))
		h.Write([]byte{'\n'})
	}
	digest := fmt.Sprintf("%x", h.Sum(nil))
	record["response_sha256"] = digest
	checkedEmit(record)
	emit(merge(extra, map[string]any{"phase": "query_validation", "batch": phase, "request_ids": ids, "reference_bad": 0,
		"reference_checked": queryCount, "reference_rows_checked": queryCount * topK, "sql_sha256": t.sqlHash, "truth_sha256": t.truthHash, "response_sha256": digest}))
	return digest
}
func census(c *child, t truth, phase string, extra map[string]any) string {
	sql := fmt.Sprintf("SELECT id,category,L2_DISTANCE(embedding,'%s') AS distance FROM docs ORDER BY id", literal(make([]float32, t.m.dim)))
	meter := begin(c)
	started := time.Now()
	response := c.send(sql, "execute")
	elapsed := time.Since(started).Seconds()
	record := meter.snapshot(phase, elapsed, merge(extra, map[string]any{"request_id": c.id, "sql_sha256": digestBytes([]byte(sql)),
		"reference_bad": 0, "reference_checked": 1, "reference_rows_checked": t.m.rows}))
	r := decode(response)
	if r.ID != c.id || len(r.Rows) != t.m.rows {
		panic("census response ID/count mismatch")
	}
	for i, row := range r.Rows {
		validateRow(row, i+1, t.m.category(), t.norms[i])
	}
	canonical, err := json.Marshal(r.Rows)
	must(err)
	digest := digestBytes(canonical)
	record["response_sha256"] = digest
	checkedEmit(record)
	emit(merge(extra, map[string]any{"phase": "census_validation", "batch": phase, "request_id": c.id, "reference_bad": 0,
		"reference_checked": 1, "reference_rows_checked": t.m.rows, "response_sha256": digest,
		"scope": "all primary keys, categories and independently regenerated scalar norms; not elementwise vectors"}))
	return digest
}
func acknowledged(b []byte, id int, affected int64) {
	r := decode(b)
	if r.ID != id || r.Affected != affected {
		panic(fmt.Sprintf("DML acknowledgement mismatch id=%d affected=%d want=%d", r.ID, r.Affected, affected))
	}
}
func load(c *child, m model, batch int, extra map[string]any) {
	meter := begin(c)
	elapsed := 0.0
	sql := fmt.Sprintf("CREATE TABLE docs(id BIGINT PRIMARY KEY,category VARCHAR(16),embedding VECTOR(%d))", m.dim)
	started := time.Now()
	response := c.send(sql, "execute")
	elapsed += time.Since(started).Seconds()
	acknowledged(response, c.id, 0)
	count := 0
	requests := 1
	for first := 1; first <= m.rows; first += batch {
		last := min(first+batch-1, m.rows)
		sql = m.insert(first, last)
		started = time.Now()
		response = c.send(sql, "execute")
		elapsed += time.Since(started).Seconds()
		acknowledged(response, c.id, int64(last-first+1))
		count += last - first + 1
		requests++
	}
	meter.finish("load", elapsed, merge(extra, map[string]any{"acknowledged_rows": count, "requests": requests, "last_request_id": c.id,
		"generator_excluded_from_t": true, "includes_create_table": true, "reference_bad": 0}))
	requireDeviceIO(c.cg, c.device)
}
func mutate(c *child, sql, phase string, affected int64, extra map[string]any) {
	meter := begin(c)
	started := time.Now()
	response := c.send(sql, "execute")
	elapsed := time.Since(started).Seconds()
	record := meter.snapshot(phase, elapsed, merge(extra, map[string]any{"request_id": c.id, "sql_sha256": digestBytes([]byte(sql)),
		"affected_rows_verified": affected, "reference_bad": 0, "index_prebuilt": true, "ack_includes_fdatasync": true}))
	acknowledged(response, c.id, affected)
	checkedEmit(record)
}
func digestBytes(b []byte) string { digest := sha256.Sum256(b); return fmt.Sprintf("%x", digest) }
func hashFile(path string) string {
	f, err := os.Open(path)
	must(err)
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	must(err)
	return fmt.Sprintf("%x", h.Sum(nil))
}
func merge(a, b map[string]any) map[string]any {
	r := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		r[k] = v
	}
	for k, v := range b {
		r[k] = v
	}
	return r
}
func stop(c *child, phase string, extra map[string]any) {
	meter := begin(c)
	started := time.Now()
	c.stop(false)
	meter.finish(phase, time.Since(started).Seconds(), merge(extra, map[string]any{"process_exit_code": c.cmd.ProcessState.ExitCode(), "shutdown_request_id": c.id}))
	c.cleanup()
}

func main() {
	binsFlag := flag.String("bins", "", "two absolute executables, baseline first")
	workDir := flag.String("work-dir", "", "new parent directory for ten fresh trial databases")
	rows := flag.Int("rows", 10000, "rows in each fresh database")
	dim := flag.Int("dim", 1024, "vector dimensions")
	batch := flag.Int("batch", 64, "streamed INSERT batch size")
	flag.Int64Var(&indexBudgetMB, "index-mem-mb", 4, "derived-index budget MiB, default intentionally cannot hold all codes")
	flag.Parse()
	if *rows < 100 || *dim < 16 || *batch < 1 || indexBudgetMB < 1 || indexBudgetMB > 256 || !filepath.IsAbs(*workDir) {
		panic("invalid rows/dim/batch/budget or missing absolute work-dir")
	}
	if int64(*rows)*int64(*dim) <= indexBudgetMB<<20 || int64(*rows)*64 >= indexBudgetMB<<20 {
		panic("require slots to fit but full code bytes alone to exceed the index budget")
	}
	bins := strings.Split(*binsFlag, ",")
	if len(bins) != 2 || filepath.Base(bins[0]) == filepath.Base(bins[1]) {
		panic("require two distinct binary basenames")
	}
	binaryHashes := map[string]string{}
	for _, binary := range bins {
		info, err := os.Stat(binary)
		must(err)
		if !filepath.IsAbs(binary) || !info.Mode().IsRegular() {
			panic("invalid binary")
		}
		binaryHashes[binary] = hashFile(binary)
	}
	if _, err := os.Stat(*workDir); !os.IsNotExist(err) {
		panic("work-dir must not exist")
	}
	must(os.MkdirAll(*workDir, 0755))
	must(os.WriteFile("/sys/fs/cgroup/cgroup.subtree_control", []byte("+cpu +memory +io +pids"), 0644))
	defer func() {
		if sharedCG != "" {
			os.Remove(sharedCG)
		}
	}()
	defer func() {
		if problem := recover(); problem != nil {
			emit(map[string]any{"phase": "failure", "error": fmt.Sprint(problem), "memory_events": fields(sharedCG + "/memory.events")})
			if currentChild != nil {
				if currentChild.active != nil {
					func() {
						defer func() { _ = recover() }()
						m := currentChild.active
						m.finish("failed_phase", time.Since(m.start).Seconds(), map[string]any{"error": fmt.Sprint(problem)})
					}()
				}
				currentChild.cleanup()
			}
			panic(problem)
		}
	}()
	emit(map[string]any{"phase": "config", "mode": "round7_indexed_update_interleaved", "bins": bins, "binary_sha256": binaryHashes,
		"work_dir": *workDir, "rows": *rows, "dim": *dim, "batch": *batch, "rounds": rounds, "queries_per_batch": queryCount, "top_k": topK,
		"unique_state_query_oracles": 2 * queryCount, "raw_vector_bytes": int64(*rows) * int64(*dim) * 4, "full_code_bytes": int64(*rows) * int64(*dim),
		"slot_estimate_bytes": int64(*rows) * 64, "index_mem_mb": indexBudgetMB, "full_codes_exceed_budget": true,
		"memory_limit": int64(2 << 30), "memory_swap_max": 0, "cpu_max": "200000 100000", "pids_max": 128, "sync": "fdatasync", "search_mode": "exact",
		"database_env": []string{"GOMAXPROCS=2", "GOMEMLIMIT=1288490188", "GOGC=20"}, "inherited_mvdb_env_removed": true,
		"generator":                 "row-local xorshift signed uint8/16; initial seed=id; initial category=c9",
		"cache_policy":              "fresh database per trial; loaded process for index/update queries; sync+private-file eviction after each shutdown; cold recovery; one cgroup reused",
		"timing":                    "SQL generation and external oracle/decoding/hashes/eviction excluded; all measured stages retained; no invented cross-stage weights",
		"is_over_2gib_capacity_run": false})
	initial := model{rows: *rows, dim: *dim}
	updated := model{rows: *rows, dim: *dim, updated: true}
	t := time.Now()
	before := prepareTruth(initial)
	emitTruth("initial", before, time.Since(t).Seconds())
	t = time.Now()
	after := prepareTruth(updated)
	emitTruth("updated", after, time.Since(t).Seconds())
	var st syscall.Stat_t
	must(syscall.Stat(*workDir, &st))
	major := (uint64(st.Dev)>>8)&0xfff | (uint64(st.Dev)>>32)&0xfffff000
	minor := uint64(st.Dev)&0xff | (uint64(st.Dev)>>12)&0xffffff00
	device := fmt.Sprintf("%d:%d", major, minor)
	front := make([]float32, *dim)
	tail := make([]float32, *dim)
	vector(front, updated.seed(1))
	vector(tail, updated.seed(*rows))
	categorySQL := "UPDATE docs SET category='c8'"
	frontSQL := fmt.Sprintf("UPDATE docs SET embedding='%s' WHERE id=1", literal(front))
	tailSQL := fmt.Sprintf("UPDATE docs SET embedding='%s' WHERE id=%d", literal(tail), *rows)
	emit(map[string]any{"phase": "update_definition", "category_sql": categorySQL, "front_sql": frontSQL, "tail_sql": tailSQL,
		"category_sql_sha256": digestBytes([]byte(categorySQL)), "front_sql_sha256": digestBytes([]byte(frontSQL)), "tail_sql_sha256": digestBytes([]byte(tailSQL)),
		"front_id": 1, "tail_id": *rows, "front_seed": updated.seed(1), "tail_seed": updated.seed(*rows),
		"scope": "all rows change category c9 to c8; existing front and tail rows then receive distinct regenerated vectors"})
	responseHashes := map[string]string{}
	for round := 0; round < rounds; round++ {
		order := append([]string(nil), bins...)
		if round%2 == 1 {
			order[0], order[1] = order[1], order[0]
		}
		for position, binary := range order {
			name := filepath.Base(binary)
			dir := filepath.Join(*workDir, fmt.Sprintf("trial-%d-%s", round, name))
			must(os.Mkdir(dir, 0755))
			if hashFile(binary) != binaryHashes[binary] {
				panic("binary changed before trial")
			}
			extra := map[string]any{"name": name, "binary": binary, "binary_sha256": binaryHashes[binary], "round": round, "position": position, "trial_dir": dir}
			emit(merge(extra, map[string]any{"phase": "trial_begin", "fresh_database": true}))
			c := start(binary, dir, device)
			meter := begin(c)
			c.ready()
			meter.finish("startup", 0, extra)
			load(c, initial, *batch, extra)
			initialHash := queryBatch(c, before, "index_query", extra)
			mutate(c, categorySQL, "category_update", int64(*rows), extra)
			mutate(c, frontSQL, "point_update_front", 1, extra)
			mutate(c, tailSQL, "point_update_tail", 1, extra)
			updatedHash := queryBatch(c, after, "updated_query", extra)
			censusHash := census(c, after, "updated_census", extra)
			stop(c, "shutdown", extra)
			log := filepath.Join(dir, "db/data.log")
			info, err := os.Stat(log)
			must(err)
			logHash := hashFile(log)
			emit(merge(extra, map[string]any{"phase": "log_snapshot", "path": log, "log_bytes": info.Size(), "sha256": logHash, "state": "updated_committed"}))
			cache := evictFixture(log, merge(extra, map[string]any{"stage": "before_recovery"}))
			c = start(binary, dir, device)
			meter = begin(c)
			c.ready()
			meter.finish("recovery", 0, merge(extra, map[string]any{"resident_pages_before_recovery": cache["resident_pages"]}))
			recoveredHash := queryBatch(c, after, "recovered_query", extra)
			recoveredCensusHash := census(c, after, "recovered_census", extra)
			stop(c, "final_shutdown", extra)
			finalInfo, err := os.Stat(log)
			must(err)
			if finalInfo.Size() != info.Size() || hashFile(log) != logHash {
				panic("read-only recovery changed committed log")
			}
			emit(merge(extra, map[string]any{"phase": "log_unchanged", "path": log, "log_bytes": finalInfo.Size(), "sha256": logHash, "verified": true}))
			evictFixture(log, merge(extra, map[string]any{"stage": "after_trial"}))
			if updatedHash != recoveredHash || censusHash != recoveredCensusHash {
				panic("recovery changed visible state")
			}
			for state, digest := range map[string]string{"initial": initialHash, "updated": updatedHash, "census": censusHash} {
				if old, ok := responseHashes[state]; ok && old != digest {
					panic("normalized response differs across versions/rounds")
				}
				responseHashes[state] = digest
			}
			if hashFile(binary) != binaryHashes[binary] {
				panic("binary changed during trial")
			}
			checkedEmit(merge(extra, map[string]any{"phase": "trial_complete", "reference_bad": 0, "topk_checked": 3 * queryCount,
				"census_queries_checked": 2, "census_rows_checked": 2 * *rows, "affected_rows_verified": *rows + 2, "process_exits_verified": 2,
				"log_sha256": logHash, "private_cache_evicted_after_stop": true, "memory_events": fields(sharedCG + "/memory.events")}))
		}
	}
	for _, binary := range bins {
		if hashFile(binary) != binaryHashes[binary] {
			panic("binary changed during comparison")
		}
	}
	checkedEmit(map[string]any{"phase": "success", "reference_bad": 0, "completed_trials": 2 * rounds, "topk_checked": 2 * rounds * 3 * queryCount,
		"census_queries_checked": 4 * rounds, "census_rows_checked": 4 * rounds * *rows, "affected_rows_verified": 2 * rounds * (*rows + 2),
		"unique_state_query_oracles": 2 * queryCount, "process_exits_verified": 4 * rounds, "binary_sha256_unchanged": binaryHashes,
		"response_sha256": responseHashes, "cgroup_reused_across_restarts": true, "all_private_caches_evicted_after_stop": true, "memory_events": fields(sharedCG + "/memory.events")})
}
