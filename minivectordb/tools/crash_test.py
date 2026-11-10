#!/usr/bin/env python3
"""崩溃恢复测试。

随机时刻向数据库进程发送 SIGKILL，重启后校验：
  * 所有已确认的提交必须完整保留；
  * 崩溃瞬间在途的语句/事务可以整体生效或整体不生效，不能部分生效；
  * 恢复过程中再次被强杀，最终状态保持不变。

用法（在 Linux 或 WSL 内运行）：
    python3 tools/crash_test.py --bin /tmp/mvdb --iterations 30
    python3 tools/crash_test.py --bin /tmp/mvdb --skip-workload --recovery-rounds 5

注意：Windows 下 SIGKILL 语义不同，请在 WSL 里跑。
"""

import argparse
import os
import random
import shutil
import signal as alarm
import subprocess
import sys
import time
from dataclasses import dataclass
from typing import Callable, Dict, List, Tuple

SCHEMA = (
    "CREATE TABLE t (id BIGINT PRIMARY KEY, payload VARCHAR(32), "
    "v VECTOR(4));"
)

State = Dict[int, str]


class ResponseTimeout(RuntimeError):
    pass


def _on_alarm(signum, frame):
    raise ResponseTimeout("timed out waiting for database response")


class Db:
    def __init__(self, binary: str, data_dir: str, stderr_path: str) -> None:
        self.stderr = open(stderr_path, "wb")
        self.proc = subprocess.Popen(
            [binary, "--protocol", "table", "--data-dir", data_dir],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=self.stderr,
            text=True,
            bufsize=1,
        )

    def send(self, sql: str) -> None:
        assert self.proc.stdin is not None
        if not sql.rstrip().endswith(";"):
            sql = sql + ";"
        self.proc.stdin.write(sql + "\n")
        self.proc.stdin.flush()

    def _read_line(self) -> str:
        assert self.proc.stdout is not None
        alarm.signal(alarm.SIGALRM, _on_alarm)
        alarm.alarm(60)
        try:
            line = self.proc.stdout.readline()
        finally:
            alarm.alarm(0)
        if line == "":
            raise RuntimeError("database closed stdout unexpectedly")
        return line.rstrip("\n")

    def read_result(self) -> Tuple[str, object]:
        line = self._read_line()
        if line.startswith("OK"):
            return ("ok", line)
        if line.startswith("ERR"):
            return ("err", line)
        if line.startswith("ROWS "):
            count = int(line.split(" ", 1)[1])
            header = self._read_line().split("\t")
            rows = [self._read_line().split("\t") for _ in range(count)]
            return ("rows", (header, rows))
        raise RuntimeError("unexpected response: %r" % line)

    def execute(self, sql: str) -> Tuple[str, object]:
        self.send(sql)
        return self.read_result()

    def close(self) -> None:
        try:
            if self.proc.stdin:
                self.proc.stdin.close()
        except Exception:
            pass
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait()
        self.stderr.close()

    def kill(self) -> None:
        self.proc.kill()
        self.proc.wait()
        self.stderr.close()


def insert_stmt(key: int) -> str:
    return "INSERT INTO t VALUES (%d, 'p%d', '[1,2,3,4]')" % (key, key)


def apply_insert(state: State, key: int) -> None:
    state[key] = "p%d" % key


def apply_update(state: State, key: int, value: str) -> None:
    if key in state:
        state[key] = value


def apply_delete(state: State, key: int) -> None:
    state.pop(key, None)


@dataclass
class Action:
    statements: List[str]
    mutate: Callable[[State], None]


def build_atomic(rng: random.Random, state: State, next_id: int):
    kind = rng.choices(["insert", "update", "delete"], weights=[5, 3, 2])[0]
    if kind == "insert" or not state:
        key = next_id
        return [insert_stmt(key)], (lambda s, k=key: apply_insert(s, k)), next_id + 1
    if kind == "update":
        key = rng.choice(list(state))
        value = "u%d" % next_id
        return (
            ["UPDATE t SET payload = '%s' WHERE id = %d" % (value, key)],
            (lambda s, k=key, v=value: apply_update(s, k, v)),
            next_id + 1,
        )
    key = rng.choice(list(state))
    return (
        ["DELETE FROM t WHERE id = %d" % key],
        (lambda s, k=key: apply_delete(s, k)),
        next_id,
    )


def build_action(rng: random.Random, state: State, next_id: int):
    if rng.random() >= 0.3:  # 单条自动提交语句
        statements, mutate, next_id = build_atomic(rng, state, next_id)
        return Action(statements, mutate), next_id

    statements = ["BEGIN"]
    mutates: List[Callable[[State], None]] = []
    for _ in range(rng.randint(2, 4)):
        part, mutate, next_id = build_atomic(rng, state, next_id)
        statements.extend(part)
        mutates.append(mutate)
    statements.append("COMMIT")

    def mutate_all(target: State) -> None:
        for fn in mutates:
            fn(target)

    return Action(statements, mutate_all), next_id


def open_db(binary: str, data_dir: str, tag: str) -> Db:
    return Db(binary, data_dir, os.path.join(data_dir, "..", tag + ".stderr"))


def read_state(binary: str, data_dir: str, tag: str = "read") -> State:
    db = open_db(binary, data_dir, tag)
    kind, payload = db.execute("SELECT id, payload FROM t ORDER BY id;")
    db.close()
    if kind != "rows":
        raise RuntimeError("state query failed: %r" % (payload,))
    header, rows = payload  # type: ignore[misc]
    if header != ["id", "payload"]:
        raise RuntimeError("unexpected header: %r" % (header,))
    state: State = {}
    for row in rows:
        state[int(row[0])] = row[1]
    return state


