// Linux-only ROUND7 large indexed-update comparison, forked from the large driver.
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
		sharedCG = fmt.Sprintf("/sys/fs/cgroup/seekdb-round7-large-update-%d", os.Getpid())
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
	cmd := exec.Command(binary, "--data-dir", filepath.Join(dir, "db"), "--sync", "fdatasync", "--index-mem-mb", "256")
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

// finalModel describes the completed ROUND3 fixture independently of any
// query response: 16 replacements, one committed transaction replacement,
// three deletions, then a committed UPDATE setting every category to c9.
// Rolled-back and killed uncommitted changes are absent.
type finalModel struct {
	rows, dim int
	live      []int
	deleted   map[int]bool
	category  string
	norms     []float64
}

func makeModel(rows, dim int) *finalModel {
	m := &finalModel{rows: rows, dim: dim, category: "c9", norms: make([]float64, rows+1), deleted: map[int]bool{rows / 3: true, rows / 2: true, rows - 1: true}}
	for id := 1; id <= rows; id++ {
		if !m.deleted[id] {
			m.live = append(m.live, id)
		}
	}
	return m
}

func (m *finalModel) seed(id int) uint64 {
	if id >= 1 && id <= 16 {
		return uint64(m.rows + id + 100000)
	}
	if id == 18 {
		return 1000018
	}
	return uint64(id)
}

const comparisonRounds, topK = 5, 10

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

type hit struct {
	id      int
	squared float64
}
type topCase struct {
	label, sql string
	category   string
	v          []float32
	want       []hit
}

func prepareCases(m *finalModel, count int) []topCase {
	qs := make([]topCase, count)
	for i := range qs {
		q := topCase{v: make([]float32, m.dim), category: m.category}
		switch i % 6 {
		case 0:
			q.label = "zero"
		case 1:
			q.label = "random_a"
			vector(q.v, uint64(m.rows+700001))
		case 2:
			q.label = "original_id17"
			vector(q.v, m.seed(17))
		case 3:
			q.label = "replacement_id1"
			vector(q.v, m.seed(1))
		case 4:
			q.label = "committed_replacement_id18"
			vector(q.v, m.seed(18))
		case 5:
			q.label = "random_b"
			vector(q.v, uint64(m.rows+200005))
		}
		filtered := i%2 == 1
		if i >= 6 {
			filtered = !filtered
		}
		where := ""
		q.label += "/all"
		if filtered {
			where = " WHERE category='" + m.category + "'"
			q.label = strings.TrimSuffix(q.label, "/all") + "/" + m.category
		}
		q.sql = fmt.Sprintf("SELECT id,category,L2_DISTANCE(embedding,'%s') AS distance FROM docs%s ORDER BY distance LIMIT %d", literal(q.v), where, topK)
		qs[i] = q
	}
	return qs
}

// No production package, vector table, index, or database response is used.
// Regenerate one live row at a time, exhaustively compare it with every query,
// and retain only K hits, sorted by squared distance then primary key.
func oracle(m *finalModel, qs []topCase) {
	v := make([]float32, m.dim)
	for _, id := range m.live {
		vector(v, m.seed(id))
		var norm float64
		for _, x := range v {
			norm += float64(x) * float64(x)
		}
		m.norms[id] = math.Sqrt(norm)
		for qi := range qs {
			q := &qs[qi]
			var squared float64
			for k, x := range v {
				d := float64(x) - float64(q.v[k])
				squared += d * d
			}
			at := sort.Search(len(q.want), func(j int) bool {
				return q.want[j].squared > squared || (q.want[j].squared == squared && q.want[j].id > id)
			})
			if at >= topK {
				continue
			}
			q.want = append(q.want, hit{})
			copy(q.want[at+1:], q.want[at:len(q.want)-1])
			q.want[at] = hit{id, squared}
			if len(q.want) > topK {
				q.want = q.want[:topK]
			}
		}
	}
	for _, q := range qs {
		if len(q.want) != topK {
			panic("oracle did not produce K live rows")
		}
	}
}

