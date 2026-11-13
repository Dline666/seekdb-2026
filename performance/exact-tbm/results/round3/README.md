原始结果与 [ROUND3 报告](../../ROUND3.md) 配套，均为本地测试，无平台提测或分数回执。

- `uniform10k.jsonl`、`uniform100k.jsonl`、`outlier-final100k.jsonl`：合并候选与 af116 基线交错对照，`summary.json` 为汇总。
- `outlier100k.jsonl`：只应用分块界的独立 5 轮对照，见 `outlier-summary.json`。不要与最终离群实验混合分析，它们的轮次编号分别从零开始。
- `update-{0,1,2}-{round2,stream}.jsonl`：只应用流式 UPDATE 的独立 3 轮对照；每次完整执行真值、更新及恢复校验。`update-summary.json` 只汇总全表 UPDATE 阶段。
- `recovery-comparison.jsonl`：只应用压缩判断早退的独立 3 轮对照，每次逐文件清缓存并验证驻留页数。`recovery-summary.json` 为启动阶段，`recovery-total-summary.json` 还包含随后 2 条查询及退出。
- `stress-final.jsonl`：合并候选的超过 2GiB 数据完整压力记录；同一 cgroup 跨进程重启持续计入文件缓存。`stress-final-summary.json` 为简化视图。
- `tests.txt`、`race.txt`、`crash.txt`、`exact-edges.jsonl`、`tail.jsonl`、`constant.jsonl`、`seed29.jsonl`：测试、独立真值和随机崩溃证据。`driver-*-selftest.*`、`recovery-selftest.*` 是小规模驱动自检，不是大数据容量验证。

保留但不计入严格性能汇总的诊断：

- `stress-base.jsonl`：旧候选基础流程通过，随后全表 UPDATE 触发 cgroup OOM。初版驱动未确认未提交尾部已刷盘，且启动计时不完整，因此只用于内存/失败定位；数据库二进制仍是相同 Ubuntu Go 1.22.2 构建。
- `recovery-attempt-mapchecks.*`：预热阶段的点校验使用通用 SELECT，触发了昂贵的随机全表遍历。主代理在正式比较开始前手动 SIGKILL 终止该次测试，改用带 WHERE 主键条件的 exact TopK 校验后重新执行完整对照。这里的 EOF 是测试中止，不是额外 OOM 或正确性失败。

`binary-sha256.txt`、`compiler.txt` 标识各数据库及驱动二进制。`stress-initial` 对应第一项诊断，驱动为 Go 1.22.12；`stress-before-point` 对应中等规模 UPDATE 对照和早期驱动自检，使用 Go 1.22.2；最终 `stress` 驱动仅进一步把点校验改为带 ORDER BY/LIMIT 的 exact 查询，避免大数据的通用 SELECT 随机访问，计量与数据库算法不变。该驱动用于冷恢复对照和最终大数据压力。

压力记录中的 `memory_events` / `cpu_stat` 是工作流累计计数，`b_bytes` 是各阶段差量；`memory_peak_scope=phase_reset_fd` 表示同一 FD 重置并读取的阶段峰值。采样 anon/file 的最大值可能来自不同时间，不能相加。连续压力 T/B/M 与独立写入/查询综合 E 采用不同生命周期，不直接混合换算。