def diff_state(actual: State, expected: State) -> str:
    missing = {k: v for k, v in expected.items() if actual.get(k) != v}
    extra = {k: v for k, v in actual.items() if expected.get(k) != v}
    parts = []
    if missing:
        parts.append("missing/mismatched: %r" % dict(list(missing.items())[:8]))
    if extra:
        parts.append("extra/mismatched: %r" % dict(list(extra.items())[:8]))
    return "; ".join(parts) if parts else "identical"


def run_workload_iteration(binary, work_dir, rng, max_actions):
    data_dir = os.path.join(work_dir, "data")
    shutil.rmtree(data_dir, ignore_errors=True)
    os.makedirs(data_dir)

    db = open_db(binary, data_dir, "workload")
    kind, payload = db.execute(SCHEMA)
    if kind != "ok":
        raise RuntimeError("schema creation failed: %r" % (payload,))

    committed: State = {}
    next_id = 1
    done = 0
    while done < max_actions:
        action, next_id = build_action(rng, committed, next_id)
        pending = dict(committed)
        action.mutate(pending)

        # 决定是否在发送途中强杀
        if rng.random() < 0.3:
            cut = rng.randint(0, len(action.statements))
            for statement in action.statements[:cut]:
                db.send(statement)
                if rng.random() < 0.5:
                    time.sleep(rng.uniform(0.0005, 0.003))
            if rng.random() < 0.5:
                time.sleep(rng.uniform(0.0, 0.002))
            db.kill()
            candidates = [committed]
            # 所有语句（含提交点）都已发出：可能整体生效，也可能整体未生效
            if cut == len(action.statements):
                candidates.append(pending)
            return data_dir, candidates, "kill@%d/%d" % (cut, len(action.statements))

        for statement in action.statements:
            result_kind, result = db.execute(statement)
            if result_kind == "err":
                raise RuntimeError("unexpected error for %r: %r" % (statement, result))
        committed = pending
        done += 1

    db.close()
    return data_dir, [committed], "clean"


def interrupt_recoveries(binary, data_dir, rng, rounds):
    for _ in range(rounds):
        proc = subprocess.Popen(
            [binary, "--data-dir", data_dir],
            stdin=subprocess.PIPE,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        time.sleep(rng.uniform(0.001, 0.03))
        proc.kill()
        proc.wait()


def check_state(binary, data_dir, candidates, label):
    actual = read_state(binary, data_dir)
    if actual in candidates:
        return True
    details = "\n".join(
        "  candidate %d vs actual: %s" % (i, diff_state(actual, c))
        for i, c in enumerate(candidates)
    )
    print("FAIL [%s] actual state matches no candidate:\n%s" % (label, details))
    return False


def large_wal_recovery_test(binary, work_dir, rng, rows, rounds):
    """先制造一个较大的 WAL，再在恢复途中反复强杀。"""
    data_dir = os.path.join(work_dir, "big")
    shutil.rmtree(data_dir, ignore_errors=True)
    os.makedirs(data_dir)

    db = open_db(binary, data_dir, "big-workload")
    kind, payload = db.execute(SCHEMA)
    if kind != "ok":
        raise RuntimeError("schema creation failed: %r" % (payload,))
    committed: State = {}
    for key in range(1, rows + 1):
        result_kind, result = db.execute(insert_stmt(key))
        if result_kind != "ok":
            raise RuntimeError("insert failed: %r" % (result,))
        committed[key] = "p%d" % key
    # 最后一条只发不读，制造"提交在途"的窗口
    db.send(insert_stmt(rows + 1))
    time.sleep(0.001)
    db.kill()
    candidates = [committed, {**committed, rows + 1: "p%d" % (rows + 1)}]

    interrupt_recoveries(binary, data_dir, rng, rounds)
    return check_state(binary, data_dir, candidates, "recovery-interrupted")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--bin", required=True, help="minivectordb 可执行文件")
    parser.add_argument("--iterations", type=int, default=30)
    parser.add_argument("--max-actions", type=int, default=40)
    parser.add_argument("--recovery-rounds", type=int, default=0,
                        help="恢复途中强杀轮数；0 表示跳过该测试")
    parser.add_argument("--big-rows", type=int, default=20000)
    parser.add_argument("--seed", type=int, default=20261109)
    parser.add_argument("--keep", action="store_true", help="保留工作目录")
    args = parser.parse_args()

    rng = random.Random(args.seed)
    work_dir = "/tmp/minivectordb-crash-test"
    shutil.rmtree(work_dir, ignore_errors=True)
    os.makedirs(work_dir)

    failures = 0
    try:
        for i in range(args.iterations):
            data_dir, candidates, label = run_workload_iteration(
                args.bin, work_dir, rng, args.max_actions)
            # 恢复途中再强杀几次，最终状态必须仍属于候选集合
            interrupt_recoveries(args.bin, data_dir, rng, rng.randint(1, 3))
            if check_state(args.bin, data_dir, candidates, "iter %d %s" % (i, label)):
                print("PASS: iter %d (%s)" % (i, label))
            else:
                failures += 1
                if not args.keep:
                    keep_dir = os.path.join(work_dir, "failed-iter-%d" % i)
                    shutil.copytree(data_dir, keep_dir)
                    print("      kept data at %s" % keep_dir)

        if args.recovery_rounds > 0:
            if large_wal_recovery_test(args.bin, work_dir, rng,
                                       args.big_rows, args.recovery_rounds):
                print("PASS: recovery-interrupted")
            else:
                failures += 1
    finally:
        if not args.keep and failures == 0:
            shutil.rmtree(work_dir, ignore_errors=True)

    if failures:
        print("%d failure(s)" % failures)
        return 1
    print("all crash tests passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
