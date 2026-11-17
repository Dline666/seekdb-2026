// Diagnostic-only perf_event_open counters for a single child and its threads.
// No sampling, no system-wide events, no changes to perf_event_paranoid.
#define _GNU_SOURCE
#include <errno.h>
#include <linux/perf_event.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/ioctl.h>
#include <sys/prctl.h>
#include <sys/syscall.h>
#include <sys/wait.h>
#include <unistd.h>

struct event { const char *name; uint32_t type; uint64_t config; int fd, error; };
int main(int argc, char **argv) {
    if (argc < 4 || argv[2][0] != '-') return 64;
    struct event ev[] = {
        {"cycles", PERF_TYPE_HARDWARE, PERF_COUNT_HW_CPU_CYCLES, -1, 0},
        {"instructions", PERF_TYPE_HARDWARE, PERF_COUNT_HW_INSTRUCTIONS, -1, 0},
        {"cache_references", PERF_TYPE_HARDWARE, PERF_COUNT_HW_CACHE_REFERENCES, -1, 0},
        {"cache_misses", PERF_TYPE_HARDWARE, PERF_COUNT_HW_CACHE_MISSES, -1, 0},
        {"branches", PERF_TYPE_HARDWARE, PERF_COUNT_HW_BRANCH_INSTRUCTIONS, -1, 0},
        {"branch_misses", PERF_TYPE_HARDWARE, PERF_COUNT_HW_BRANCH_MISSES, -1, 0},
        {"L1D_read_misses", PERF_TYPE_HW_CACHE, PERF_COUNT_HW_CACHE_L1D | (PERF_COUNT_HW_CACHE_OP_READ<<8) | (PERF_COUNT_HW_CACHE_RESULT_MISS<<16), -1, 0},
        {"page_faults", PERF_TYPE_SOFTWARE, PERF_COUNT_SW_PAGE_FAULTS, -1, 0},
        {"context_switches", PERF_TYPE_SOFTWARE, PERF_COUNT_SW_CONTEXT_SWITCHES, -1, 0}
    };
    pid_t child = fork();
    if (child < 0) return 70;
    if (child == 0) {
        prctl(PR_SET_PDEATHSIG, SIGKILL);
        raise(SIGSTOP);
        execvp(argv[3], argv+3);
        _exit(127);
    }
    int status;
    if (waitpid(child, &status, WUNTRACED) != child || !WIFSTOPPED(status)) return 71;
    for (size_t i=0; i<sizeof(ev)/sizeof(ev[0]); i++) {
        struct perf_event_attr a = {0};
        a.size=sizeof(a); a.type=ev[i].type; a.config=ev[i].config;
        a.disabled=1; a.inherit=1; a.enable_on_exec=1;
        a.exclude_kernel=(ev[i].type != PERF_TYPE_SOFTWARE); a.exclude_hv=1;
        a.read_format=PERF_FORMAT_TOTAL_TIME_ENABLED|PERF_FORMAT_TOTAL_TIME_RUNNING;
        ev[i].fd=syscall(__NR_perf_event_open, &a, child, -1, -1, 0);
        if (ev[i].fd < 0) ev[i].error=errno;
    }
    kill(child, SIGCONT);
    while (waitpid(child, &status, 0) < 0) { if (errno != EINTR) return 72; }
    FILE *out=fopen(argv[1], "wx");
    if (!out) return 73;
    fprintf(out,"{\"scope\":\"child plus inherited threads, user hardware and all-mode software, whole process\",\"diagnostic_only\":true,\"child_status\":%d,\"events\":[",status);
    for (size_t i=0; i<sizeof(ev)/sizeof(ev[0]); i++) {
        uint64_t value[3]={0,0,0};
        if (ev[i].fd >= 0) {
            if (read(ev[i].fd, value, sizeof(value)) != sizeof(value)) ev[i].error=errno ? errno : EIO;
            close(ev[i].fd);
        }
        fprintf(out,"%s{\"name\":\"%s\",\"raw\":%llu,\"time_enabled_ns\":%llu,\"time_running_ns\":%llu,\"errno\":%d}",i ? "," : "", ev[i].name,(unsigned long long)value[0],(unsigned long long)value[1],(unsigned long long)value[2],ev[i].error);
    }
    fprintf(out,"]}\n"); fclose(out);
    return WIFEXITED(status) ? WEXITSTATUS(status) : 128+WTERMSIG(status);
}
