// Linux-only generic SQL comparison. The database alone is
// confined to 2 CPUs / 2 GiB / no swap; independent SQL truth runs in the
// driver. See README.md for fixture, cold-cache and timing definitions.
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
func (m *meter) snapshot(phase string, elapsed float64, extra map[string]any) map[string]any {
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
	return rec
}

func checkedEmit(rec map[string]any) {
	emit(rec)
	events := rec["memory_events"].(map[string]int64)
	if events["oom"] != 0 || events["oom_kill"] != 0 {
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
var stderrTag string

func start(binary, dir, device string) *child {
	seq++
	if sharedCG == "" {
		sharedCG = fmt.Sprintf("/sys/fs/cgroup/seekdb-sqlcompare-%d", os.Getpid())
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

// finalModel describes the completed ROUND3 fixture independently of any
// query response: 16 replacements, one committed transaction replacement,
// three deletions, then a committed UPDATE setting every category to c9.
// Rolled-back and killed uncommitted changes are absent.
type finalModel struct {
	rows, dim int
	live      []int
	deleted   map[int]bool
}

func makeModel(rows, dim int) *finalModel {
	m := &finalModel{rows: rows, dim: dim, deleted: map[int]bool{rows / 3: true, rows / 2: true, rows - 1: true}}
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

type truthRow struct {
	id       int
	distance float64
}

func (m *finalModel) normRow(id int) truthRow {
	v := make([]float32, m.dim)
	vector(v, m.seed(id))
	var squared float64
	for _, x := range v {
		squared += float64(x) * float64(x)
	}
	return truthRow{id: id, distance: math.Sqrt(squared)}
}

type sqlCase struct {
	label string
	sql   string
	want  []truthRow
}

func (m *finalModel) point(id int, suffix, label string, falseResidual bool) sqlCase {
	v := make([]float32, m.dim)
	vector(v, m.seed(id))
	q := sqlCase{label: label, sql: fmt.Sprintf("SELECT id,category,L2_DISTANCE(embedding,'%s') AS distance FROM docs WHERE id=%d%s", literal(v), id, suffix)}
	if id >= 1 && id <= m.rows && !m.deleted[id] && !falseResidual {
		q.want = []truthRow{{id: id, distance: 0}}
	}
	return q
}

func (m *finalModel) ordered(label, where, order string, offset, limit int, minimumID int, descending bool) sqlCase {
	q := sqlCase{label: label, sql: fmt.Sprintf("SELECT id,category,L2_DISTANCE(embedding,'%s') AS distance FROM docs%s%s", literal(make([]float32, m.dim)), where, order)}
	if limit >= 0 {
		q.sql += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	}
	ids := m.live
	seen := 0
	for i := 0; i < len(ids); i++ {
		at := i
		if descending {
			at = len(ids) - 1 - i
		}
		id := ids[at]
		if id < minimumID {
			continue
		}
		if seen < offset {
			seen++
			continue
		}
		if limit >= 0 && len(q.want) >= limit {
			break
		}
		q.want = append(q.want, m.normRow(id))
	}
	return q
}

func cases(m *finalModel, suite string, extended bool) []sqlCase {
	switch suite {
	case "point":
		out := []sqlCase{m.point(18, "", "committed_replacement", false)}
		if extended {
			out = append(out,
				m.point(1, "", "first_replacement", false),
				m.point(17, "", "rolled_back_replacement", false),
				m.point(19, "", "killed_replacement", false),
				m.point(m.rows, "", "rolled_back_delete", false),
				m.point(m.rows/3, "", "committed_delete", false),
				m.point(m.rows+1, "", "killed_insert", false),
				m.point(18, " AND category='c9'", "true_residual", false),
				m.point(18, " AND category='c0'", "false_residual", true))
		}
		return out
	case "order":
		out := []sqlCase{m.ordered("ascending_middle_offset", "", " ORDER BY id", len(m.live)/2, 10, 1, false)}
		if extended {
			out = append(out,
				m.ordered("ascending_first", "", " ORDER BY id", 0, 10, 1, false),
				m.ordered("descending_offset", "", " ORDER BY id DESC", 7, 10, 1, true),
				m.ordered("implicit_primary_order", "", "", len(m.live)-15, 10, 1, false),
				m.ordered("filtered_descending", " WHERE category='c9' AND id>=17", " ORDER BY id DESC", 3, 7, 17, true),
				m.ordered("ordinal_descending", "", " ORDER BY 1 DESC", 5, 10, 1, true),
				m.ordered("offset_past_end", "", " ORDER BY id", len(m.live)+10, 10, 1, false),
				m.ordered("zero_limit", "", " ORDER BY id", 0, 0, 1, false))
		}
		return out
	case "census":
		return []sqlCase{m.ordered("all_ids_categories_norms", "", " ORDER BY id", 0, -1, 1, false)}
	default:
		panic("unknown suite: " + suite)
	}
}

func validateCase(b []byte, id int, q sqlCase) int {
	r := decode(b)
	if r.ID != id || len(r.Rows) != len(q.want) {
		panic(fmt.Sprintf("%s: response id/count got %d/%d want %d/%d", q.label, r.ID, len(r.Rows), id, len(q.want)))
	}
	for i, want := range q.want {
		row := r.Rows[i]
		if len(row) != 3 || row[0].(float64) != float64(want.id) || row[1].(string) != "c9" {
			panic(fmt.Sprintf("%s row %d: got %v want id=%d category=c9", q.label, i, row, want.id))
		}
		d := row[2].(float64)
		if math.IsNaN(d) || math.IsInf(d, 0) || (want.distance == 0 && d != 0) || math.Abs(d-want.distance) > 1e-12*math.Max(1, want.distance) {
			panic(fmt.Sprintf("%s id=%d: distance got %.17g want %.17g", q.label, want.id, d, want.distance))
		}
	}
	return len(q.want)
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

// Small fixtures encode the same final logical model directly. Preparation is
// excluded from every query comparison; it is not a write-performance result.
func buildSmallFixture(binary, dir, device string, m *finalModel) {
	c := start(binary, dir, device)
	defer c.cleanup()
	c.ready()
	c.sql(fmt.Sprintf("CREATE TABLE docs(id BIGINT PRIMARY KEY,category VARCHAR(16),embedding VECTOR(%d))", m.dim))
	v := make([]float32, m.dim)
	for from := 0; from < len(m.live); from += 64 {
		var b strings.Builder
		b.WriteString("INSERT INTO docs VALUES ")
		for j, id := range m.live[from:min(from+64, len(m.live))] {
			if j > 0 {
				b.WriteByte(',')
			}
			vector(v, m.seed(id))
			fmt.Fprintf(&b, "(%d,'c9','%s')", id, literal(v))
		}
		c.sql(b.String())
	}
	c.stop(false)
	emit(map[string]any{"phase": "fixture_built", "comparison": false, "rows": m.rows, "live_rows": len(m.live), "dim": m.dim, "binary": binary})
}

func main() {
	binList := flag.String("bins", "", "comma-separated absolute database executable paths, reference first")
	fixture := flag.String("fixture", "", "ROUND3 final data.log; only a private copy is opened by a database")
	workDir := flag.String("work-dir", "", "new private directory; defaults to a fresh /tmp directory")
	rows := flag.Int("rows", 10000, "original row count before the three committed deletes")
	dim := flag.Int("dim", 128, "vector dimension; existing final fixture uses 1024")
	rounds := flag.Int("rounds", 3, "interleaved rounds for point and order suites")
	censusRounds := flag.Int("census-rounds", 1, "census diagnostic rounds, 0 disables")
	suitesFlag := flag.String("suites", "point,order,census", "point,order,census, or a comma-separated subset")
	profile := flag.String("profile", "auto", "auto: extended cases for generated small fixtures, minimal for existing fixture; or extended/minimal")
	flag.Parse()
	if *rows < 100 || *dim < 1 || *rounds < 1 || *censusRounds < 0 || *binList == "" {
		panic("require --bins, rows>=100, dim>0, rounds>0 and census-rounds>=0")
	}
	names := strings.Split(*binList, ",")
	seenNames := map[string]bool{}
	for _, name := range names {
		if !filepath.IsAbs(name) || seenNames[filepath.Base(name)] {
			panic("binaries must have absolute paths and distinct basenames")
		}
		seenNames[filepath.Base(name)] = true
		st, err := os.Stat(name)
		must(err)
		if !st.Mode().IsRegular() {
			panic("binary is not a regular file: " + name)
		}
	}
	extended := *fixture == ""
	if *profile != "auto" {
		if *profile != "extended" && *profile != "minimal" {
			panic("profile must be auto, extended or minimal")
		}
		extended = *profile == "extended"
	}
	if *workDir == "" {
		d, err := os.MkdirTemp("/tmp", "seekdb-sqlcompare-")
		must(err)
		*workDir = d
	} else {
		if _, err := os.Stat(*workDir); !os.IsNotExist(err) {
			panic("work-dir must not exist; use a fresh private directory")
		}
		must(os.MkdirAll(*workDir, 0755))
	}
	must(os.WriteFile("/sys/fs/cgroup/cgroup.subtree_control", []byte("+cpu +memory +io +pids"), 0644))
	defer func() {
		if sharedCG != "" {
			os.Remove(sharedCG)
		}
	}()
	defer func() {
		if problem := recover(); problem != nil {
			emit(map[string]any{"phase": "failure", "error": fmt.Sprint(problem), "work_dir": *workDir, "memory_events": fields(sharedCG + "/memory.events")})
			if currentChild != nil && currentChild.active != nil {
				func() {
					defer func() { _ = recover() }()
					meter := currentChild.active
					meter.finish("failed_phase", time.Since(meter.start).Seconds(), map[string]any{"error": fmt.Sprint(problem)})
				}()
			}
			if currentChild != nil {
				currentChild.cleanup()
			}
			panic(problem)
		}
	}()
	var st syscall.Stat_t
	must(syscall.Stat(*workDir, &st))
	major := (uint64(st.Dev)>>8)&0xfff | (uint64(st.Dev)>>32)&0xfffff000
	minor := uint64(st.Dev)&0xff | (uint64(st.Dev)>>12)&0xffffff00
	device := fmt.Sprintf("%d:%d", major, minor)
	m := makeModel(*rows, *dim)
	suites := strings.Split(*suitesFlag, ",")
	prepared := map[string][]sqlCase{}
	for _, suite := range suites {
		if _, exists := prepared[suite]; exists {
			panic("duplicate suite: " + suite)
		}
		if suite == "census" && *censusRounds == 0 {
			continue
		}
		prepared[suite] = cases(m, suite, extended)
	}
	emit(map[string]any{"phase": "config", "mode": "generic_sql_interleaved", "bins": names, "fixture_source": *fixture, "work_dir": *workDir, "rows": *rows, "live_rows": len(m.live), "dim": *dim, "fixture_model": "round3_final_all_c9", "raw_vector_bytes": int64(*rows) * int64(*dim) * 4, "rounds": *rounds, "census_rounds": *censusRounds, "extended_cases": extended, "suites": suites, "memory_limit": int64(2 << 30), "cpu_max": "200000 100000", "memory_swap_max": 0, "pids_max": 128, "env": []string{"GOMAXPROCS=2", "GOMEMLIMIT=1288490188", "GOGC=20"}, "sync": "fdatasync", "index_mem_mb": 256, "cache_policy": "private copy; Sync/FADV_DONTNEED/mincore before startup and again after ready before each SQL group", "timing": "startup from before exec; query protocol/output only; truth generation and response decode outside query T", "census_validation": "all primary keys, c9 categories, independent vector norms; not elementwise vectors", "census_diagnostic": true})
	path := filepath.Join(*workDir, "db", "data.log")
	sourceSignature := ""
	if *fixture != "" {
		sourceSignature = fixtureSignature(*fixture)
		must(os.Mkdir(filepath.Dir(path), 0755))
		// cp creates a distinct inode even where reflinks are supported. File
		// pages belong to this private inode, never the original fixture.
		cmd := exec.Command("cp", "--reflink=auto", "--sparse=always", "--", *fixture, path)
		if out, err := cmd.CombinedOutput(); err != nil {
			panic(fmt.Sprintf("fixture copy: %v: %s", err, out))
		}
		a, err := os.Stat(*fixture)
		must(err)
		b, err := os.Stat(path)
		must(err)
		if os.SameFile(a, b) || b.Size() < int64(*rows)*int64(*dim)*4 {
			panic("fixture must be a private copy with enough bytes for the stated model")
		}
	} else {
		buildSmallFixture(names[0], *workDir, device, m)
	}
	signature := fixtureSignature(path)
	fixtureHash := hashFile(path)
	emit(map[string]any{"phase": "fixture_ready", "comparison": false, "private_log": path, "sha256": fixtureHash, "signature": signature, "source_signature": sourceSignature})
	checkedRows, checkedQueries := 0, 0
	for _, suite := range suites {
		qs := prepared[suite]
		if len(qs) == 0 {
			continue
		}
		nrounds := *rounds
		if suite == "census" {
			nrounds = *censusRounds
		}
		var labels, statements []string
		for _, q := range qs {
			labels, statements = append(labels, q.label), append(statements, q.sql)
		}
		sqlHash := sha256.Sum256([]byte(strings.Join(statements, "\n")))
		emit(map[string]any{"phase": "suite_definition", "suite": suite, "labels": labels, "statements": statements, "sql_sha256": fmt.Sprintf("%x", sqlHash), "rounds": nrounds, "census_diagnostic": suite == "census"})
		for round := 0; round < nrounds; round++ {
			order := append([]string(nil), names...)
			if round%2 == 1 {
				for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
					order[i], order[j] = order[j], order[i]
				}
			}
			for position, binary := range order {
				extra := map[string]any{"suite": suite, "name": filepath.Base(binary), "binary": binary, "round": round, "position": position, "rows": *rows, "dim": *dim, "queries": len(qs), "sql_sha256": fmt.Sprintf("%x", sqlHash), "comparison": suite != "census", "diagnostic": suite == "census"}
				if fixtureSignature(path) != signature {
					panic("private fixture changed before trial")
				}
				evictFixture(path)
				c := start(binary, *workDir, device)
				meter := begin(c)
				c.ready()
				requireDeviceIO(c.cg, device)
				meter.finish(suite+"_startup", 0, extra)
				// Startup reads the log. Cold SQL means cold row pages after
				// recovery as well, not merely a cold pre-start fixture.
				cache := evictFixture(path)
				meter = begin(c)
				responses := make([][]byte, len(qs))
				t := time.Now()
				for i, q := range qs {
					responses[i] = c.send(q.sql, "execute")
				}
				elapsed := time.Since(t).Seconds()
				// Freeze query metrics before external validation; peak and
				// I/O cover execution/output, not oracle CPU time afterwards.
				record := meter.snapshot(suite+"_query", elapsed, merge(extra, map[string]any{"resident_pages_before_query": cache["resident_pages"]}))
				runRows := 0
				for i, b := range responses {
					runRows += validateCase(b, i+1, qs[i])
				}
				checkedRows += runRows
				checkedQueries += len(qs)
				record["reference_bad"], record["reference_checked"], record["reference_rows_checked"] = 0, len(qs), runRows
				checkedEmit(record)
				emit(merge(extra, map[string]any{"phase": "query_validation", "reference_bad": 0, "reference_checked": len(qs), "reference_rows_checked": runRows, "validation": "independent generator, exact primary keys/categories, zero distance or scalar norm"}))
				meter = begin(c)
				t = time.Now()
				c.stop(false)
				meter.finish(suite+"_shutdown", time.Since(t).Seconds(), extra)
				c.cleanup()
				if fixtureSignature(path) != signature {
					panic("read-only SQL trial changed private fixture bytes or modification time")
				}
			}
		}
	}
	if hashFile(path) != fixtureHash {
		panic("private fixture content changed")
	}
	if *fixture != "" && fixtureSignature(*fixture) != sourceSignature {
		panic("original fixture metadata changed")
	}
	emit(map[string]any{"phase": "success", "reference_bad": 0, "reference_checked": checkedQueries, "reference_rows_checked": checkedRows, "fixture_sha256_unchanged": fixtureHash, "original_fixture_metadata_unchanged": true, "private_log": path, "memory_events": fields(sharedCG + "/memory.events")})
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
