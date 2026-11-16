"""Copy small review artifacts into the branch; large fixtures stay external."""
from common import *

summary=json.loads((OUTPUT/'summary.json').read_text())
assert summary['source_unchanged'] and summary['official_submissions']==0
assert (OUTPUT/'candidate-fresh-large-retry-archive.json').is_file()
target=REPO/'performance/exact-tbm/round2-v3'
assert not target.exists()
target.mkdir(parents=True)
scripts={'common.py','benchmark.py','analyze_tbm.py','tbm_eval.go','run_comparisons.py',
    'build_tools.py','build_candidate.py','build_memory.py','check_correctness.py',
    'stress_driver.go','stress_verify.py','report.py','post_comparisons.py','retry_large.py',
    'archive_retention.py','archive_large.py','archive_interrupted.py','package_results.py',
    'red_verified.py','red.py','rebuild_driver.py'}
for p in OUTPUT.iterdir():
    if not p.is_file() or p.stat().st_size>2_000_000:
        continue
    if p.suffix not in {'.json','.jsonl','.txt','.md'} and p.name not in scripts:
        continue
    # Canonical text hashes remain stable across Git's Windows line endings.
    data=p.read_bytes().replace(b'\r\n',b'\n')
    (target/p.name).write_bytes(data)
snapshots=target/'diagnostics'
snapshots.mkdir()
(target/'.gitattributes').write_text('# Preserve the protocol runner output, including its trailing spaces.\n'
                                    'candidate-protocol-docker.txt whitespace=-blank-at-eol\n')
for name,src in [('numeric_test.go',OUTPUT.parent/'seekdb-fable-v2-evaluation/diagnostics/numeric_test.go'),
                 ('retention_repro.go',OUTPUT.parent/'seekdb-fable-v2-evaluation/retention_repro.go')]:
    (snapshots/name).write_bytes(src.read_bytes().replace(b'\r\n',b'\n'))
with (target/'REPORT.md').open('a') as f:
    f.write('\n源码提交：`7d65ed1`（内存引用修复）、`4d083a5`（轮询显式开启）。'
            '测试后 Go 源码与二进制 SHA 均未变；仅 README 更新轮询默认值说明，'
            '构建时与最终文档 SHA 分别保留在 candidate-source.json 和 candidate-final-source.json。\n\n'
            '完整大数据 fixture 已逐文件校验后归档到实验目录，见 '
            '`candidate-fresh-large-retry-archive.json`；原始测试目录释放以恢复测试盘空间。'
            '中断尝试单独归档并保留，不混作成功结果。\n\n'
            '`SHA256SUMS.json` 对本目录文本按 LF 规范化后计算 SHA-256，'
            '二进制与源快照的原始字节 SHA 则保留在各自身份记录中。\n')
manifest={str(p.relative_to(target)):hashlib.sha256(p.read_bytes().replace(b'\r\n',b'\n')).hexdigest()
    for p in sorted(target.rglob('*')) if p.is_file()}
(target/'SHA256SUMS.json').write_text(json.dumps(manifest,indent=2)+'\n')
print(json.dumps({'review_directory':str(target),'files':len(manifest)}))