func expectedRows(q topCase) [][]any {
	rows := make([][]any, len(q.want))
	for i, h := range q.want {
		rows[i] = []any{h.id, q.category, math.Sqrt(h.squared)}
	}
	return rows
}

func validateCase(b []byte, requestID int, q topCase) []byte {
	r := decode(b)
	if r.ID != requestID || len(r.Rows) != len(q.want) {
		panic(fmt.Sprintf("%s: response ID/count mismatch", q.label))
	}
	var previousDistance float64
	var previousID int
	for i, want := range q.want {
		row := r.Rows[i]
		if len(row) != 3 {
			panic(fmt.Sprintf("%s: row width mismatch", q.label))
		}
		id, idOK := row[0].(float64)
		category, catOK := row[1].(string)
		distance, distanceOK := row[2].(float64)
		expected := math.Sqrt(want.squared)
		if !idOK || !catOK || !distanceOK || id != float64(want.id) || category != q.category ||
			math.IsNaN(distance) || math.IsInf(distance, 0) || distance < 0 ||
			(expected == 0 && distance != 0) || math.Abs(distance-expected) > 1e-12*math.Max(1, expected) {
			panic(fmt.Sprintf("%s row %d: got %v want id=%d distance=%.17g", q.label, i, row, want.id, expected))
		}
		if i > 0 && (distance < previousDistance || (distance == previousDistance && want.id <= previousID)) {
			panic(fmt.Sprintf("%s: response is not ordered by distance then primary key", q.label))
		}
		previousDistance, previousID = distance, want.id
	}
	canonical, err := json.Marshal(r.Rows)
	must(err)
	return canonical
}

