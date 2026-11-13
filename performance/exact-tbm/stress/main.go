// Linux-only, streaming external-protocol stress test. The database alone is
// confined to 2 CPUs / 2 GiB / no swap; generation and the independent scalar
// float64 oracle run in the driver. No full vector table or INSERT corpus is kept.
package main

import (
	"bufio"
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
	if err == nil {
		_, err = f.WriteString("0")
		return f, err == nil
	}
	return nil, false
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
func (m *meter) finish(phase string, elapsed float64, extra map[string]any) {
	m.c.active = nil
	if m.startup {
		elapsed = time.Since(m.start).Seconds()
	}
	m.sample()
	close(m.done)
	m.wg.Wait()
	r, w := ioBytes(m.c.cg, m.c.device)
	peak := number(m.c.cg + "/memory.peak")
	scope := "whole_workflow"
	if m.peakFD != nil {
		_, err := m.peakFD.Seek(0, 0)
		if err == nil {
			b, err := io.ReadAll(m.peakFD)
			if err == nil {
				peak, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
			}
		}
		m.peakFD.Close()
		if m.reset {
			scope = "phase_reset_fd"
		}
	}
	rec := map[string]any{"phase": phase, "t_s": elapsed, "wall_s": time.Since(m.start).Seconds(), "m_bytes": peak, "memory_peak_scope": scope, "sample_period_ms": 50, "sampled_peak": m.peak, "max_sampled_rss": m.maxRSS, "max_sampled_anon": m.maxAnon, "max_sampled_file": m.maxFile, "r_bytes": r - m.r, "w_bytes": w - m.w, "b_bytes": r - m.r + w - m.w, "device": m.c.device, "memory_events": fields(m.c.cg + "/memory.events"), "cpu_stat": fields(m.c.cg + "/cpu.stat"), "pid": m.c.cmd.Process.Pid}
	if m.startup {
		rec["startup_wall_s"] = elapsed
		rec["startup_meter_started_before_exec"] = true
	}
	for k, v := range extra {
		rec[k] = v
	}
	emit(rec)
	if numberEvent := fields(m.c.cg + "/memory.events"); numberEvent["oom"] != 0 || numberEvent["oom_kill"] != 0 {
		panic("cgroup OOM in " + phase)
	}
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
var stderrTag string

func start(binary, dir, device string) *child {
	seq++
	if sharedCG == "" {
		sharedCG = fmt.Sprintf("/sys/fs/cgroup/seekdb-stress-%d", os.Getpid())
		must(os.Mkdir(sharedCG, 0755))
		for k, v := range map[string]string{"cpu.max": "200000 100000", "memory.max": "2147483648", "memory.swap.max": "0", "pids.max": "128"} {
			must(os.WriteFile(sharedCG+"/"+k, []byte(v), 0644))
		}
	}
	cg := sharedCG
	f, err := os.Open(cg)
	must(err)
	defer f.Close()
	ef, err := os.Create(filepath.Join(dir, fmt.Sprintf("stderr-%s%d.txt", stderrTag, seq)))
	must(err)
	cmd := exec.Command(binary, "--data-dir", filepath.Join(dir, "db"), "--sync", "fdatasync", "--index-mem-mb", "256")
	cmd.Env = append(os.Environ(), "GOMAXPROCS=2", "GOMEMLIMIT=1288490188", "GOGC=20")
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
	var r struct {
		ID int `json:"id"`
	}
	must(json.Unmarshal(b, &r))
	if r.ID != c.id {
		panic("response id mismatch")
	}
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
		decode(c.send("", "shutdown"))
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

type rowState struct {
	seed    uint64
	cat     int
	deleted bool
}
type model struct {
	rows, dim int
	overrides map[int]rowState
	bulk      bool
}

func (m *model) row(id int) rowState {
	if r, ok := m.overrides[id]; ok {
		if m.bulk {
			r.cat = 9
		}
		return r
	}
	cat := id % 10
	if m.bulk {
		cat = 9
	}
	return rowState{seed: uint64(id), cat: cat}
}
func (m *model) insert(first, last int) string {
	var b strings.Builder
	b.Grow((last - first + 1) * m.dim * 6)
	b.WriteString("INSERT INTO docs VALUES ")
	v := make([]float32, m.dim)
	for id := first; id <= last; id++ {
		if id > first {
			b.WriteByte(',')
		}
		r := m.row(id)
		vector(v, r.seed)
		fmt.Fprintf(&b, "(%d,'c%d','%s')", id, r.cat, literal(v))
	}
	return b.String()
}

type query struct {
	v   []float32
	cat int
	sql string
}
type hit struct {
	id int
	d  float64
}

func queries(m *model, n int) []query {
	qs := make([]query, n)
	for i := range qs {
		q := query{v: make([]float32, m.dim), cat: -1}
		switch i % 4 {
		case 0:
			vector(q.v, uint64((i*7919)%m.rows+1))
		case 1:
			vector(q.v, uint64(m.rows+i+1000))
		case 2:
			vector(q.v, uint64(m.rows+i+2000))
		case 3: /* zero vector exercises ties */
		}
		where := ""
		if i%2 == 1 {
			q.cat = i % 10
			if m.bulk {
				q.cat = 9
			}
			where = fmt.Sprintf(" WHERE category='c%d'", q.cat)
		}
		q.sql = fmt.Sprintf("SELECT id,L2_DISTANCE(embedding,'%s') AS distance FROM docs%s ORDER BY distance LIMIT 10", literal(q.v), where)
		qs[i] = q
	}
	return qs
}
func oracle(m *model, qs []query) [][]hit {
	truth := make([][]hit, len(qs))
	v := make([]float32, m.dim)
	for id := 1; id <= m.rows; id++ {
		r := m.row(id)
		if r.deleted {
			continue
		}
		vector(v, r.seed)
		for qi, q := range qs {
			if q.cat >= 0 && q.cat != r.cat {
				continue
			}
			var d float64
			for k, x := range v {
				z := float64(x) - float64(q.v[k])
				d += z * z
			}
			best := truth[qi]
			at := sort.Search(len(best), func(i int) bool { return best[i].d > d || (best[i].d == d && best[i].id > id) })
			if at >= 10 {
				continue
			}
			best = append(best, hit{})
			copy(best[at+1:], best[at:len(best)-1])
			best[at] = hit{id, d}
			if len(best) > 10 {
				best = best[:10]
			}
			truth[qi] = best
		}
	}
	return truth
}
func validate(b []byte, want []hit) {
	r := decode(b)
	if len(r.Rows) != len(want) {
		panic("top-k length mismatch")
	}
	for i, h := range want {
		row := r.Rows[i]
		d := math.Sqrt(h.d)
		if len(row) != 2 || int(row[0].(float64)) != h.id || math.Abs(row[1].(float64)-d) > 1e-10*math.Max(1, d) {
			panic(fmt.Sprintf("exact mismatch got=%v want=%+v", r.Rows, want))
		}
	}
}
func queryPhase(c *child, m *model, n int, phase string) {
	qs := queries(m, n)
	start := time.Now()
	truth := oracle(m, qs)
	oracleS := time.Since(start).Seconds()
	meter := begin(c)
	results := make([][]byte, n)
	start = time.Now()
	for i, q := range qs {
		results[i] = c.send(q.sql, "execute")
	}
	elapsed := time.Since(start).Seconds()
	for i, b := range results {
		validate(b, truth[i])
	}
	meter.finish(phase, elapsed, map[string]any{"reference_checked": n, "reference_bad": 0, "oracle_s": oracleS})
}

// Census verifies every visible primary key/category and every vector's norm,
// independently regenerated. It keeps only the scalar protocol response.
func census(c *child, m *model) {
	zero := make([]float32, m.dim)
	meter := begin(c)
	start := time.Now()
	b := c.send(fmt.Sprintf("SELECT id,category,L2_DISTANCE(embedding,'%s') AS distance FROM docs ORDER BY id", literal(zero)), "execute")
	elapsed := time.Since(start).Seconds()
	r := decode(b)
	v := make([]float32, m.dim)
	at := 0
	for id := 1; id <= m.rows; id++ {
		state := m.row(id)
		if state.deleted {
			continue
		}
		if at >= len(r.Rows) {
			panic("missing census row")
		}
		row := r.Rows[at]
		at++
		vector(v, state.seed)
		var d float64
		for _, x := range v {
			d += float64(x) * float64(x)
		}
		d = math.Sqrt(d)
		if len(row) != 3 || int(row[0].(float64)) != id || row[1].(string) != fmt.Sprintf("c%d", state.cat) || math.Abs(row[2].(float64)-d) > 1e-10*math.Max(1, d) {
			panic(fmt.Sprintf("census mismatch id=%d got=%v", id, row))
		}
	}
	if at != len(r.Rows) {
		panic("unexpected census rows")
	}
	meter.finish("final_full_census", elapsed, map[string]any{"reference_rows_checked": at, "reference_bad": 0})
}
func update(c *child, m *model, id int, seed uint64, cat int) {
	v := make([]float32, m.dim)
	vector(v, seed)
	c.sql(fmt.Sprintf("UPDATE docs SET embedding='%s',category='c%d' WHERE id=%d", literal(v), cat, id))
}

// A zero distance to the independently regenerated vector checks every
// coordinate of a point row, while also checking transaction visibility.
func checkPoint(c *child, dim, id int, want rowState) {
	v := make([]float32, dim)
	vector(v, want.seed)
	// Use the exact TopK path so the fixture is visited in log order. The
	// generic scalar SELECT path walks the row map and can make a point check
	// disproportionately expensive when the log exceeds the memory limit.
	r := c.sql(fmt.Sprintf("SELECT id,category,L2_DISTANCE(embedding,'%s') AS distance FROM docs WHERE id=%d ORDER BY distance LIMIT 1", literal(v), id))
	if want.deleted {
		if len(r.Rows) != 0 {
			panic(fmt.Sprintf("deleted point %d is visible: %v", id, r.Rows))
		}
		return
	}
	if len(r.Rows) != 1 || len(r.Rows[0]) != 3 {
		panic(fmt.Sprintf("missing point %d: %v", id, r.Rows))
	}
	row := r.Rows[0]
	if int(row[0].(float64)) != id || row[1].(string) != fmt.Sprintf("c%d", want.cat) || row[2].(float64) != 0 {
		panic(fmt.Sprintf("point mismatch %d: %v", id, row))
	}
}

func completedBasicModel(rows, dim int) *model {
	m := &model{rows: rows, dim: dim, overrides: map[int]rowState{}}
	for id := 1; id <= 16; id++ {
		m.overrides[id] = rowState{seed: uint64(rows + id + 100000), cat: (id + 3) % 10}
	}
	for _, id := range []int{rows / 3, rows / 2, rows - 1} {
		state := m.row(id)
		state.deleted = true
		m.overrides[id] = state
	}
	m.overrides[18] = rowState{seed: 1000018, cat: 4}
	return m
}

// This changes only the named fixture's cache residency. It never writes to
// drop_caches or reclaims unrelated files. mincore checks that fadvise actually
// removed the data pages before the next measured process starts.
func evictFixture(path string) map[string]any {
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
	emit(rec)
	// A partial last page may remain. More means this run is not a cold-file
	// comparison and must not silently enter the results.
	if n > 1 {
		panic(fmt.Sprintf("fixture still has %d resident pages after fadvise", n))
	}
	return rec
}

func recoveryComparison(dir, device string, rows, dim, rounds int, names []string) {
	if rounds < 1 {
		panic("recovery-rounds must be positive")
	}
	for _, name := range names {
		if !filepath.IsAbs(name) {
			panic("recovery-bins requires absolute executable paths")
		}
		st, err := os.Stat(name)
		must(err)
		if !st.Mode().IsRegular() {
			panic("recovery binary is not a regular file: " + name)
		}
	}
	path := filepath.Join(dir, "db", "data.log")
	st, err := os.Stat(path)
	must(err)
	if !st.Mode().IsRegular() || st.Size() < int64(rows)*int64(dim)*4 {
		panic("recovery fixture is missing or too small for the stated rows/dim")
	}
	stderrTag = fmt.Sprintf("recovery-%d-", os.Getpid())
	m := completedBasicModel(rows, dim)
	qs := queries(m, 2)
	truth := oracle(m, qs)
	// The first recovery may discard an uncommitted tail left by an interrupted
	// diagnostic run. It is explicitly excluded from the cold comparisons.
	c := start(names[0], dir, device)
	defer c.cleanup()
	meter := begin(c)
	c.ready()
	meter.finish("recovery_warmup", 0, map[string]any{"binary": names[0], "comparison": false})
	for _, id := range []int{1, 16, 18, 19, rows / 3, rows / 2, rows - 1, rows} {
		checkPoint(c, dim, id, m.row(id))
	}
	checkPoint(c, dim, rows+1, rowState{deleted: true})
	meter = begin(c)
	t := time.Now()
	c.stop(false)
	meter.finish("recovery_warmup_shutdown", time.Since(t).Seconds(), map[string]any{"binary": names[0], "comparison": false})
	c.cleanup()
	fixtureInfo, err := os.Stat(path)
	must(err)
	fixtureBytes := fixtureInfo.Size()
	checked := 0
	for round := 0; round < rounds; round++ {
		order := append([]string(nil), names...)
		if round%2 == 1 {
			for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
				order[i], order[j] = order[j], order[i]
			}
		}
		for position, name := range order {
			cache := evictFixture(path)
			c = start(name, dir, device)
			defer c.cleanup()
			meter = begin(c)
			c.ready()
			requireDeviceIO(c.cg, device)
			meter.finish("recovery_compare", 0, map[string]any{"mode": "recovery_interleaved", "binary": name, "name": filepath.Base(name), "round": round, "position": position, "comparison": true, "fixture_bytes": fixtureBytes, "resident_pages_before_start": cache["resident_pages"]})
			meter = begin(c)
			responses := make([][]byte, len(qs))
			t = time.Now()
			for i, q := range qs {
				responses[i] = c.send(q.sql, "execute")
			}
			elapsed := time.Since(t).Seconds()
			for i, b := range responses {
				validate(b, truth[i])
			}
			checked += len(qs)
			meter.finish("recovery_compare_queries", elapsed, map[string]any{"binary": name, "name": filepath.Base(name), "round": round, "position": position, "reference_checked": len(qs), "reference_bad": 0})
			meter = begin(c)
			t = time.Now()
			c.stop(false)
			meter.finish("recovery_compare_shutdown", time.Since(t).Seconds(), map[string]any{"binary": name, "name": filepath.Base(name), "round": round, "position": position})
			c.cleanup()
			after, err := os.Stat(path)
			must(err)
			if after.Size() != fixtureBytes {
				panic("read-only recovery comparison changed fixture size")
			}
		}
	}
	emit(map[string]any{"phase": "recovery_comparison_success", "mode": "recovery_interleaved", "rounds": rounds, "binaries": names, "reference_checked": checked, "reference_bad": 0, "fixture_bytes": fixtureBytes, "whole_workflow_m_bytes": number(sharedCG + "/memory.peak"), "memory_events": fields(sharedCG + "/memory.events"), "cgroup_reused_across_restarts": true})
}

func main() {
	binary := flag.String("binary", "/tmp/seekdb-official-go-bin/release-safe", "database executable")
	dir := flag.String("data-dir", "", "fresh test directory (retained with stderr and database)")
	rows := flag.Int("rows", 550000, "streamed row count; default raw vectors exceed 2 GiB")
	dim := flag.Int("dim", 1024, "vector dimensions")
	nq := flag.Int("queries", 12, "independent oracle queries per state")
	batch := flag.Int("batch", 64, "rows per INSERT")
	bulk := flag.Bool("bulk-update", false, "after the basic workflow, run one all-row UPDATE and another crash recovery")
	killDelay := flag.Duration("recovery-kill-delay", 10*time.Millisecond, "delay before SIGKILL during recovery")
	recoveryBins := flag.String("recovery-bins", "", "comma-separated absolute binaries: compare cold recovery on an existing completed-basic fixture")
	recoveryRounds := flag.Int("recovery-rounds", 3, "alternating forward/reverse recovery comparison rounds")
	flag.Parse()
	if *rows < 100 || *dim < 1 || *nq < 1 || *batch < 1 {
		panic("rows>=100 and positive dim/queries/batch required")
	}
	must(os.WriteFile("/sys/fs/cgroup/cgroup.subtree_control", []byte("+cpu +memory +io +pids"), 0644))
	// Keep the cgroup across restarts. Its page-cache charges must not escape
	// to the parent when the first process dies and a new process maps the log.
	defer func() {
		if sharedCG != "" {
			os.Remove(sharedCG)
		}
	}()
	defer func() {
		if problem := recover(); problem != nil {
			rec := map[string]any{"phase": "failure", "error": fmt.Sprint(problem), "data_dir": *dir}
			if sharedCG != "" {
				rec["memory_events"] = fields(sharedCG + "/memory.events")
				rec["whole_workflow_m_bytes"] = number(sharedCG + "/memory.peak")
				rec["memory_current"] = number(sharedCG + "/memory.current")
			}
			if currentChild != nil {
				rec["stderr"] = currentChild.errfile.Name()
			}
			emit(rec)
			if currentChild != nil && currentChild.active != nil {
				func() {
					defer func() { _ = recover() }()
					m := currentChild.active
					m.finish("failed_phase", time.Since(m.start).Seconds(), map[string]any{"error": fmt.Sprint(problem)})
				}()
			}
			panic(problem)
		}
	}()
	if *dir == "" {
		if *recoveryBins != "" {
			panic("recovery comparison requires an explicit existing -data-dir")
		}
		d, err := os.MkdirTemp("/tmp", "seekdb-stress-")
		must(err)
		*dir = d
	} else {
		if *recoveryBins != "" {
			info, err := os.Stat(*dir)
			must(err)
			if !info.IsDir() {
				panic("recovery data-dir is not a directory")
			}
		} else {
			must(os.MkdirAll(*dir, 0755))
		}
	}
	if _, err := os.Stat(filepath.Join(*dir, "db")); err == nil && *recoveryBins == "" {
		panic("data-dir/db already exists; use a fresh test directory")
	}
	var st syscall.Stat_t
	must(syscall.Stat(*dir, &st))
	major := (uint64(st.Dev)>>8)&0xfff | (uint64(st.Dev)>>32)&0xfffff000
	minor := uint64(st.Dev)&0xff | (uint64(st.Dev)>>12)&0xffffff00
	device := fmt.Sprintf("%d:%d", major, minor)
	raw := int64(*rows) * int64(*dim) * 4
	if *recoveryBins != "" {
		if *bulk {
			panic("bulk-update cannot be combined with recovery-bins")
		}
		names := strings.Split(*recoveryBins, ",")
		emit(map[string]any{"phase": "config", "mode": "recovery_interleaved", "data_dir": *dir, "rows": *rows, "dim": *dim, "raw_vector_bytes": raw, "binaries": names, "rounds": *recoveryRounds, "queries_per_recovery": 2, "memory_limit": int64(2 << 30), "memory_swap_max": 0, "cpu_max": "200000 100000", "env": []string{"GOMAXPROCS=2", "GOMEMLIMIT=1288490188", "GOGC=20"}, "durability": "explicit fdatasync", "index_mem_mb": 256, "cgroup_reused_across_restarts": true, "cache_policy": "per-file Sync/FADV_DONTNEED with mincore verification"})
		recoveryComparison(*dir, device, *rows, *dim, *recoveryRounds, names)
		return
	}
	emit(map[string]any{"phase": "config", "binary": *binary, "data_dir": *dir, "rows": *rows, "dim": *dim, "raw_vector_bytes": raw, "exceeds_2gib": raw > 2<<30, "queries_per_state": *nq, "batch": *batch, "device": device, "memory_limit": int64(2 << 30), "cpu_max": "200000 100000", "memory_swap_max": 0, "env": []string{"GOMAXPROCS=2", "GOMEMLIMIT=1288490188", "GOGC=20"}, "generator": "row-local xorshift signed uint8/16", "durability": "explicit fdatasync", "index_mem_mb": 256, "bulk_update": *bulk})
	m := &model{rows: *rows, dim: *dim, overrides: map[int]rowState{}}
	c := start(*binary, *dir, device)
	defer c.cleanup()
	startup := begin(c)
	t := time.Now()
	c.ready()
	startup.finish("initial_startup", time.Since(t).Seconds(), nil)
	c.sql(fmt.Sprintf("CREATE TABLE docs(id BIGINT PRIMARY KEY,category VARCHAR(16),embedding VECTOR(%d))", *dim))
	meter := begin(c)
	var measured float64
	for first := 1; first <= *rows; first += *batch {
		last := min(first+*batch-1, *rows)
		sql := m.insert(first, last)
		t = time.Now()
		b := c.send(sql, "execute")
		measured += time.Since(t).Seconds()
		decode(b)
		if last%(*batch*500) == 0 || last == *rows {
			emit(map[string]any{"phase": "write_progress", "rows_acknowledged": last, "raw_vector_bytes": int64(last) * int64(*dim) * 4, "memory_current": number(c.cg + "/memory.current"), "memory_peak": number(c.cg + "/memory.peak")})
		}
	}
	logInfo, err := os.Stat(filepath.Join(*dir, "db", "data.log"))
	must(err)
	requireDeviceIO(c.cg, c.device)
	meter.finish("write", measured, map[string]any{"rows_acknowledged": *rows, "raw_vector_bytes": raw, "log_bytes": logInfo.Size(), "generator_excluded_from_t": true})
	queryPhase(c, m, *nq, "query_after_write")
	meter = begin(c)
	t = time.Now()
	// Committed replacements, deletes, and a rollback change the oracle model
	// only after the relevant acknowledgement has been received.
	for id := 1; id <= min(16, *rows); id++ {
		seed := uint64(*rows + id + 100000)
		cat := (id + 3) % 10
		update(c, m, id, seed, cat)
		m.overrides[id] = rowState{seed: seed, cat: cat}
	}
	for _, id := range []int{*rows / 3, *rows / 2, *rows - 1} {
		c.sql(fmt.Sprintf("DELETE FROM docs WHERE id=%d", id))
		r := m.row(id)
		r.deleted = true
		m.overrides[id] = r
	}
	c.sql("BEGIN")
	update(c, m, 17, 999999, 8)
	c.sql(fmt.Sprintf("DELETE FROM docs WHERE id=%d", *rows))
	c.sql("ROLLBACK")
	c.sql("BEGIN")
	update(c, m, 18, 1000018, 4)
	c.sql("COMMIT")
	m.overrides[18] = rowState{seed: 1000018, cat: 4}
	meter.finish("updates_deletes_rollback", time.Since(t).Seconds(), map[string]any{"updates": 17, "deletes": 3, "rollback_checked_later": true})
	queryPhase(c, m, *nq, "query_after_updates")
	meter = begin(c)
	t = time.Now()
	c.stop(true)
	meter.finish("kill_after_acknowledged_commits", time.Since(t).Seconds(), map[string]any{"signal": "SIGKILL"})
	c.cleanup()
	c = start(*binary, *dir, device)
	defer c.cleanup()
	meter = begin(c)
	t = time.Now()
	c.ready()
	meter.finish("recovery_after_commit_kill", time.Since(t).Seconds(), nil)
	queryPhase(c, m, *nq, "query_after_commit_recovery")
	committedInfo, err := os.Stat(filepath.Join(*dir, "db", "data.log"))
	must(err)
	committedEnd := committedInfo.Size()
	c.sql("BEGIN")
	update(c, m, 19, 777777, 7)
	c.sql(fmt.Sprintf("DELETE FROM docs WHERE id=%d", *rows))
	v := make([]float32, *dim)
	vector(v, 888888)
	c.sql(fmt.Sprintf("INSERT INTO docs VALUES (%d,'c8','%s')", *rows+1, literal(v)))
	// These three mutations are smaller than the log's 4 MiB write buffer.
	// A TopK scan flushes that buffer to the file without committing or syncing
	// this transaction. Stat must then prove that recovery has a real tail to
	// discard, rather than merely losing a userspace buffer on SIGKILL.
	txnModel := &model{rows: *rows + 1, dim: *dim, overrides: make(map[int]rowState, len(m.overrides)+3)}
	for id, state := range m.overrides {
		txnModel.overrides[id] = state
	}
	txnModel.overrides[19] = rowState{seed: 777777, cat: 7}
	deletedLast := m.row(*rows)
	deletedLast.deleted = true
	txnModel.overrides[*rows] = deletedLast
	txnModel.overrides[*rows+1] = rowState{seed: 888888, cat: 8}
	flushQuery := query{v: v, cat: -1, sql: fmt.Sprintf("SELECT id,L2_DISTANCE(embedding,'%s') AS distance FROM docs ORDER BY distance LIMIT 10", literal(v))}
	flushTruth := oracle(txnModel, []query{flushQuery})
	meter = begin(c)
	t = time.Now()
	validate(c.send(flushQuery.sql, "execute"), flushTruth[0])
	for _, id := range []int{19, *rows, *rows + 1} {
		checkPoint(c, *dim, id, txnModel.row(id))
	}
	flushedInfo, err := os.Stat(filepath.Join(*dir, "db", "data.log"))
	must(err)
	uncommittedTail := flushedInfo.Size() - committedEnd
	if uncommittedTail <= 0 {
		panic("uncommitted transaction did not reach the log file")
	}
	meter.finish("uncommitted_tail_flushed", time.Since(t).Seconds(), map[string]any{"committed_log_bytes": committedEnd, "log_bytes": flushedInfo.Size(), "uncommitted_tail_bytes": uncommittedTail, "reference_checked": 1, "point_rows_checked": 3, "reference_bad": 0, "transaction_committed": false})
	meter = begin(c)
	t = time.Now()
	c.stop(true)
	meter.finish("kill_with_uncommitted_transaction", time.Since(t).Seconds(), map[string]any{"signal": "SIGKILL"})
	c.cleanup()
	c = start(*binary, *dir, device)
	defer c.cleanup()
	meter = begin(c)
	t = time.Now()
	ready := make(chan bool, 1)
	recoveryReader := c.rd
	go func() {
		b, err := recoveryReader.ReadBytes('\n')
		var r response
		if err == nil {
			json.Unmarshal(b, &r)
		}
		ready <- err == nil && r.Type == "ready"
	}()
	time.Sleep(*killDelay)
	beforeReady := true
	readyReceived := false
	select {
	case ok := <-ready:
		beforeReady = !ok
		readyReceived = true
	default:
	}
	c.stop(true)
	// Even if delivery was delayed, a complete ready line means recovery
	// already finished before the kill and must not count as an interruption.
	if !readyReceived {
		beforeReady = !(<-ready)
	}
	meter.finish("kill_during_recovery", time.Since(t).Seconds(), map[string]any{"signal": "SIGKILL", "killed_before_ready": beforeReady, "delay_ms": killDelay.Milliseconds()})
	c.cleanup()
	if raw > 2<<30 && !beforeReady {
		panic("large recovery kill missed recovery window")
	}
	c = start(*binary, *dir, device)
	defer c.cleanup()
	meter = begin(c)
	t = time.Now()
	c.ready()
	meter.finish("final_recovery", time.Since(t).Seconds(), nil)
	recoveredInfo, err := os.Stat(filepath.Join(*dir, "db", "data.log"))
	must(err)
	if recoveredInfo.Size() != committedEnd {
		panic(fmt.Sprintf("uncommitted tail was not truncated: got %d want %d", recoveredInfo.Size(), committedEnd))
	}
	checkPoint(c, *dim, 19, m.row(19))
	checkPoint(c, *dim, *rows, m.row(*rows))
	checkPoint(c, *dim, *rows+1, rowState{deleted: true})
	emit(map[string]any{"phase": "uncommitted_tail_discarded", "committed_log_bytes": committedEnd, "recovered_log_bytes": recoveredInfo.Size(), "discarded_tail_bytes": uncommittedTail, "point_rows_checked": 3, "reference_bad": 0})
	queryPhase(c, m, *nq, "query_after_final_recovery")
	census(c, m)
	checked := 4**nq + 1
	emit(map[string]any{"phase": "basic_workflow_success", "reference_checked": checked, "reference_bad": 0, "full_census_checked": true, "raw_vector_bytes": raw, "whole_workflow_m_bytes": number(c.cg + "/memory.peak"), "memory_events": fields(c.cg + "/memory.events"), "cgroup_reused_across_restarts": true})
	if *bulk {
		meter = begin(c)
		t = time.Now()
		r := c.sql("UPDATE docs SET category='c9'")
		m.bulk = true
		if r.Affected != int64(*rows-3) {
			panic("bulk update affected_rows mismatch")
		}
		meter.finish("bulk_update", time.Since(t).Seconds(), map[string]any{"affected_rows": r.Affected})
		queryPhase(c, m, *nq, "query_after_bulk_update")
		meter = begin(c)
		t = time.Now()
		c.stop(true)
		meter.finish("kill_after_bulk_commit", time.Since(t).Seconds(), map[string]any{"signal": "SIGKILL"})
		c.cleanup()
		c = start(*binary, *dir, device)
		defer c.cleanup()
		meter = begin(c)
		t = time.Now()
		c.ready()
		meter.finish("recovery_after_bulk_commit", time.Since(t).Seconds(), nil)
		queryPhase(c, m, *nq, "query_after_bulk_recovery")
		census(c, m)
		checked += 2 * *nq
	}
	meter = begin(c)
	t = time.Now()
	c.stop(false)
	meter.finish("graceful_shutdown", time.Since(t).Seconds(), nil)
	c.cleanup()
	logInfo, err = os.Stat(filepath.Join(*dir, "db", "data.log"))
	must(err)
	emit(map[string]any{"phase": "success", "raw_vector_bytes": raw, "exceeds_2gib": raw > 2<<30, "log_bytes": logInfo.Size(), "reference_checked": checked, "reference_bad": 0, "full_census_checked": true, "data_dir": *dir, "whole_workflow_m_bytes": number(c.cg + "/memory.peak"), "memory_events": fields(c.cg + "/memory.events"), "cgroup_reused_across_restarts": true})
}
