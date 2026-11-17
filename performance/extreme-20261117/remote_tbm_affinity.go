// Linux subprocess benchmark. The driver and correctness oracle stay outside
// each fresh cgroup. This is a local comparison, not the official evaluator.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type response struct {
	ID   int             `json:"id"`
	OK   bool            `json:"ok"`
	Type string          `json:"type"`
	Rows [][]json.Number `json:"rows"`
}
type query struct {
	sql string
	v   []float32
	cat int
}
type hit struct {
	id int
	d  float64
}

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func literal(v []float32, spaced bool) string {
	b := []byte{'['}
	for i, x := range v {
		if i > 0 {
			b = append(b, ',')
			if spaced {
				b = append(b, ' ')
			}
		}
		b = strconv.AppendFloat(b, float64(x), 'g', -1, 64)
	}
	return string(append(b, ']'))
}
func num(p string) int64 {
	b, e := os.ReadFile(p)
	must(e)
	n, e := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	must(e)
	return n
}
func counters(p string) map[string]int64 {
	b, e := os.ReadFile(p)
	must(e)
	result := make(map[string]int64)
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			panic("invalid cgroup counter: " + line)
		}
		n, e := strconv.ParseInt(fields[1], 10, 64)
		must(e)
		result[fields[0]] = n
	}
	return result
}
func request(sql, op string, id int) []byte {
	b, e := json.Marshal(map[string]any{"id": id, "op": op, "sql": sql, "search_mode": "exact"})
	must(e)
	return append(b, '\n')
}
func check(b []byte) response {
	var r response
	must(json.Unmarshal(b, &r))
	if !r.OK {
		panic(string(b))
	}
	return r
}
func main() {
	rows := flag.Int("rows", 10000, "")
	dim := flag.Int("dim", 128, "")
	nq := flag.Int("queries", 1000, "")
	rounds := flag.Int("rounds", 5, "five for comparisons; smaller values are smoke only")
	seed := flag.Int64("seed", 7, "")
	mode := flag.String("data", "uniform", "")
	format := flag.String("format", "compact", "compact, spaced (Chinese content and spaced vectors), or multiline (also escaped SQL newlines)")
	cpuAffinity := flag.String("cpu-affinity", "", "optional fixed allowed CPU list, recorded and verified")
	cpuCount := flag.Int("cpu-count", 2, "database cgroup CPU quota and GOMAXPROCS: 1 or 2")
	bins := flag.String("bins", "kernel2,avx-fix", "name[:KEY=VALUE...],...")
	binDir := flag.String("bin-dir", "/tmp/seekdb-review-bin", "directory containing candidate executables")
	dataRoot := flag.String("data-dir", "/tmp", "ext4 directory for fresh databases")
	transport := flag.String("transport", "native", "native or docker; Docker uses a persistent parent slice for full-lifetime counters")
	imagePrefix := flag.String("image-prefix", "seekdb-local-v3:", "Docker image prefix, followed by binary name")
	flag.Parse()
	if *transport != "native" && *transport != "docker" {
		panic("invalid transport")
	}
	if *rows <= 0 || *dim <= 0 || *nq <= 0 || *rounds <= 0 {
		panic("rows, dim, queries and rounds must be positive")
	}
	if *format != "compact" && *format != "spaced" && *format != "multiline" {
		panic("format must be compact, spaced, or multiline")
	}
	if *cpuCount != 1 && *cpuCount != 2 {
		panic("cpu-count must be 1 or 2")
	}
	spaced, multiline := *format != "compact", *format == "multiline"
	cpuMax := fmt.Sprintf("%d 100000", *cpuCount*100000)
	must(os.WriteFile("/sys/fs/cgroup/cgroup.subtree_control", []byte("+cpu +memory +io +pids"), 0644))
	rng := rand.New(rand.NewSource(*seed))
	vecs := make([][]float32, *rows)
	cats := make([]int, *rows)
	for i := range vecs {
		vecs[i] = make([]float32, *dim)
		for j := range vecs[i] {
			switch *mode {
			case "constant":
				vecs[i][j] = float32(1234567890123.0)
			default:
				vecs[i][j] = float32(rng.Float64())
			}
		}
		cats[i] = rng.Intn(10)
	}
	if *mode == "outlier" {
		vecs[0][0] = 1e9
	}
	ddl := fmt.Sprintf("CREATE TABLE docs(id BIGINT PRIMARY KEY,category VARCHAR(16),embedding VECTOR(%d))", *dim)
	if spaced {
		ddl = fmt.Sprintf("CREATE TABLE docs (id BIGINT PRIMARY KEY, category VARCHAR(16), content VARCHAR(256), embedding VECTOR(%d))", *dim)
	}
	if multiline {
		ddl = fmt.Sprintf("CREATE TABLE docs (\n  id BIGINT PRIMARY KEY,\n  category VARCHAR(16),\n  content VARCHAR(256),\n  embedding VECTOR(%d)\n)", *dim)
	}
	ddlRequest := request(ddl, "execute", 1)
	batches := [][]byte{}
	for i := 0; i < *rows; i += 100 {
		var b strings.Builder
		if multiline {
			b.WriteString("INSERT INTO docs\nVALUES\n")
		} else {
			b.WriteString("INSERT INTO docs VALUES ")
		}
		for j := i; j < i+100 && j < *rows; j++ {
			if j > i {
				b.WriteByte(',')
				if multiline {
					b.WriteByte('\n')
				} else if spaced {
					b.WriteByte(' ')
				}
			}
			id := j + 1
			if *mode == "constant" {
				id = *rows - j
			}
			if multiline {
				b.WriteString("  ")
			}
			if spaced {
				fmt.Fprintf(&b, "(%d, 'c%d', '中文文档%d：向量精确查询，类别%d。', '%s')", id, cats[j], id, cats[j], literal(vecs[j], true))
			} else {
				fmt.Fprintf(&b, "(%d,'c%d','%s')", id, cats[j], literal(vecs[j], false))
			}
		}
		batches = append(batches, request(b.String(), "execute", len(batches)+2))
	}
	qs := make([]query, *nq)
	requests := make([][]byte, *nq)
	for i := range qs {
		q := query{v: make([]float32, *dim), cat: -1}
		for j := range q.v {
			if *mode == "constant" {
				q.v[j] = vecs[0][j]
			} else {
				q.v[j] = float32(rng.Float64())
			}
		}
		where := ""
		if i%2 == 1 {
			q.cat = rng.Intn(10)
			where = fmt.Sprintf(" WHERE category='c%d'", q.cat)
			if spaced {
				where = fmt.Sprintf(" WHERE category = 'c%d'", q.cat)
			}
			if multiline {
				where = fmt.Sprintf("\nWHERE category = 'c%d'", q.cat)
			}
		}
		q.sql = fmt.Sprintf("SELECT id,L2_DISTANCE(embedding,'%s') AS distance FROM docs%s ORDER BY distance LIMIT 10", literal(q.v, spaced), where)
		if spaced {
			q.sql = fmt.Sprintf("SELECT id, L2_DISTANCE(embedding, '%s') AS distance FROM docs%s ORDER BY distance LIMIT 10", literal(q.v, true), where)
		}
		if multiline {
			q.sql = fmt.Sprintf("SELECT id,\n  L2_DISTANCE(embedding, '%s') AS distance\nFROM docs%s\nORDER BY distance\nLIMIT 10", literal(q.v, true), where)
		}
		qs[i] = q
		requests[i] = request(q.sql, "execute", i+100000)
	}
	// Full independent scalar float64 truth, computed before any measured run.
	truth := make([][]hit, *nq)
	for i, q := range qs {
		best := make([]hit, 0, 11)
		for j, v := range vecs {
			if q.cat >= 0 && cats[j] != q.cat {
				continue
			}
			var d float64
			for k, x := range v {
				z := float64(x) - float64(q.v[k])
				d += z * z
			}
			id := j + 1
			if *mode == "constant" {
				id = *rows - j
			}
			h := hit{id, d}
			at := sort.Search(len(best), func(k int) bool { return best[k].d > d || (best[k].d == d && best[k].id > id) })
			if at >= 10 {
				continue
			}
			best = append(best, h)
			copy(best[at+1:], best[at:len(best)-1])
			best[at] = h
			if len(best) > 10 {
				best = best[:10]
			}
		}
		truth[i] = best
	}
	for round := 0; round < *rounds; round++ {
		names := strings.Split(*bins, ",")
		if round%2 == 1 {
			for i, j := 0, len(names)-1; i < j; i, j = i+1, j-1 {
				names[i], names[j] = names[j], names[i]
			}
		}
		for _, name := range names {
			for _, phase := range []string{"write", "query"} {
				func() {
					parts := strings.Split(name, ":")
					data, e := os.MkdirTemp(*dataRoot, "seekdb-tbm-data-")
					must(e)
					defer os.RemoveAll(data)
					cg := fmt.Sprintf("/sys/fs/cgroup/seekdb-tbm-%d", os.Getpid())
					unit := fmt.Sprintf("seekdbtbm%d.slice", os.Getpid())
					if *transport == "docker" {
						must(exec.Command("systemctl", "start", unit).Run())
						cg = "/sys/fs/cgroup/" + unit
					} else {
						must(os.Mkdir(cg, 0755))
					}
					cleanup := func() error {
						if *transport == "docker" {
							return exec.Command("systemctl", "stop", unit).Run()
						}
						return os.Remove(cg)
					}
					defer cleanup()
					for k, v := range map[string]string{"cpu.max": cpuMax, "memory.max": "2147483648", "memory.swap.max": "0", "pids.max": "128"} {
						must(os.WriteFile(cg+"/"+k, []byte(v), 0644))
					}
					f, e := os.Open(cg)
					must(e)
					errfile, e := os.Create(data + "/stderr.txt")
					must(e)
					cmd := exec.Command(filepath.Join(*binDir, parts[0]), "--data-dir", data+"/db", "--mode", "exact", "--sync", "fdatasync")
					if *cpuAffinity != "" {
						cmd = exec.Command("taskset", "--cpu-list", *cpuAffinity, filepath.Join(*binDir, parts[0]), "--data-dir", data+"/db", "--mode", "exact", "--sync", "fdatasync")
					}
					cmd.Env = append(os.Environ(), parts[1:]...)
					// CPU and heap controls are fixed after optional per-version
					// flags, so a bin suffix cannot silently relabel the control.
					cmd.Env = append(cmd.Env, fmt.Sprintf("GOMAXPROCS=%d", *cpuCount))
					// UseCgroupFD enforces the hard limit without a private cgroup
					// namespace. Match the application's Docker heap target even
					// when its root-path detector cannot see this child cgroup.
					cmd.Env = append(cmd.Env, "GOMEMLIMIT=1288490188")
					cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(f.Fd())}
					containerName := fmt.Sprintf("seekdbv3-%d", os.Getpid())
					if *transport == "docker" {
						must(os.Mkdir(data+"/db", 0755))
						args := []string{"run", "--rm", "--network=none", "--name", containerName,
							"--cgroup-parent", unit, "--cpus", strconv.Itoa(*cpuCount),
							"--memory", "2147483648", "--memory-swap", "2147483648", "--pids-limit", "128",
							"--mount", "type=bind,src=" + data + "/db,dst=/data", "-e", "GOGC=20",
							"-e", fmt.Sprintf("GOMAXPROCS=%d", *cpuCount), "-e", "GOMEMLIMIT=1288490188"}
						for _, setting := range parts[1:] {
							args = append(args, "-e", setting)
						}
						if *cpuAffinity != "" {
							args = append(args, "--cpuset-cpus", *cpuAffinity)
						}
						args = append(args, "-i", *imagePrefix+parts[0], "--data-dir", "/data", "--mode", "exact", "--sync", "fdatasync")
						cmd = exec.Command("docker", args...)
						defer exec.Command("docker", "rm", "-f", containerName).Run()
					}
					cmd.Stderr = errfile
					in, e := cmd.StdinPipe()
					must(e)
					out, e := cmd.StdoutPipe()
					must(e)
					startupStart := time.Now()
					must(cmd.Start())
					defer func() {
						if cmd.ProcessState == nil {
							cmd.Process.Kill()
							cmd.Wait()
						}
					}()
					f.Close()
					rd := bufio.NewReaderSize(out, 1<<20)
					b, e := rd.ReadBytes('\n')
					if e != nil {
						diagnostic, _ := os.ReadFile(data + "/stderr.txt")
						fmt.Fprintln(os.Stderr, string(diagnostic))
					}
					must(e)
					var ready response
					must(json.Unmarshal(b, &ready))
					if ready.Type != "ready" {
						panic(string(b))
					}
					startupSeconds := time.Since(startupStart).Seconds()
					databasePID := cmd.Process.Pid
					containerCgroup := ""
					if *transport == "docker" {
						pidBytes, err := exec.Command("docker", "inspect", "--format", "{{.State.Pid}}", containerName).Output()
						must(err)
						pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
						must(err)
						databasePID = pid
						cgBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
						must(err)
						containerCgroup = strings.TrimSpace(string(cgBytes))
						if !strings.HasPrefix(containerCgroup, "0::/"+unit+"/") {
							panic("container escaped measurement slice: " + containerCgroup)
						}
					}
					status, e := os.ReadFile(fmt.Sprintf("/proc/%d/status", databasePID))
					must(e)
					allowedCPUs := ""
					for _, line := range strings.Split(string(status), "\n") {
						if strings.HasPrefix(line, "Cpus_allowed_list:") {
							allowedCPUs = strings.TrimSpace(strings.TrimPrefix(line, "Cpus_allowed_list:"))
						}
					}
					if *cpuAffinity != "" && allowedCPUs != *cpuAffinity {
						panic("database CPU affinity mismatch: " + allowedCPUs)
					}
					send := func(b []byte) []byte { _, e := in.Write(b); must(e); b, e = rd.ReadBytes('\n'); must(e); return b }
					ddlStart := time.Now()
					check(send(ddlRequest))
					ddlSeconds := time.Since(ddlStart).Seconds()
					wr := make([][]byte, len(batches))
					start := time.Now()
					for i, b := range batches {
						wr[i] = send(b)
					}
					loadSeconds := time.Since(start).Seconds()
					elapsed := loadSeconds
					for _, b := range wr {
						check(b)
					}
					warmup := 0.0
					querySeconds := 0.0
					results := make([][]byte, len(qs))
					bad := 0
					hash := fnv.New64a()
					if phase == "query" {
						start = time.Now()
						check(send(request(qs[0].sql, "execute", 99999)))
						warmup = time.Since(start).Seconds()
						start = time.Now()
						for i, b := range requests {
							results[i] = send(b)
						}
						elapsed = time.Since(start).Seconds()
						querySeconds = elapsed
					}
					shutdownStart := time.Now()
					check(send(request("", "shutdown", 200000)))
					in.Close()
					must(cmd.Wait())
					shutdownSeconds := time.Since(shutdownStart).Seconds()
					// These nonoverlapping measured spans omit external truth,
					// load-response validation, and post-exit file Sync work.
					// On write, elapsed already equals loadSeconds; never add it twice.
					requestTotal := startupSeconds + ddlSeconds + loadSeconds + warmup + querySeconds + shutdownSeconds
					errfile.Close()
					// All commits and graceful shutdown have flushed the database. Also wait
					// for any remaining filesystem writeback before taking cgroup totals.
					must(filepath.Walk(data+"/db", func(p string, info os.FileInfo, e error) error {
						if e != nil {
							return e
						}
						if !info.IsDir() {
							f, e := os.Open(p)
							if e != nil {
								return e
							}
							e = f.Sync()
							f.Close()
							return e
						}
						return nil
					}))
					peak := num(cg + "/memory.peak")
					ios, e := os.ReadFile(cg + "/io.stat")
					must(e)
					cpu, e := os.ReadFile(cg + "/cpu.stat")
					must(e)
					memoryEvents := counters(cg + "/memory.events")
					if _, ok := memoryEvents["oom"]; !ok {
						panic("memory.events missing oom counter")
					}
					if _, ok := memoryEvents["oom_kill"]; !ok {
						panic("memory.events missing oom_kill counter")
					}
					var st syscall.Stat_t
					must(syscall.Stat(data, &st))
					major := (uint64(st.Dev)>>8)&0xfff | (uint64(st.Dev)>>32)&0xfffff000
					minor := uint64(st.Dev)&0xff | (uint64(st.Dev)>>12)&0xffffff00
					device := accountingDevice(major, minor)
					var rb, wb int64
					found := false
					for _, line := range strings.Split(string(ios), "\n") {
						fields := strings.Fields(line)
						if len(fields) < 2 || fields[0] != device {
							continue
						}
						found = true
						for _, kv := range fields[1:] {
							a := strings.SplitN(kv, "=", 2)
							v, _ := strconv.ParseInt(a[1], 10, 64)
							if a[0] == "rbytes" {
								rb = v
							}
							if a[0] == "wbytes" {
								wb = v
							}
						}
					}
					if !found {
						panic("missing data-device io.stat: " + device + " " + string(ios))
					}
					if phase == "query" {
						for i, b := range results {
							r := check(b)
							if r.ID != i+100000 {
								panic("wrong id")
							}
							got := r.Rows
							want := truth[i]
							if len(got) != len(want) {
								bad++
								continue
							}
							for j, h := range want {
								d := math.Sqrt(h.d)
								if len(got[j]) != 2 {
									bad++
									break
								}
								id, idErr := got[j][0].Int64()
								distance, distanceErr := got[j][1].Float64()
								if idErr != nil || distanceErr != nil || math.IsNaN(distance) || math.IsInf(distance, 0) || id != int64(h.id) || math.Abs(distance-d) > 1e-5*math.Max(1, d) {
									bad++
									break
								}
							}
							b, _ = json.Marshal(got)
							hash.Write(b)
						}
					}
					must(cleanup())
					must(os.RemoveAll(data))
					rec := map[string]any{
						"name": name, "phase": phase, "round": round, "rows": *rows, "dim": *dim, "queries": *nq, "seed": *seed, "data": *mode,
						"transport": *transport, "container_cgroup": containerCgroup,
						"cpu_affinity_requested": *cpuAffinity, "cpu_affinity_allowed": allowedCPUs, "format": *format, "content_column": spaced, "cpu_count": *cpuCount, "cpu_max": cpuMax, "gomaxprocs": *cpuCount,
						"memory_limit_bytes": int64(2 << 30), "memory_swap_max": 0, "sync": "fdatasync", "search_mode": "exact",
						"t_s": elapsed, "warmup_s": warmup, "startup_s": startupSeconds, "ddl_s": ddlSeconds, "load_s": loadSeconds,
						"query_s": querySeconds, "shutdown_s": shutdownSeconds, "request_total_s": requestTotal,
						"t_s_definition":             "write: load_s only; query: query_s only, after one warmup request",
						"startup_s_definition":       "immediately before cmd.Start through validated ready response",
						"shutdown_s_definition":      "shutdown request through acknowledged response, stdin close and process Wait",
						"request_total_s_definition": "startup_s + ddl_s + load_s + warmup_s + query_s + shutdown_s; write warmup_s/query_s are zero",
						"timing_excludes":            "fixture/oracle/request generation (except original warmup/shutdown request encoding), load-response validation and post-exit file Sync; query oracle validation is after exit",
						"bm_scope":                   "whole fresh process including load and warmup, with final file Sync before reading I/O counters; not query-only",
						"m_bytes":                    peak, "b_bytes": rb + wb, "r_bytes": rb, "w_bytes": wb, "device": device,
						"filesystem_device": fmt.Sprintf("%d:%d", major, minor), "io_stat_raw": string(ios), "cpu_stat": string(cpu), "memory_events": memoryEvents, "reference_bad": bad,
					}
					if phase == "query" {
						rec["reference_checked"] = len(truth)
						rec["response_hash"] = fmt.Sprintf("%x", hash.Sum64())
					}
					b, _ = json.Marshal(rec)
					fmt.Println(string(b))
					if bad != 0 {
						panic("exact mismatch")
					}
					if memoryEvents["oom"] != 0 || memoryEvents["oom_kill"] != 0 {
						panic("database cgroup recorded OOM")
					}
				}()
			}
		}
	}
}

// A partition-backed filesystem is charged to its parent disk in io.stat on
// this kernel. Resolve that identity through sysfs, never guess or sum devices.
func accountingDevice(major, minor uint64) string {
	name := fmt.Sprintf("%d:%d", major, minor)
	node := "/sys/dev/block/" + name
	if _, err := os.Stat(node + "/partition"); err == nil {
		resolved, err := filepath.EvalSymlinks(node)
		must(err)
		data, err := os.ReadFile(filepath.Join(filepath.Dir(resolved), "dev"))
		must(err)
		return strings.TrimSpace(string(data))
	}
	return name
}
