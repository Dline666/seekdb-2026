#!/usr/bin/env python3
"""运行官方公开用例：调用题包自带的 protocol_check（JSONL 协议）。

每个用例使用独立空数据目录；支持 --restart 与 --mode 指令、error 断言。
需要 tests/public 下同时存在 manifest.json、cases/ 与 protocol_check/。

用法:
    python3 tools/run_public_cases.py --bin ./build/release/minivectordb
    python3 tools/run_public_cases.py --image minivectordb:dev
    python3 tools/run_public_cases.py --bin ./build/release/minivectordb --case sql_crud
"""

import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--bin", default=None, help="本地可执行文件")
    parser.add_argument("--image", default=None, help="Docker 镜像（与 --bin 二选一）")
    parser.add_argument("--package", default=os.path.join("tests", "public"),
                        help="题包目录（含 manifest.json、cases/、protocol_check/）")
    parser.add_argument("--case", action="append", default=None, help="只跑指定用例")
    args = parser.parse_args()
    if not args.bin and not args.image:
        parser.error("one of --bin or --image is required")

    package = os.path.abspath(args.package)
    runner = os.path.join(package, "protocol_check", "run.py")
    if not os.path.exists(runner):
        print("missing official checker: %s" % runner, file=sys.stderr)
        return 2

    # run.py 要求输出目录事先不存在：先建父目录，报告写到其下。
    parent = tempfile.mkdtemp(prefix="public-cases-")
    output = os.path.join(parent, "report")
    command = [sys.executable, runner, "--package", package, "--output", output]
    if args.image:
        command += ["--image", args.image]
    else:
        argv = [os.path.abspath(args.bin), "--data-dir", "{data_dir}"]
        command += ["--command", json.dumps(argv)]
    for case in args.case or []:
        command += ["--case", case]

    try:
        return subprocess.call(command)
    finally:
        shutil.rmtree(parent, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