func hashFile(path string) string {
	f, err := os.Open(path)
	must(err)
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	must(err)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func fixtureSignature(path string) string {
	s, err := os.Stat(path)
	must(err)
	return fmt.Sprintf("%d:%d", s.Size(), s.ModTime().UnixNano())
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

func queryBatch(c *child, qs []topCase, batch string, extra map[string]any) string {
	m := begin(c)
	responses := make([][]byte, len(qs))
	ids := make([]int, len(qs))
	perQuery := make([]float64, len(qs))
	started := time.Now()
	for i, q := range qs {
		t := time.Now()
		responses[i] = c.send(q.sql, "execute")
		perQuery[i] = time.Since(t).Seconds()
		ids[i] = c.id
	}
	elapsed := time.Since(started).Seconds()
	// Freeze physical I/O and the retained-FD memory peak before decoding
	// responses or checking the independent oracle outside the cgroup.
	record := m.snapshot(batch, elapsed, merge(extra, map[string]any{
		"batch": batch, "request_ids": ids, "per_query_t_s": perQuery,
		"reference_bad": 0, "reference_checked": len(qs), "reference_rows_checked": len(qs) * topK,
	}))
	h := sha256.New()
	for i, response := range responses {
		h.Write(validateCase(response, ids[i], qs[i]))
		h.Write([]byte{'\n'})
	}
	digest := fmt.Sprintf("%x", h.Sum(nil))
	record["response_sha256"] = digest
	checkedEmit(record)
	emit(merge(extra, map[string]any{"phase": "batch_validation", "batch": batch,
		"request_ids": ids, "reference_bad": 0, "reference_checked": len(qs),
		"reference_rows_checked": len(qs) * topK, "response_sha256": digest,
		"validation": "exhaustive row-generator scalar float64 oracle; exact request IDs, ordered primary keys, categories, distances and ties"}))
	return digest
}

const queryCount = 6
const updateSQL = "UPDATE docs SET category='c8'"

func digestBytes(b []byte) string { h := sha256.Sum256(b); return fmt.Sprintf("%x", h) }
func expectedBatchHash(qs []topCase) string {
	h := sha256.New()
	for _, q := range qs {
		b, err := json.Marshal(expectedRows(q))
		must(err)
		h.Write(b)
		h.Write([]byte{'\n'})
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
func censusHash(m *finalModel, category string) string {
	h := sha256.New()
	h.Write([]byte{'['})
	for i, id := range m.live {
		if i > 0 {
			h.Write([]byte{','})
		}
		b, err := json.Marshal([]any{id, category, m.norms[id]})
		must(err)
		h.Write(b)
	}
	h.Write([]byte{']'})
	return fmt.Sprintf("%x", h.Sum(nil))
}
func emitOracle(m *finalModel, qs []topCase, state string, oracleS float64) map[string]any {
	var statements, labels []string
	var expected [][][]any
	for _, q := range qs {
		statements = append(statements, q.sql)
		labels = append(labels, q.label)
		expected = append(expected, expectedRows(q))
	}
	b, err := json.Marshal(expected)
	must(err)
	record := map[string]any{"phase": "oracle_definition", "state": state, "category": m.category, "statements": statements, "labels": labels,
		"expected_topk": expected, "sql_sha256": digestBytes([]byte(strings.Join(statements, "\n"))), "truth_sha256": digestBytes(b),
		"expected_response_sha256": expectedBatchHash(qs), "unique_queries": queryCount, "oracle_rows_per_query": len(m.live), "oracle_s": oracleS,
		"row_seed_rule": "IDs 1..16 use rows+id+100000; id18 uses 1000018; others seed=id; rows/3,rows/2,rows-1 deleted",
		"query_seeds":   []any{"zero", m.rows + 700001, 17, m.seed(1), 1000018, m.rows + 200005},
		"vector_oracle_reused_across_category_states": true}
	if state == "updated" {
		record["census_expected_response_sha256"] = censusHash(m, "c8")
	}
	emit(record)
	return record
}
func measuredUpdate(c *child, live int, extra map[string]any) {
	meter := begin(c)
	started := time.Now()
	b := c.send(updateSQL, "execute")
	elapsed := time.Since(started).Seconds()
	record := meter.snapshot("category_update", elapsed, merge(extra, map[string]any{"request_id": c.id, "sql_sha256": digestBytes([]byte(updateSQL)),
		"affected_rows_verified": live, "reference_bad": 0, "index_prebuilt": true, "ack_includes_fdatasync": true}))
	r := decode(b)
	if r.ID != c.id || r.Affected != int64(live) {
		panic("UPDATE response ID/affected count mismatch")
	}
	checkedEmit(record)
}
func measuredCensus(c *child, m *finalModel, expectedHash string, extra map[string]any) string {
	sql := fmt.Sprintf("SELECT id,category,L2_DISTANCE(embedding,'%s') AS distance FROM docs ORDER BY id", literal(make([]float32, m.dim)))
	meter := begin(c)
	started := time.Now()
	b := c.send(sql, "execute")
	elapsed := time.Since(started).Seconds()
	record := meter.snapshot("census", elapsed, merge(extra, map[string]any{"request_id": c.id, "sql_sha256": digestBytes([]byte(sql)),
		"reference_bad": 0, "reference_checked": 1, "reference_rows_checked": len(m.live)}))
	r := decode(b)
	if r.ID != c.id || len(r.Rows) != len(m.live) {
		panic("census response ID/count mismatch")
	}
	for i, id := range m.live {
		row := r.Rows[i]
		if len(row) != 3 {
			panic("census row width mismatch")
		}
		pk, pok := row[0].(float64)
		cat, cok := row[1].(string)
		distance, dok := row[2].(float64)
		want := m.norms[id]
		if !pok || !cok || !dok || pk != float64(id) || cat != "c8" || math.IsNaN(distance) || math.IsInf(distance, 0) || distance < 0 ||
			(want == 0 && distance != 0) || math.Abs(distance-want) > 1e-12*math.Max(1, want) {
			panic(fmt.Sprintf("census mismatch for id %d: %v", id, row))
		}
	}
	canonical, err := json.Marshal(r.Rows)
	must(err)
	digest := digestBytes(canonical)
	if digest != expectedHash {
		panic("census response hash differs from independent norm oracle")
	}
	record["response_sha256"] = digest
	checkedEmit(record)
	emit(merge(extra, map[string]any{"phase": "census_validation", "request_id": c.id, "reference_bad": 0, "reference_checked": 1,
		"reference_rows_checked": len(m.live), "response_sha256": digest, "scope": "every live PK, c8 category and independent scalar norm; not elementwise vectors"}))
	return digest
}

// Reset only the private inode after the measured process has exited. The
// original prefix is hashed before truncation; any change prevents mutation.
// SectionReader bounds the hash to the original prefix, not the appended WAL.
func restorePrivate(path string, sourceID, privateID os.FileInfo, originalBytes, liveBytes int64, originalHash string, extra map[string]any) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	must(err)
	defer f.Close()
	info, err := f.Stat()
	must(err)
	if !os.SameFile(info, privateID) || os.SameFile(info, sourceID) || info.Size() <= originalBytes || info.Size()-originalBytes < liveBytes {
		panic("refusing reset: private inode identity or appended log length is invalid")
	}
	h := sha256.New()
	copied, err := io.Copy(h, io.NewSectionReader(f, 0, originalBytes))
	must(err)
	prefixHash := fmt.Sprintf("%x", h.Sum(nil))
	if copied != originalBytes || prefixHash != originalHash {
		panic("refusing reset: original private log prefix changed")
	}
	emit(merge(extra, map[string]any{"phase": "prefix_verified", "path": path, "prefix_bytes": copied, "prefix_sha256": prefixHash,
		"original_bytes": originalBytes, "appended_log_bytes": info.Size(), "appended_bytes": info.Size() - originalBytes,
		"private_inode_verified": true, "source_inode_distinct": true, "process_exited_before_reset": true}))
	must(f.Truncate(originalBytes))
	must(f.Sync())
	info, err = f.Stat()
	must(err)
	if info.Size() != originalBytes || !os.SameFile(info, privateID) || os.SameFile(info, sourceID) {
		panic("private restore changed inode or length")
	}
	h = sha256.New()
	copied, err = io.Copy(h, io.NewSectionReader(f, 0, originalBytes))
	must(err)
	restoredHash := fmt.Sprintf("%x", h.Sum(nil))
	if copied != originalBytes || restoredHash != originalHash {
		panic("private restore full hash differs from fixture")
	}
	emit(merge(extra, map[string]any{"phase": "fixture_restored", "path": path, "file_bytes": info.Size(), "sha256": restoredHash,
		"truncate_synced": true, "private_inode_verified": true, "source_inode_distinct": true}))
}

func main() {
	binsFlag := flag.String("bins", "", "two absolute executables, reference first")
	fixture := flag.String("fixture", "", "completed final-all-c9 source log; never modified")
	workDir := flag.String("work-dir", "", "new directory for one private log inode")
	rows := flag.Int("rows", 550000, "original row count before three deleted IDs")
	dim := flag.Int("dim", 1024, "vector dimensions")
	allowSmall := flag.Bool("allow-small", false, "functional smoke only, not >2GiB capacity evidence")
	flag.Parse()
	if *rows < 100 || *dim < 1 || !filepath.IsAbs(*fixture) || !filepath.IsAbs(*workDir) {
		panic("invalid rows/dim or missing absolute fixture/work-dir")
	}
	m := makeModel(*rows, *dim)
	liveBytes := int64(len(m.live)) * int64(*dim) * 4
	if liveBytes <= 2<<30 && !*allowSmall {
		panic("live vectors must exceed 2GiB unless allow-small is explicit")
	}
	bins := strings.Split(*binsFlag, ",")
	if len(bins) != 2 || filepath.Base(bins[0]) == filepath.Base(bins[1]) {
		panic("two distinct binary basenames required")
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
						meter := currentChild.active
						meter.finish("failed_phase", time.Since(meter.start).Seconds(), map[string]any{"error": fmt.Sprint(problem)})
					}()
				}
				currentChild.cleanup()
			}
			panic(problem)
		}
	}()
	emit(map[string]any{"phase": "config", "mode": "round7_large_update_interleaved", "bins": bins, "binary_sha256": binaryHashes,
		"fixture_source": *fixture, "work_dir": *workDir, "fixture_model": "completed_final_all_c9", "rows": *rows, "live_rows": len(m.live), "dim": *dim,
		"raw_vector_bytes": int64(*rows) * int64(*dim) * 4, "live_vector_bytes": liveBytes, "exceeds_2gib": liveBytes > 2<<30, "allow_small": *allowSmall,
		"rounds": comparisonRounds, "queries_per_batch": queryCount, "top_k": topK, "unique_state_query_oracles": 2 * queryCount,
		"memory_limit": int64(2 << 30), "memory_swap_max": 0, "cpu_max": "200000 100000", "pids_max": 128, "index_mem_mb": 256, "sync": "fdatasync", "search_mode": "exact",
		"database_env": []string{"GOMAXPROCS=2", "GOMEMLIMIT=1288490188", "GOGC=20"}, "inherited_mvdb_env_removed": true,
		"cache_policy": "one private inode; cold before startup and pre_update; no eviction between pre/update/post/census; verified prefix then private truncate+sync+hash+eviction after exit",
		"timing":       "all service stages measured; generation/oracle/decoding/hash/reset/eviction excluded; no invented cross-stage weights",
		"filter_scope": "c9 before update and c8 afterwards each cover all live rows"})
	pre := prepareCases(m, queryCount)
	t := time.Now()
	oracle(m, pre)
	oracleS := time.Since(t).Seconds()
	preDef := emitOracle(m, pre, "initial", oracleS)
	postModel := *m
	postModel.category = "c8"
	post := prepareCases(&postModel, queryCount)
	for i := range post {
		post[i].want = pre[i].want
	}
	postDef := emitOracle(&postModel, post, "updated", 0)
	emit(map[string]any{"phase": "update_definition", "sql": updateSQL, "sql_sha256": digestBytes([]byte(updateSQL)), "affected_rows": len(m.live), "from_category": "c9", "to_category": "c8"})
	path := filepath.Join(*workDir, "db/data.log")
	sourceSignature := fixtureSignature(*fixture)
	sourceHash := hashFile(*fixture)
	sourceID, err := os.Stat(*fixture)
	must(err)
	must(os.Mkdir(filepath.Dir(path), 0755))
	cmd := exec.Command("cp", "--reflink=auto", "--sparse=always", "--", *fixture, path)
	if out, err := cmd.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("private copy failed: %v: %s", err, out))
	}
	privateID, err := os.Stat(path)
	must(err)
	originalBytes := privateID.Size()
	if os.SameFile(sourceID, privateID) || originalBytes != sourceID.Size() || originalBytes < int64(*rows)*int64(*dim)*4 || hashFile(path) != sourceHash || fixtureSignature(*fixture) != sourceSignature {
		panic("private source copy validation failed")
	}
	emit(map[string]any{"phase": "fixture_ready", "private_log": path, "fixture_bytes": originalBytes, "source_sha256": sourceHash, "private_sha256": sourceHash,
		"source_signature": sourceSignature, "private_copies": 1, "distinct_inode_verified": true})
	var st syscall.Stat_t
	must(syscall.Stat(*workDir, &st))
	major := (uint64(st.Dev)>>8)&0xfff | (uint64(st.Dev)>>32)&0xfffff000
	minor := uint64(st.Dev)&0xff | (uint64(st.Dev)>>12)&0xffffff00
	device := fmt.Sprintf("%d:%d", major, minor)
	for round := 0; round < comparisonRounds; round++ {
		order := append([]string(nil), bins...)
		if round%2 == 1 {
			order[0], order[1] = order[1], order[0]
		}
		for position, binary := range order {
			extra := map[string]any{"name": filepath.Base(binary), "binary": binary, "binary_sha256": binaryHashes[binary], "round": round, "position": position}
			info, err := os.Stat(path)
			must(err)
			if !os.SameFile(info, privateID) || info.Size() != originalBytes || fixtureSignature(*fixture) != sourceSignature || hashFile(binary) != binaryHashes[binary] {
				panic("trial did not begin with the same private inode/source/binary")
			}
			trialSignature := fixtureSignature(path)
			emit(merge(extra, map[string]any{"phase": "trial_begin", "private_inode_verified": true, "initial_log_bytes": info.Size()}))
			cache := evictFixture(path, merge(extra, map[string]any{"stage": "before_startup"}))
			c := start(binary, *workDir, device)
			meter := begin(c)
			c.ready()
			requireDeviceIO(c.cg, device)
			meter.finish("startup", 0, merge(extra, map[string]any{"resident_pages_before_startup": cache["resident_pages"]}))
			if fixtureSignature(path) != trialSignature {
				panic("startup modified the restored source fixture")
			}
			cache = evictFixture(path, merge(extra, map[string]any{"stage": "before_pre_update"}))
			preHash := queryBatch(c, pre, "pre_update", merge(extra, map[string]any{"state": "initial", "sql_sha256": preDef["sql_sha256"], "truth_sha256": preDef["truth_sha256"], "cache_state": "cold_at_batch_start", "resident_pages_before_batch": cache["resident_pages"]}))
			if preHash != preDef["expected_response_sha256"] || fixtureSignature(path) != trialSignature {
				panic("pre-update oracle/hash/readonly check failed")
			}
			measuredUpdate(c, len(m.live), extra)
			postHash := queryBatch(c, post, "post_update", merge(extra, map[string]any{"state": "updated", "sql_sha256": postDef["sql_sha256"], "truth_sha256": postDef["truth_sha256"], "cache_state": "same_process_after_update_no_eviction"}))
			if postHash != postDef["expected_response_sha256"] {
				panic("post-update hash differs from independent truth")
			}
			measuredCensus(c, m, postDef["census_expected_response_sha256"].(string), extra)
			meter = begin(c)
			t = time.Now()
			c.stop(false)
			meter.finish("shutdown", time.Since(t).Seconds(), merge(extra, map[string]any{"process_exit_code": c.cmd.ProcessState.ExitCode(), "shutdown_request_id": c.id}))
			c.cleanup()
			restorePrivate(path, sourceID, privateID, originalBytes, liveBytes, sourceHash, extra)
			evictFixture(path, merge(extra, map[string]any{"stage": "after_restore"}))
			if fixtureSignature(*fixture) != sourceSignature || hashFile(binary) != binaryHashes[binary] {
				panic("source/binary changed during trial")
			}
			checkedEmit(merge(extra, map[string]any{"phase": "trial_complete", "reference_bad": 0, "topk_checked": 2 * queryCount, "census_queries_checked": 1,
				"census_rows_checked": len(m.live), "affected_rows_verified": len(m.live), "process_exit_code": 0, "private_restored": true, "source_metadata_unchanged": true, "memory_events": fields(sharedCG + "/memory.events")}))
		}
	}
	if hashFile(path) != sourceHash || hashFile(*fixture) != sourceHash || fixtureSignature(*fixture) != sourceSignature {
		panic("final source/private content verification failed")
	}
	for _, binary := range bins {
		if hashFile(binary) != binaryHashes[binary] {
			panic("binary identity changed")
		}
	}
	checkedEmit(map[string]any{"phase": "success", "reference_bad": 0, "completed_trials": 2 * comparisonRounds, "topk_checked": 4 * comparisonRounds * queryCount,
		"census_queries_checked": 2 * comparisonRounds, "census_rows_checked": 2 * comparisonRounds * len(m.live), "affected_rows_verified": 2 * comparisonRounds * len(m.live),
		"unique_state_query_oracles": 2 * queryCount, "private_copies": 1, "verified_prefix_resets": 2 * comparisonRounds, "source_sha256_unchanged": sourceHash,
		"private_sha256_restored": sourceHash, "source_metadata_unchanged": true, "binary_sha256_unchanged": binaryHashes, "cgroup_reused_across_restarts": true,
		"memory_events": fields(sharedCG + "/memory.events")})
}
