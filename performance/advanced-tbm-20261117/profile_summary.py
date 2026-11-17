"""Summarize diagnostic snapshots; never treat instrumented time as a score."""
import json
from pathlib import Path

HERE = Path(__file__).resolve().parent
SOURCES = {
    'baseline_default': 'default/round0-advanced-profile-query',
    'final_default': 'final-default/round0-advanced-final-profile-query',
    'baseline_100k': '100k/round0-advanced-profile-query',
    'final_100k': 'final-100k/round0-advanced-final-profile-query',
    'baseline_large': 'large/trial1-advanced-profile',
    'single_pass_large': 'large/trial2-advanced-eqsingle-profile',
}


def seconds(value):
    return value['Sec'] + value['Usec']/1e6


def summarize(relative):
    path = HERE/'profiles'/relative/'runtime.jsonl'
    entries = {r['event']: r for r in map(json.loads, path.read_text().splitlines())}
    out = {'path': str(path.relative_to(HERE)), 'diagnostic_only': True}
    for phase in ['startup', 'prepare', 'query']:
        start, end = entries[phase+'-begin'], entries[phase+'-end']
        a,b = start['memstats'], end['memstats']
        ru0,ru1 = start['rusage'], end['rusage']
        out[phase] = {
            'allocated_bytes': b['TotalAlloc']-a['TotalAlloc'],
            'allocated_objects': b['Mallocs']-a['Mallocs'],
            'gc_cycles': b['NumGC']-a['NumGC'],
            'gc_pause_s': (b['PauseTotalNs']-a['PauseTotalNs'])/1e9,
            'gc_cpu_s': end['metrics']['/cpu/classes/gc/total:cpu-seconds']-
                        start['metrics']['/cpu/classes/gc/total:cpu-seconds'],
            'end_heap_alloc_bytes': b['HeapAlloc'],
            'end_heap_objects': b['HeapObjects'],
            'user_cpu_s': seconds(ru1['Utime'])-seconds(ru0['Utime']),
            'system_cpu_s': seconds(ru1['Stime'])-seconds(ru0['Stime']),
            **{key: ru1[key]-ru0[key] for key in ['Minflt','Majflt','Nvcsw','Nivcsw']}}
    out['after_forced_gc'] = {
        'heap_alloc_bytes': entries['finish-after-gc']['memstats']['HeapAlloc'],
        'scan_heap_bytes': entries['finish-after-gc']['metrics']['/gc/scan/heap:bytes'],
    }
    return out


if __name__ == '__main__':
    result = {name: summarize(path) for name,path in SOURCES.items()}
    with (HERE/'results/profile-summary.json').open('x') as f:
        json.dump(result,f,indent=2);f.write('\n')
    for name,value in result.items():
        print(name, json.dumps(value['query']))
