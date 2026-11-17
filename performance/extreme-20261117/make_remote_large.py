"""Keep historical phase-reset drivers unchanged; adapt to server kernel 6.5."""
from pathlib import Path
root=Path(__file__).resolve().parents[2]
out=Path(__file__).resolve().parent/'remote_large'
out.mkdir(exist_ok=True)
s=(root/'performance/exact-tbm/round7/large/main.go').read_text()
start=s.index('func resetPeak(cg string)')
end=s.index('\nfunc begin(c *child)',start)
s=s[:start]+'''func resetPeak(cg string) (*os.File, bool) {
    // The server probe established a read-only, lifetime memory.peak.
    // Each trial has a fresh cgroup; no phase reset is attempted or claimed.
    f, err := os.Open(cg + "/memory.peak")
    must(err)
    return f, false
}
'''+s[end:]
s=s.replace('cmd := exec.Command(binary, "--data-dir",',
            'cmd := exec.Command("taskset", "--cpu-list", "16-17", binary, "--data-dir",')
# Verify the effective affinity after ready, rather than trusting taskset argv.
ready_needle='\t\t\tc.ready()'
assert ready_needle in s
s=s.replace(ready_needle, ready_needle+'''
            status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", c.cmd.Process.Pid))
            must(err)
            affinityOK := false
            for _, line := range strings.Split(string(status), "\\n") {
                if strings.HasPrefix(line, "Cpus_allowed_list:") {
                    affinityOK = strings.TrimSpace(strings.TrimPrefix(line, "Cpus_allowed_list:")) == "16-17"
                }
            }
            if !affinityOK { panic("database CPU affinity mismatch") }
''')
s=s.replace('"filter_scope": "all live', '"database_cpu_affinity": "16-17 (verified after ready)",\n\t\t"filter_scope": "all live')
a=s.index('\tif m.peakFD == nil || !m.reset {')
b=s.index('\trec := map[string]any',a)
s=s[:a]+'''\t// Linux 6.5 cannot reset memory.peak. A fresh cgroup per trial makes
	// this the cumulative startup+cold+replay+shutdown peak. Phase T/B
	// remain deltas; only service_total has a complete-lifetime M.
	if m.peakFD != nil {
		must(m.peakFD.Close())
	}
	peak := number(m.c.cg + "/memory.peak")
	if peak <= 0 || r < m.r || w < m.w {
		panic("invalid trial peak or negative physical I/O delta")
	}
	scope := "whole_trial"
'''+s[b:]
s=s.replace('"memory_peak_scope": scope,', '"memory_peak_scope": scope, "measurement_cgroup": m.c.cg,')
s=s.replace('fmt.Sprintf("/sys/fs/cgroup/seekdb-round7-large-%d", os.Getpid())','fmt.Sprintf("/sys/fs/cgroup/seekdb-remote-large-%d-%d", os.Getpid(), seq)')
s=s.replace('\tresponseHash := ""','\tvar finalMemoryEvents map[string]int64\n\tresponseHash := ""')
s=s.replace('\t\t\tc.cleanup()\n\t\t\tif fixtureSignature', '''\t\t\tfinalMemoryEvents = fields(sharedCG + "/memory.events")
			c.cleanup()
			must(os.Remove(sharedCG))
			sharedCG = ""
			// Surviving file pages are evicted and mincore-verified before
			// the next cgroup starts: no trial escapes page-cache charges.
			if fixtureSignature''')
s=s.replace('"memory_events": fields(sharedCG + "/memory.events"),\n\t\t"cgroup_reused_across_restarts": true', '"memory_events": finalMemoryEvents,\n\t\t"cgroup_reused_across_restarts": false, "fresh_cgroup_per_trial": true')
s=s.replace('"filter_scope": "all live', '"memory_accounting": "fresh cgroup per trial, cumulative peak; only service_total M is comparable",\n\t\t"filter_scope": "all live')
s=s.replace('device := fmt.Sprintf("%d:%d", major, minor)', 'device := accountingDevice(major, minor)')
s += '\n' + (out.parent/'server_device_helper.txt').read_text()
(out/'main.go').write_text(s)
a=(root/'performance/exact-tbm/round7/large/analyze.py').read_text()
a=a.replace("record['memory_peak_scope'] == 'phase_reset_fd', 'phase peak reset not verified'", "record['memory_peak_scope'] == 'whole_trial', 'whole-trial cumulative peak not verified'")
a=a.replace("final['cgroup_reused_across_restarts'] is True", "final['cgroup_reused_across_restarts'] is False and final['fresh_cgroup_per_trial'] is True")
a=a.replace("    cursor = 3", "    seen_cgroups = set()\n    cursor = 3")
a=a.replace("            completed = take('trial_complete')", """            cg = startup['measurement_cgroup']
            require(cg not in seen_cgroups, 'trial did not get a fresh cgroup')
            seen_cgroups.add(cg)
            require(all(r['measurement_cgroup'] == cg for r in phases.values()), 'cgroup changed inside trial')
            peaks = [phases[p]['m_bytes'] for p in STAGES]
            require(peaks == sorted(peaks), 'cumulative trial peak decreased')
            completed = take('trial_complete')""")
a=a.replace("if scope in ('cold', 'replay', 'service_total'):", "if scope == 'service_total':")
a=a.replace('This reports local query-service T/B/M and stage E, never a write/query overall', 'Server Linux 6.5: only service_total has a comparable lifetime M and E.\nOther phase M values are cumulative within each trial, not phase peaks.\nThis reports local query-service T/B/M, never a write/query overall')
a=a.replace('M is the retained-FD kernel cgroup peak.', 'M is the fresh-cgroup whole-trial lifetime kernel peak.')
(out/'analyze.py').write_text(a)
