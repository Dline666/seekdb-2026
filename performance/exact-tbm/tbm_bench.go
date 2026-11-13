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
	ID   int         `json:"id"`
	OK   bool        `json:"ok"`
	Type string      `json:"type"`
	Rows [][]float64 `json:"rows"`
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
func literal(v []float32) string {
	b := []byte{'['}
	for i, x := range v {
		if i > 0 {
			b = append(b, ',')
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
	rounds := flag.Int("rounds", 3, "")
	seed := flag.Int64("seed", 7, "")
	mode := flag.String("data", "uniform", "")
	bins := flag.String("bins", "kernel2,avx-fix", "name[:KEY=VALUE...],...")
	binDir := flag.String("bin-dir", "/tmp/seekdb-review-bin", "directory containing candidate executables")
	dataRoot := flag.String("data-dir", "/tmp", "ext4 directory for fresh databases")
	flag.Parse()
	if *rows <= 0 || *dim <= 0 || *nq <= 0 || *rounds <= 0 {
		panic("rows, dim, queries and rounds must be positive")
	}
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
	batches := [][]byte{}
	for i := 0; i < *rows; i += 100 {
		var b strings.Builder
		b.WriteString("INSERT INTO docs VALUES ")
		for j := i; j < i+100 && j < *rows; j++ {
			if j > i {
				b.WriteByte(',')
			}
			id := j + 1
			if *mode == "constant" {
				id = *rows - j
			}
			fmt.Fprintf(&b, "(%d,'c%d','%s')", id, cats[j], literal(vecs[j]))
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
		}
		q.sql = fmt.Sprintf("SELECT id,L2_DISTANCE(embedding,'%s') AS distance FROM docs%s ORDER BY distance LIMIT 10", literal(q.v), where)
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
					must(os.Mkdir(cg, 0755))
					defer os.Remove(cg)
					for k, v := range map[string]string{"cpu.max": "200000 100000", "memory.max": "2147483648", "memory.swap.max": "0", "pids.max": "128"} {
						must(os.WriteFile(cg+"/"+k, []byte(v), 0644))
					}
					f, e := os.Open(cg)
					must(e)
					errfile, e := os.Create(data + "/stderr.txt")
					must(e)
					cmd := exec.Command(filepath.Join(*binDir, parts[0]), "--data-dir", data+"/db")
					cmd.Env = append(os.Environ(), "GOMAXPROCS=2")
					cmd.Env = append(cmd.Env, parts[1:]...)
					cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(f.Fd())}
					cmd.Stderr = errfile
					in, e := cmd.StdinPipe()
					must(e)
					out, e := cmd.StdoutPipe()
					must(e)
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
					must(e)
					var ready response
					must(json.Unmarshal(b, &ready))
					if ready.Type != "ready" {
						panic(string(b))
					}
					send := func(b []byte) []byte { _, e := in.Write(b); must(e); b, e = rd.ReadBytes('\n'); must(e); return b }
					check(send(request(fmt.Sprintf("CREATE TABLE docs(id BIGINT PRIMARY KEY,category VARCHAR(16),embedding VECTOR(%d))", *dim), "execute", 1)))
					wr := make([][]byte, len(batches))
					start := time.Now()
					for i, b := range batches {
						wr[i] = send(b)
					}
					elapsed := time.Since(start).Seconds()
					for _, b := range wr {
						check(b)
					}
					warmup := 0.0
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
					}
					check(send(request("", "shutdown", 200000)))
					in.Close()
					must(cmd.Wait())
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
					var st syscall.Stat_t
					must(syscall.Stat(data, &st))
					major := (uint64(st.Dev)>>8)&0xfff | (uint64(st.Dev)>>32)&0xfffff000
					minor := uint64(st.Dev)&0xff | (uint64(st.Dev)>>12)&0xffffff00
					device := fmt.Sprintf("%d:%d", major, minor)
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
								if len(got[j]) != 2 || int(got[j][0]) != h.id || math.Abs(got[j][1]-d) > 1e-5*math.Max(1, d) {
									bad++
									break
								}
							}
							b, _ = json.Marshal(got)
							hash.Write(b)
						}
					}
					must(os.Remove(cg))
					must(os.RemoveAll(data))
					rec := map[string]any{"name": name, "phase": phase, "round": round, "rows": *rows, "dim": *dim, "queries": *nq, "seed": *seed, "data": *mode, "t_s": elapsed, "warmup_s": warmup, "m_bytes": peak, "b_bytes": rb + wb, "r_bytes": rb, "w_bytes": wb, "device": device, "cpu_stat": string(cpu), "reference_bad": bad}
					if phase == "query" {
						rec["reference_checked"] = len(truth)
						rec["response_hash"] = fmt.Sprintf("%x", hash.Sum64())
					}
					b, _ = json.Marshal(rec)
					fmt.Println(string(b))
					if bad != 0 {
						panic("exact mismatch")
					}
				}()
			}
		}
	}
}
