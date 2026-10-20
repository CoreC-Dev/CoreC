# CoreC 后端功能完整性审计（功能面 × 合理性 × 完整性 × Bug × 增删建议）

> **审计目的**：完整审视 CoreC 后端每个大功能面所实现的东西是否合理、是否完整有逻辑、是否有 bug、需要增加或删除哪些功能，形成后续优化的权威输入。
> **特别关注**：可观测性 —— 用户评价"貌似目前就是暴露了几个 API 而已，并不全面详实有用"。本审计对其做了全量盘点与公平核实（见第 2 章）。
>
> **审计基线**：
> - CoreC @ `aee3946`（2026-10-18，工作树干净）
> - CoreC-Dashboard @ `34fb275`（2026-10-19，工作树干净）
> - 代码健康基线：`go build ./...`、`go vet ./...` 全绿；`go test -short ./...` 25 个包全部通过（测试期间实测）。
>
> **审计方法**：
> 1. **主审计第一手深读全部核心源码**：8 个功能面（配置 / 数据管线 / 弹性与控制 / 南向驱动 / 北向传输 / 规则引擎 / 控制面 API / 可观测性）的每条发现均由主审计逐文件核对实现与测试得出，无一凭文档或注释转述——包括跨层调用链的端到端追踪（如 /write → engine → 驱动编码的 scale 反演核查、告警从规则动作到前端消费的完整链路）。
> 2. 历史审计（audit-features-crud / audit-phaseB-logic / audit-baseline-verification）的每条发现逐项复核当前状态（见第 12 章），本轮只报告新问题。
> 3. 严重度定义：P0=数据丢失/错误数据/安全漏洞/panic；P1=生产故障难排查、功能明显缺口、可靠性隐患；P2=值得做的改进；P3=打磨项。
> 4. 置信度标记：**已验证**=逐行追踪过完整逻辑链；**疑似**=有代码气味、需运行时确认。本文档所有发现均为**已验证**级，无"疑似"条目。

---

## 目录

- [0. 总体结论](#0-总体结论)
- [1. 功能面总览与判定](#1-功能面总览与判定)
- [2. 可观测性专项审计（用户重点关注）](#2-可观测性专项审计)
- [3. 配置子系统](#3-配置子系统)
- [4. 数据管线（databus/调度/batcher/缓存/派生）](#4-数据管线)
- [5. 弹性与控制（重连/降级/缓冲/死信/写路径/关停）](#5-弹性与控制)
- [6. 南向驱动（modbus/S7/OPC UA）](#6-南向驱动)
- [7. 北向传输（MQTT/HTTP Push/Notify/parser）](#7-北向传输)
- [8. 规则引擎](#8-规则引擎)
- [9. 控制面 API（REST/WS/认证/限流）](#9-控制面-api)
- [10. 横切议题](#10-横切议题)
- [11. 优化路线图（合并总表）](#11-优化路线图)
- [12. 与历史审计文档的对照](#12-与历史审计文档的对照)

---

## 0. 总体结论

CoreC 后端的**工程底子很好**：构建/vet/测试全绿，历史两轮审计的发现全部闭环且带回归测试，数据面的可靠性机制（重连/熔断/调度降级/离线缓冲/死信/写审计）完整自洽。**但以"生产环境够用"为标尺，本轮审计发现的问题呈现清晰的三层结构**：

1. **一个 P0 正确性漏洞**：写路径不做 scale/offset 反演（DRV-1）。读值 `×0.01` 的 tag，操作者写工程值 1.5，PLC 收到的就是 1.5 而非 150——**控制指令以错误数量级下发且显示"写成功"**。这是控制系统级错误数据，必须最优先修复。
2. **可观测性是"半成品"，用户的批评在关键处成立**：指标骨架（26 族 + 直方图 + 双探针）质量不差，但存在语义错误（`points_per_second` 是启动均值不是速率）、粒度断崖（延迟直方图无 driver 标签，单驱动劣化被稀释）、关键状态只进日志不进指标（调度降级/死信深度/入站丢弃）、**告警退化为日志行**（无状态/无历史/无 API）、追踪默认级别下完全不可用。"暴露了几个 API"的观感来自：真正排障要靠翻日志 + 轮询 JSON，而不是指标告警。
3. **热重载与运维动作面有一组系统性暗坑**：三类配置（derived-tags/node/buffer）改了不生效且无警告；reload 失败半应用无善后；PUT /configs 一切错误掩码 500；运维基本动作（暂停驱动、查配置变更历史、按过滤查数据）没有 API 面。

合计发现 **P0×1 ｜ P1×11 ｜ P2×19 ｜ P3×10**，全部带 文件:行号 级证据，逐项修复建议见第 11 章路线图。

---

## 1. 功能面总览与判定

| 功能面 | 一句话判定 | 核心问题（编号见各章） |
|:---|:---|:---|
| 2. 可观测性 | 骨架好、语义错、粒度粗、关键状态不可见——"不全面详实有用"的批评在排障场景成立 | OBS-1~13 |
| 3. 配置子系统 | 校验链上乘；**热重载覆盖面与失败善后是系统性短板** | CFG-1~7 |
| 4. 数据管线 | 背压/优先级/质量语义扎实；**生命周期末尾一致性缺清理** | PIPE-1~5 |
| 5. 弹性与控制 | 正确性成熟度最高的一章；剩洞全在"可运营性" | RES-1~5 |
| 6. 南向驱动 | 协议实现完整；**藏着一个全审计唯一的 P0（写不反演）** | DRV-1~4 |
| 7. 北向传输 | 发布/重试/命令安全达标；入站姿态不一致、缺 LWT | TRA-1~5 |
| 8. 规则引擎 | 求值语义可靠；动作语义有文档发明、错误反馈面窄 | RULE-1~4 |
| 9. 控制面 API | 安全细节认真；错误分类、运维端点、查询能力是短板 | HUB-1~7 |
| 10. 横切 | 文档 5 处失实（含加剧 DRV-1 风险的那处）；e2e 有盲区 | 10.2/10.3 |

---

## 2. 可观测性专项审计

用户的评价是"貌似目前就是暴露了几个 API 而已，并不全面详实有用"。**审计结论：这个判断对了一半。** CoreC 的可观测性不是"几个 API"——它有一套手写的 Prometheus 文本指标体系（约 26 个指标族）、双健康探针、4 条 WebSocket 实时流、pprof、W3C traceparent 传播、写审计历史与死信队列，工程实现质量高（直方图累计桶、STW-free 的 runtime/metrics、慢消费者丢弃计数等都做对了）。**但"不全面详实有用"的批评在关键处成立**：指标语义有错误（伪速率）、粒度不够（延迟直方图无标签、无 per-driver 新鲜度）、关键状态只进日志不进指标（降级/超限/死信深度/暂停）、告警没有闭环（退化为日志行）、追踪实际不可用（默认级别下全部丢弃）、入站丢弃的命令完全不可见。以下逐项展开。

### 2.1 现状盘点（全量，均已核对源码）

**暴露面**

| 能力 | 端点 | 认证 | 实现位置 |
|:---|:---|:---|:---|
| Prometheus 指标 | `GET /metrics`（文本 0.0.4） | 是 | `hub/route/metrics*.go` |
| JSON 引擎统计 | `GET /stats` | 是 | `hub/route/stats.go` → `engine/stats_collector.go:54` |
| 存活/就绪探针 | `GET /healthz/live`、`/healthz/ready`（未就绪含 reason+components） | 否 | `hub/route/health.go` |
| 实时日志流 | `WS /logs`（slog 全捕获 fan-out） | 是 | `hub/route/logs.go`、`log/log.go` |
| 实时吞吐流 | `WS /traffic`（周期推送累计计数） | 是 | `hub/route/traffic.go` |
| 实时数据流 | `WS /tags/stream`（`?driver=` 过滤） | 是 | `hub/route/stream.go` |
| 内存/GC 流 | `WS /memory`（runtime/metrics 采样） | 是 | `hub/route/memory.go` |
| 性能分析 | `GET /debug/pprof/*`（主端口带认证；可配独立端口无认证；可禁用） | 可配 | `hub/route/router.go:104` |
| 写审计历史 | `GET /write/history`（最近 1000 条，成功+失败，新→旧，内存态） | 是 | `hub/route/write_history.go` |
| 死信队列 | `GET /write/failed`（重试耗尽的写命令，1000 条封顶） | 是 | `hub/route/tags.go:115` |
| 分布式追踪 | W3C `traceparent` 解析/注入 + span 计时 | — | `common/trace/` |

**指标族清单**（`/metrics` 输出，26 族）

| 族 | 类型 | 标签 | 来源 |
|:---|:---|:---|:---|
| `corec_reads_total` / `corec_publishes_total` / `corec_errors_total` / `corec_dropped_total` / `corec_log_dropped_total` | counter | — | `stats_collector.go:82-95` |
| `corec_drivers` / `corec_transports` / `corec_rules` / `corec_uptime_seconds` / `corec_points_per_second` | gauge | — | 同上 |
| `corec_driver_read_total{driver,type}` / `errors_total{driver}` / `reconnect_total{driver}` / `tags{driver}` / `connected{driver}` | counter/gauge | driver | `metrics_driver.go` |
| `corec_transport_published_total{transport,type}` / `failed_total` / `received_total` / `queue_size` / `connected` / `dropped_commands_total` | counter/gauge | transport | `metrics_transport.go` |
| `corec_offline_buffer_pending` / `drained_total` / `pushed_total` / `evicted_total` | gauge/counter | — | `metrics_engine.go:14-43` |
| `corec_flush_batches_dropped_total` | counter | — | `metrics_engine.go:49-58` |
| `corec_http_requests_total{method,status}` / `corec_http_request_duration_seconds` | counter/histogram | method,status | `metrics_http.go` |
| `corec_read_latency_seconds` / `corec_publish_latency_seconds` / `corec_data_age_seconds` | histogram | **无标签** | `metrics_engine.go:63-98` |
| `corec_goroutines` / `mem_heap_alloc_bytes` / `mem_heap_sys_bytes` / `mem_stack_inuse_bytes` / `mem_total_alloc_bytes` / `gc_count` / `gc_pause_total_seconds` / `cpu_count` | gauge | — | `metrics_runtime.go` |

**配套状态 API**：`/drivers/{name}` 返回 `last_read`、`last_error`、`read_count`、`error_count`、`reconnect_count`（`core/driver.go:57-67`）；`/tags` 返回缓存值含 `is_stale` 陈旧标记。

### 2.2 三个真实排障场景推演（检验"够不够用"）

**场景 A："plc2 的温度点 10 分钟没更新了，为什么？"**
- 现有工具：`/drivers/plc2` 能看 `last_read` 与 `last_error`（JSON，已验证）；`/tags` 能看 `is_stale`。**但两者都没有指标化** —— 没有 `corec_driver_last_read_timestamp_seconds` 这类 gauge，Prometheus 无法直接对"驱动停止读取"告警，只能靠 Dashboard 人肉盯或外部脚本轮询 JSON。
- 如果原因是**调度器自动降级**（连续 5 次失败把周期拉长 10 倍，`engine/scheduler.go:312-322`）或**调度任务 overrun**（读耗时超过周期，`scheduler.go:268-284`），这两个状态**只写日志，没有任何指标/API 暴露**，`degraded`、`consecutiveErrors`、`overrunCount` 都是 taskRunner 私有字段。而且 overrun 计数在节流日志触发后被清零（`scheduler.go:283`），历史累计直接丢失。
- 结论：**可以回答，但只能翻日志 + 轮询 JSON，不能监控告警。** 这正是"不全面详实"的典型体验。

**场景 B："MQTT 断连那 20 分钟，丢了多少数据？"**
- offline buffer 四指标（pending/pushed/drained/evicted）+ `corec_flush_batches_dropped_total` 设计得很好（阶段 B 审计 B2.1 的逐出可见性已落地），**这部分能回答**。
- 但 `corec_dropped_total` 是四类丢弃的总和：DataBus.Push 丢弃 + PushDropped + HighPriorityPushDropped + 日志丢弃（`stats_collector.go:79`），**无来源标签**，归因困难；bad-quality drop 走的是 `total_errors`（`processing.go:125-127`）而不是 dropped，语义上"被丢弃的坏质量点"混进了错误数。数据点到底在哪一层丢的，需要差分多个计数器才能反推。
- 结论：**大体能回答，但归因体系是混账的。**

**场景 C："这条写 PLC 的命令为什么没生效？"**
- `/write/history`（成功+失败审计）+ `/write/failed`（死信）+ 调度日志可以回答**已到达引擎**的命令。
- 但 MQTT 命令通道满时的**入站丢弃走的是 transport 本地死信**（`transport/mqtt/publisher.go:97-105` `commandDeadLetter`），**该队列没有任何 API 暴露**（全仓 grep 仅 transport/mqtt 及其测试引用 `CommandDeadLetterEntries`）—— 入站丢弃的命令实际上不可见，只有一个 `corec_transport_dropped_commands_total` 计数器告诉你"丢过 N 条"，但丢了什么、错误是什么，查不到。
- 死信队列深度没有指标（无法对"死信堆积"告警）；写路径没有延迟直方图（读/发布有，写没有）。
- 结论：**能查但链条有盲区。**

### 2.3 已验证的问题清单

| # | 问题 | 级别 | 证据 | 影响 |
|:--|:---|:---|:---|:---|
| OBS-1 | **`corec_points_per_second` 是启动以来平均值，不是"当前速率"**。HELP 写"Current data points processed per second"，实现是 `totalRead / uptime.Seconds()` | P1·已验证 | `engine/stats_collector.go:70-73`；`hub/route/metrics.go:61-62` | 采集停摆时该值不归零，只缓慢衰减（运行越久衰减越慢）。用它做"断流告警"完全失效；这是指标语义错误，且 docs/guide/observability.md:45 同样宣称"当前数据处理速率" |
| OBS-2 | **延迟直方图无标签，全驱动/全传输聚合**。`readLatency`/`publishLatency`/`dataAge` 是引擎级单例 | P1·已验证 | `common/metrics/latency.go`（单实例）；`stats_collector.go:31-39`；`metrics_engine.go:63-98`（渲染无标签） | 无法回答"哪个驱动慢/哪个 broker 慢"；单驱动网络劣化被其他驱动平均稀释，P99 告警基本不可用 |
| OBS-3 | **死信队列深度无指标**，且 MQTT 入站丢弃的 transport 本地死信（`commandDeadLetter`）**无任何 API 暴露** | P1·已验证 | 全仓 grep `CommandDeadLetterEntries` 仅 `transport/mqtt/*` 与测试；`/write/failed` 只读 engine 层队列（`hub/route/tags.go:115`） | 命令通道满导致的入站丢弃完全不可查（丢的是什么、为什么）；死信堆积无法监控告警 |
| OBS-4 | **告警没有闭环**：`alert` 动作 → `OnAlert` 回调 → main.go 里唯一 handler 是 `slog.Warn("ALERT",...)`；Dashboard 靠 `/logs` WS 流按日志级别过滤"告警" | P1·已验证 | `cmd/corec/main.go:90-97`；`engine/processing.go:348-357`；前端 `AlertStreamProvider`/`alertStore`（过滤 level≥4 的日志行） | 告警=一条日志行：无告警状态（active/acked/cleared）、无历史查询 API、无去重/冷却（notify 渠道的 cooldown 只覆盖显式配置 notify transport 的路径）、和普通 WARN 日志无法区分。规则命中风暴时唯一保护是日志节流 |
| OBS-5 | **调度器运行状态不进指标**：`degraded`/`consecutiveErrors`/`overrunCount` 是 taskRunner 私有字段，只有节流日志；overrun 计数还在日志后被清零 | P1·已验证 | `engine/scheduler.go:63-65,268-284,312-322` | "为什么这个 tag 变慢/停更"只能翻日志；降级状态无法监控；overrun 无累计指标 |
| OBS-6 | **`corec_dropped_total` 是四类丢弃的总和且无标签**（databus push 丢弃+push 丢弃+高优先级丢弃+日志丢弃）；bad-quality 的 drop 策略计入 `total_errors` 而非 dropped | P2·已验证 | `engine/stats_collector.go:79`；`engine/processing.go:125-127` | 丢弃归因困难；"按丢失原因"的看板/告警做不出来 |
| OBS-7 | **统计双轨制**：`engine/statistic.Manager` 与 `statsCollector` 原子计数并行记录同类事件，但 Manager 生产代码从不读取（仅测试断言），包级 `DefaultManager` 无人使用；且两套口径不等（chained transport 入站点计入 totalRead 但不进 statManager） | P2·已验证 | `scheduler.go:259` vs `lifecycle_support.go:183`；`transport_manager.go:278`；grep `statManager`/`statistic.` 全部写入点 vs 读取点 | 死代码 + 口径漂移的温床：未来有人读 Manager 会拿到与 /stats 不同的数 |
| OBS-8 | **`core.Metrics` 端口已定义、modbus 驱动已实现 `SetMetrics`，但唯一注入的是 `NoopMetrics`** | P2·已验证 | `core/metrics.go:9-32`（自注"NOT yet injected"）；`driver_manager.go:75`；`modbus_base.go:163` | per-driver 细粒度指标的"最后一公里"没接通，驱动侧埋点永远丢弃 |
| OBS-9 | **追踪实际不可用**：span 仅 debug 级别输出（`noopSpan` 优化），无 OTLP 导出；生产默认 info 级别下 span 全部丢弃 | P2·已验证 | `common/trace/trace.go:73-80`；README:54 已注明 | "分布式追踪"退化为 traceparent 解析 + trace_id 进日志；跨服务排障链路（读→规则→发布）没有真实 span 记录 |
| OBS-10 | **累计值以 gauge TYPE 暴露**：`corec_mem_total_alloc_bytes`、`corec_gc_count`、`corec_gc_pause_total_seconds` | P3·已验证 | `metrics_runtime.go:60-69`；docs/guide/observability.md:125-127 承认 | 违反 Prometheus 惯例（单调累计应为 counter）；不影响正确性，影响生态工具的语义识别 |
| OBS-11 | **`WS /traffic` 推送的是累计计数**（read/publish/dropped 三列），README 宣称"实时吞吐量统计（每秒推送）" | P3·已验证 | `hub/route/traffic.go:40-44`；README:240 | "每秒推送"≠"每秒速率"；客户端要自己差分，重启归零时差分出现错误值；服务端做速率更可靠 |
| OBS-12 | `/metrics` 位于认证组，scraper 必须携带 API secret；无 metrics 专用只读凭证/来源白名单 | P2·已验证 | `hub/route/router.go:94-98`；docs 已给 bearer_token 配置 | 运维摩擦+安全权衡：把能抓全部控制面（含 PUT /configs）的 secret 交给每台 Prometheus |
| OBS-13 | 无 per-tag / per-group 粒度指标（设计上避免基数爆炸，合理），但**没有提供任何替代**（top-N 慢点、按 group 汇总、可开关的高基数模式） | P2·设计缺口 | — | 数千 tag 场景下"哪些点异常"从指标体系无法回答，只能轮询 /tags |

### 2.4 生产级可观测性路线图（按优先级）

**P0（不做就无法监控）**
1. 修复 `corec_points_per_second` 语义：改为滑动窗口速率（如 60s 窗口差分），或直接删掉让用户 `rate(corec_reads_total[1m])`（推荐后者——Prometheus 本来就是干这个的，指标自带的伪速率反而误导）。
2. 新增 `corec_driver_last_read_seconds`（gauge，per-driver，距上次成功读取的秒数）与 `corec_driver_last_read_timestamp_seconds`——这是"数据停更"告警的基础。DriverStatus 已有 last_read 字段，只差暴露。

**P1（补齐"能告警、能归因"）**
3. 延迟直方图加 `driver`/`transport` 标签（或按驱动分桶的 per-driver 实例表）。
4. `corec_dead_letter_queue_size` gauge + `corec_dead_letter_pushed_total` counter；把 MQTT transport 本地死信并入统一死信 API（`GET /write/failed?source=ingress`）或至少暴露查询端点。
5. 调度器状态指标：`corec_scheduler_degraded{driver}`、`corec_scheduler_overruns_total{driver}`（不清零）、`corec_scheduler_consecutive_errors{driver}`。
6. dropped 指标拆分来源：`corec_databus_dropped_total{channel=priority\|normal}`、`corec_bad_quality_dropped_total` 等，`total_errors` 不再吞 bad-quality drop。
7. 告警一等公民化：`/alerts/stream` WS + `/alerts` 历史（含 active 状态、触发规则、计数、last-fired）+ `corec_alerts_total{rule}` 计数器；告警去重/冷却放引擎层，而不是只依赖 notify 渠道。

**P2（体系化）**
8. 接通 `core.Metrics` 端口：驱动层按需上报协议级指标（modbus 异常码、OPC UA 订阅 keepalive 等），由 engine 汇聚到 /metrics。
9. 配置变更事件流：`corec_config_reloads_total{result=ok\|failed}` + 变更审计日志端点（谁在何时 PUT 了什么——目前只有 slog.Info）。
10. 写路径直方图 `corec_write_latency_seconds{driver}` 与命令处理延迟（transport 收到→驱动完成）。
11. metrics 独立监听地址/只读 token（或至少文档化 Prometheus 专用 secret 的替代方案：IP 白名单中间件）。
12. offline buffer 字节水位 gauge（现有 pending 是批次数，磁盘字节数无界是 B2.1 已知问题，指标应同时暴露 bytes）。

**P3（打磨）**
13. runtime 累计指标改 counter TYPE；`/traffic` WS 改推速率（服务端差分）；GC pause 直方图而非 midpoint 估算总和。

### 2.5 对"是否要引入 prometheus/client_golang"的评估

手写文本格式目前没有正确性错误（累计桶、+Inf、转义、Content-Type 均验证无误），且符合项目"零依赖"理念。**不建议为此引入 client_golang**；真正的问题不在格式而在**语义与覆盖**（上面的 OBS-1/2/6 与路线图）。若未来要 OpenMetrics/exemplar/进程指标标准化，再评估引入。

---

## 3. 配置子系统

### 3.1 实现现状（盘点）

- **加载链**：`config.Load` → 解析 YAML → `expandEnvVars`（tree-based：parse→expand→re-marshal，非字节级替换）→ `validate`（`config/validate.go:29-57`）。另有 `LoadNoValidate`/`ParseNoValidate` 供 reload 链先解析后校验。
- **校验面**（`config/validate.go`）：buffer 路径/容量、driver 名唯一+类型已注册+至少一个 tag、tag 名唯一+地址/类型非空+数据类型合法+interval/read-timeout 为正、api.secret 必填且 ≥8 字符、transport 名唯一+类型已注册+**误把 batch/retry 字段塞进 settings 的检测**（288-292 行，错误信息直接教用户怎么改缩进）、rule 名唯一+动作合法+**drop+continue 互斥**+target 引用存在、derived-tag 结构校验。
- **非阻塞告警**：`IdleWarnings`（validate.go:110-162）——无 api.listen/无数据源/无传输/批量无 flush-interval 尾批滞留（B1.1 闭环），启动与 reload 都会输出，`POST /configs/validate` 也返回。
- **热重载**（`hub/executor`）：`PUT /configs`（path 或 payload）→ 解析 → `config.Validate` → `ApplyConfig`（diff 驱动/传输/规则）→ 持久化（0600 + chmod 收紧 + 落盘前自动快照备份）。`resolveConfigPath` 防路径穿越（executor.go:110-149）。备份：列表/创建/读取/恢复四端点，恢复走完整校验链。`PATCH /configs` 仅支持 `log-level`（executor.go:246-248）。
- **密钥面**：`GET /configs` 只回 summary + `secret-set` 布尔（configs.go:18-42）；`GET /configs/raw` 明文全量（executor.go:87-103，有意为之）。

### 3.2 评估

配置链是整个后端工程化程度最高的子系统之一：fail-fast 校验、防穿越、权限收紧、备份恢复、错误信息可操作性都属上乘。**系统性短板集中在"热重载的覆盖面"与"失败的善后"**——见下。

### 3.3 发现的问题

| # | 问题 | 级别 | 证据 | 影响 |
|:--|:---|:---|:---|:---|
| CFG-1 | **热重载覆盖面有暗坑：`derived-tags`、`node`（自动发现）、`buffer`（离线缓冲）三类配置变更被静默忽略**。`ApplyConfig` 只 diff drivers/transports/rules（executor_apply.go:52-73）；`warnRestartRequired` 提示了 api/rule-providers/rule-groups/engine 参数，**唯独没提这三类**（executor_apply.go:90-112） | P1·已验证 | `hub/executor/executor_apply.go:52-73,90-112` | 操作者在 Dashboard 改派生标签/发现配置/缓冲路径，PUT 返回成功、校验通过、无任何警告——但什么都没发生。其中 derived-tags 连"需重启"的警告都没有，是最隐蔽的一个 |
| CFG-2 | **失败的中途 reload 没有善后**：diff 半途失败（如删了旧驱动、新驱动起不来）返回错误但已删的不恢复，`currentCfg` 也不更新——`/configs/raw` 显示旧配置而引擎按新配置半运行，代码注释自认"state may be inconsistent until next successful reload" | P1·已验证 | `hub/executor/executor_apply.go:52-65`（部分应用后直接 return） | 配置视图与运行状态漂移，无自愈、无回滚、无 API 能告诉你当前真实组件集。一次手抖的 PUT 之后，"配置中心看到的就是假的" |
| CFG-3 | **PUT /configs 把一切失败都当 500 掩码**：解析错误、校验错误（明明是客户端错误）统一渲染成 `500 {"error":"internal server error"}`，真实原因只在服务端日志 | P1·已验证 | `hub/route/configs.go:178-187` → `common.go:75-82` | 操作者从 PUT 拿不到任何失败原因（必须先去 /configs/validate 或翻服务器日志）；非 Dashboard 的 API 用户更是无从下手。校验/解析类错误应 400 透传（含 warnings），仅内部 I/O 错误才 500 掩码 |
| CFG-4 | settings map 不做 schema 校验：驱动/传输的 `settings` 是 `map[string]any`，字段拼写/类型错误推迟到 `Init/Start` 才爆——在热重载场景下爆在已被 Suspend 的引擎中间 | P2·已验证 | `config/validate.go:287-292`（只挡字段错位，不校验 settings 内容） | 配置错误发现点后移；reload 时表现为"半应用失败"而非"校验不通过"，和 CFG-2 叠加 |
| CFG-5 | **密钥材料化陷阱**：`GET /configs/raw` 返回的是 env 展开后的内存配置；Dashboard 编辑后 PUT 回存，`${COREC_API_SECRET}` 之类引用被字面明文永久替换（payload 原样写盘，executor.go:224） | P2·已验证 | `executor.go:87-103,208-238` | 第一次经 Dashboard 保存后，所有秘密以明文落盘、且脱离了环境变量的轮换管理。需要文档明示或"保留引用"策略 |
| CFG-6 | PATCH 能力面极窄：`supportedPatchKeys` 只有 `log-level` | P2·已验证 | `executor.go:246-248` | README 宣称"修改运行时设置"，实际能改的只有日志级别；log-format、stale-threshold 等明显安全的运行时项都不支持 |
| CFG-7 | `RULE-SET:`/`SUB-RULE:` 引用完整性不在 config.validate 内：引用不存在的 provider/组，启动或 reload 时才报错 | P3·已验证 | `config/validate.go:299-340` 不查 rule-groups 引用；`rule/rule_build.go:247-254` 启动时才炸 | 错误发现点后移（有 /configs/validate 兜底，影响小） |

### 3.4 增/删建议

**建议增加**：① reload 的 diff 预览端点（PUT 前回答"这次改动会动哪些驱动/传输"）；② CFG-1 三类的热应用或显式 restart-required 警告（derived-tags 热应用技术上完全可行——它就是周期 ticker 集合）；③ 配置变更审计事件（谁、何时、PUT 了什么摘要）——写审计有 /write/history，配置变更没有对应物；④ PATCH 扩面（log-format、staleness-threshold）。
**建议删除/简化**：`validateDataSources` 空壳保留函数（validate.go:80-98，注释已说明是兼容占位）可以趁大版本清掉；其余无冗余。

---

## 4. 数据管线

### 4.1 实现现状（盘点）

- **DataBus**（`engine/databus.go`）：主通道 + 高优先级通道双轨（bufferSize/4，上限 1024）；满载**丢最旧保最新**，三类丢弃分开计数（`dropped`/`pushDropped`/`highPriPushDropped`）；Push 与 Close 竞态有 recover 防护且只吞预期 panic。
- **调度器**（`engine/scheduler.go`）：同 interval 的 tag 合并为一个任务（等价拼写如 "1s"/"1000ms" 归一，driver_manager.go:182-213）；自动降级（连续 5 错 ×10 间隔、下限 10s）与恢复；overrun 检测（读耗时>周期）与高频错误节流（10s 窗口）；高优任务走专用 worker（priority>0）。
- **处理循环**（`engine/processing.go`）：bad-quality 四策略（drop/mark/alert/ publish）→ cache 更新 → 订阅者广播 → 规则链（优先级序 + continue 短路，全部对**原始点**求值，transform 不重触发匹配）→ 动作执行。
- **LatestCache**（`engine/cache.go`）：64 分片；按 driver 的原子快照 + dirty 标志（正确处理了快照/更新竞态）。
- **派生标签**（`engine/derived.go`）：expr 编译（float 环境）、interval ≥1s、stale-guard、on-missing 三策略（skip/bad-quality/zero）、NaN/Inf 处理、每标签独立 ticker。
- **read_accessor / deadband**：deadband 数值型专用，坏质量与非数值穿透且不污染基线（B5-2/B5-3 修复落地，scheduler.go:347-397）。

### 4.2 评估

管线的核心语义（背压、优先级、交付语义、质量处理）经过两轮审计打磨，正确性扎实。**短板是"生命周期末尾"的一致性**：配置收缩（删驱动/删 tag）后，管线里残留的旧状态没有清理。

### 4.3 发现的问题

| # | 问题 | 级别 | 证据 | 影响 |
|:--|:---|:---|:---|:---|
| PIPE-1 | **缓存永不逐出已删除的资源**：`RemoveDriver`/删 tag 不清 `LatestCache`；`Clear()` 生产代码从不调用 | P2·已验证 | `engine/driver_manager.go:140-163`（无 cache 清理）、`cache.go:151-158`（只有全清）、grep `Clear()` 仅测试调用 | 删掉驱动后其 tag 的最后值**永远**留在 `/tags`（配 stale 阈值也只是标 stale，值还在）；删 tag 同理。派生标签还能继续消费这些幽灵值。运维者看到的 /tags 与真实管线不符 |
| PIPE-2 | **跨驱动同名 tag 在平铺视图里互相覆盖**：`GetAll()` 以 tag name 为键，"last one visited wins" | P2·已验证 | `engine/cache.go:130-148`（注释自认）；派生标签 buildEnv 用同一视图 `derived.go:166` | 两个 PLC 都有 "temperature" 时，`/tags` 只显示其一（不确定哪个）；派生标签引用 "temperature" 拿到的是不确定来源的值。工业现场同名 tag 极常见，应支持 `driver.tag` 限定 |
| PIPE-3 | **规则求值的运行时类型错误被静默吞成 false**（"不匹配"与"求值失败"不分）；preview 端点同样只回 `matched:false` 不报错 | P2·已验证 | `rule/expr.go:48-56`（`err!=nil → return false`）；`engine/rules.go:71-81`（preview 不透出 eval error） | `value > 90` 遇到 string 值的 tag：规则永远不触发、无任何信号（日志/统计/preview 三处都无声）。规则作者只能靠肉眼发现类型不匹配 |
| PIPE-4 | 派生标签无依赖/循环引用检测，运行时求值错误仅 `slog.Debug` | P3·已验证 | `engine/derived.go:279-297`（只查重名/空表达式）、`derived.go:178-181` | 互引的两个派生标签静默用旧值跑；生产 info 级别下求值失败完全不可见 |
| PIPE-5 | Suspend（reload 期间）tick 直接跳过、不补偿 | P3·已验证·设计取舍 | `scheduler.go:217-227`（`globalPaused → continue`） | 每次 reload 丢一个采集窗口内的数据点（毫秒级，多数场景可接受；文档未写明，值得一句注释级说明） |

### 4.4 增/删建议

**建议增加**：① tag/driver 级采集健康视图（per-tag last-update 年龄，API 层 /tags 已有 is_stale，但 per-driver 的"最老 tag 年龄"更利于告警）；② 驱动/任务级 pause-resume API（调度器有 `PauseDriver`，无任何路由暴露——见第 9 章）；③ 管道水位 gauge（dataBus 队列使用率，目前只有丢弃后计数，没有"快满了"的前置信号）。
**建议删除/简化**：`engine/statistic` 包整层（与 OBS-7 同源：双轨计数，生产无读者）——二选一保留即可，推荐保留 statsCollector 并把 scheduler 的 PushRead 改到同一口径。

---

## 5. 弹性与控制

### 5.1 实现现状（盘点）

- **重连**（`driverbase` + 各驱动）：指数退避、断路器（连续失败进 5 分钟低频档）、`handleConnLostIfCurrent` 代际校验（B4-1），读/写路径都触发重连（B4-2），TCP 半开兜底。
- **调度降级**：连续 5 错自动 ×10 慢采、恢复自动还原、高频错误 10s 节流聚合。
- **离线缓冲**（`engine/offlinebuffer.go`）：编号 JSON 文件 + 临时文件原子 rename + 数据 fsync + **目录 fsync**（崩溃一致性教科书实现）；容量逐出（最旧先丢）有计数有日志（B2.1）；回放按 Source 过滤 + `drainMu` 互斥（B2.3）；重启后 seq 续扫。
- **写路径**（`engine/write_manager.go`）：同步写 + 审计历史（1000 条）；命令监听带并发闸（信号量背压）、指数退避重试、**永久错误分类**（`isPermanentWriteError` 立即死信，B6-1）、stranded commands 统一死信（B3.2）、无本地驱动时命令转发（command-forward-topic，支持多跳）。
- **命令安全**（`transport/mqtt/command_handler.go`）：HMAC-SHA256 签名；**无 secret 时拒绝启动（fail-closed）**（publisher.go:294-302）；timestamp 新鲜度 + replay 窗口（含无 timestamp legacy 路径）+ `command-strict-replay` 强制模式。
- **fallback 传输**：主目标失败自动切备用（transport_manager.go:288-321），异步路径同样覆盖。
- **自动发现**（`engine/discovery.go`）：MQTT 心跳组网，TTL 驱逐，自动 AddTransport。
- **关停**：hub 先停 → engine Stop（组件倒序、每步超时、batcher flushFinal 带 shutdownBudget、排空语义文档化）→ 双 SIGINT 强杀。

### 5.2 评估

这是后端最"重"也最成熟的功能面：上面每一个子机制都有针对性测试（重连计数、死信回放、stranded、断路器）。**剩下的洞都在"可运营性"而非"正确性"**——出问题时的可见性与恢复手段。

### 5.3 发现的问题

| # | 问题 | 级别 | 证据 | 影响 |
|:--|:---|:---|:---|:---|
| RES-1 | **入站命令的丢弃盲区**：MQTT 命令通道满时，命令进 transport 本地 `commandDeadLetter`——该队列**没有任何 API/指标暴露**（`/write/failed` 只读 engine 层队列） | P1·已验证 | `transport/mqtt/publisher.go:97-105`、`command_handler.go:125-136`；grep `CommandDeadLetterEntries` 仅 transport/mqtt 与测试引用 | 消费洪峰时被丢的命令（丢弃内容、错误原因）完全不可查；只有一个 `corec_transport_dropped_commands_total` 计数器说明"丢过 N 条"。与 engine 死信队列（可查可重放）形成双标 |
| RES-2 | **死信队列是内存态**：1000 条封顶，进程重启即清零 | P2·已验证 | `engine/write_manager.go:36-40,402-409`（内存 slice）；对照：数据侧有落盘 offline buffer | 最需要死信的场景（崩溃/OOM/断电）恰恰会连同死信一起丢。至少应文档化"死信不保证持久"；更优：可选死信落盘 |
| RES-3 | **discovery 的 HTTP 端点类型未接线**：`endpoint.Type` 支持 "http"，实际只收集/宣告 MQTT | P2·已验证 | `engine/discovery.go:48-52`；`lifecycle_support.go:114-117`（"deferred to a future iteration"） | 配置了 HTTP 上行/下行的节点无法参与自动组网；文档若宣称 http 发现即为失实（未见文档宣称，属半成品而非错误） |
| RES-4 | **驱动暂停无 API**：调度器有 `PauseDriver`/`Resume`，但只在 AddDriver 内部使用，无任何路由暴露 | P2·已验证 | `engine/scheduler.go:152-162,182-188`；`hub/route/router.go` 全表无 pause 端点 | 运维标准动作"临时停采某 PLC"（换传感器、维修设备）只能通过整份改配置实现，绕一大圈且触发驱动重建 |
| RES-5 | `/write` 只有单条同步语义；驱动 `Write` 明明支持批量 | P3·已验证 | `hub/route/tags.go:47-109`（单命令）；`write_manager.go:77`（内部即批量接口） | 批量下置（如配方下发）需 N 次往返；无批量事务语义（哪怕"尽力而为"） |

### 5.4 增/删建议

**建议增加**：① 入站死信并入统一死信 API（或暴露 `/write/failed?source=ingress`）；② `POST /drivers/{name}/pause` + `/resume`（调度器能力已存在，只差路由）；③ 命令端到端延迟指标（transport 收到→驱动写完）；④ 死信落盘选项。
**建议删除/简化**：`engine/statistic` 包（同 PIPE-5 建议）；`discovery` 若短期不打算支持 HTTP 端点，可先把 `endpoint.Type` 的 http 分支清掉或标注 experimental。

---

## 6. 南向驱动

### 6.1 实现现状（盘点）

- **Modbus 全家族**（`driver/modbus/`）：TCP/RTU/RTU-over-TCP/UDP/RTU-over-UDP/TLS(mTLS)，四类寄存器区，bool→float64 全类型，线圈/保持寄存器写入，批量读优化（连续地址合并），地址解析带防错（线圈区拒绝非 bool 写入，modbus_write.go:66-74），重连/断路器完整。
- **Siemens S7**（`driver/s7/`）：DB/M/I/Q 区，位级 Read-Modify-Write（`writeMu` 序列化防并发位写丢失，s7.go:287-296），连续地址合并读（PERF-003 修复：同 DB+间隙≤32B 合并、段上限 480B——100 tag 从 100 次往返降到 1 次）。
- **OPC UA**（`driver/opcua/`）：NodeID 解析、轮询批量读 + 订阅双模式、写入、订阅通道满时丢弃计数。
- **driverbase**：重连退避/断路器/状态机/错误计数等公共底座。
- 三驱动一致：读取统一打本地时钟戳（B5-4 决策），scale/offset 读取变换统一走 `util.ApplyTransform`。

### 6.2 评估

驱动层的协议实现与重连状态机经过多轮打磨，S7 合并读这类深水区优化也做完了。**但有一个横跨三驱动的正确性漏洞（DRV-1），属于"错误数据下发"，是全审计最高优先级**。

### 6.3 发现的问题

| # | 问题 | 级别 | 证据 | 影响 |
|:--|:---|:---|:---|:---|
| DRV-1 | **scale/offset 读写不对称：读方向做 `value×scale+offset`，写方向不反演**。modbus 写直接编码 cmd.Value（modbus_write.go:76-105）；S7 `encodeS7Value(cmd.Value,...)` 无反演（s7.go:281-283）；OPC UA 同（opcua_write.go 无变换） | **P0·已验证** | 读方向：`modbus_read.go:78,121`、`s7_merge.go:282`、`opcua_read.go:108`、`opcua_subscription.go:126`；写方向三驱动均无逆变换；文档 `docs/config/drivers.md:457-472` 只描述读变换 | 带 scale 的 tag：UI 显示工程值 1.5（scale 0.01 的 pressure），操作者下发 1.5，**PLC 收到原始值 1.5 而不是 150**——写值错 100 倍且"写成功"。对控制系统这是物理层面的错误数据。修复：写路径反演 `(value-offset)/scale`（scale≠0 时），并提供 `raw` 显式逃生口 + 文档写明 |
| DRV-2 | **OPC UA 订阅回调硬编码 `QualityGood`**，丢弃服务器 StatusCode | P1·已验证 | `opcua_subscription.go:129-136`（`Quality: core.QualityGood` 写死，不读 `item.Value.Status`）；对照轮询路径有映射 `opcua_read.go:97-105` | 订阅模式下传感器报 Bad（节点不可用、超限）仍以"好质量"入管线：bad-quality 策略、死区质量穿透、告警规则全部失效——**同一驱动两条路径语义不一致**，坏数据被洗白 |
| DRV-3 | OPC UA 轮询路径把一切非 OK StatusCode 都折算成 `QualityBad`，`QualityUncertain` 类型存在但从未产生 | P3·已验证 | `opcua_read.go:97-105`；`core/types.go:105-107` 定义了 QualityUncertain | 不确定质量被当坏质量处理（丢弃/告警）；语义有损但方向保守，影响有限 |
| DRV-4 | 驱动层订阅通道满即丢（`subChannel` 满丢弃 + RecordError） | P3·已验证 | `opcua_subscription.go:137-147` | 丢弃有计数有日志，可接受；但丢弃的是订阅数据（事件不可再生，与轮询不同），值得单独指标区分 |

### 6.4 增/删建议

**建议增加**：① DRV-1 修复是第一优先级（三驱动 + 文档 + 测试一次性收口）；② modbus/S7 的写路径批量优化与读对齐（当前写是逐命令往返）；③ 驱动级 pause API（同 RES-4）；④ 订阅路径透传 OPC UA 的 SourceTimestamp 作为可选配置（`stamp-source: device\|local`，B5-4 决策保留默认 local）。
**建议删除/简化**：无需删除；`core.Metrics` 端口要么接通（驱动协议级指标：modbus exception code、opcua keepalive 间隔）要么摘掉接口定义（避免"看起来能埋点"的误导）。

---

## 7. 北向传输

### 7.1 实现现状（盘点）

- **MQTT**（`transport/mqtt/`）：QoS/retain/动态主题模板（`{{.Driver}}/{{.Group}}/{{.Tag}}`，缺省自动拼）；发布/订阅超时可配；断连行为与 auto-reconnect（默认开）+ 实例级 ±20% jitter 防重连风暴；命令订阅 HMAC fail-closed + replay 窗口 + strict 模式（B6-1 闭环）；命令转发（command-forward-topic + 独立签名）；data-topic 入站（chained-core）+ parser；TLS/mTLS；入站命令通道满→transport 本地死信 + dropped 计数。
- **HTTP Push**（`transport/httppush/`）：原生连接池、批量 JSON、5xx 重试/4xx 终止的分类、`traceparent` 注入、webhook 入站（可选 secret：Bearer 或 X-Webhook-Secret，常时比较）、10 MiB 体积上限。
- **Notify**（`transport/notify/`）：Slack/Teams（webhook JSON）、Email（SMTP + PLAIN auth）、Generic webhook（自定义 method/headers/模板——DingTalk/飞书走这个）；**per-rule cooldown** 防风暴（notify.go:120 以 `point.Metadata["rule"]` 为键）；429 专门识别。
- **parser**（`transport/parser/`）：入站数据点 JSON 解析（webhook/data-topic 共用），畸形样本容错。

### 7.2 评估

传输层的发布/重试/安全分类都达标，命令安全（fail-closed + HMAC + 防重放）是同类项目里少见的完整。**缺口集中在"入站姿态不一致"与"可运营性"**。

### 7.3 发现的问题

| # | 问题 | 级别 | 证据 | 影响 |
|:--|:---|:---|:---|:---|
| TRA-1 | **入站 webhook 默认无认证（fail-open）**：未配 `webhook-secret` 时任意 POST 的数据点直接进管线；与 MQTT 命令的 fail-closed（无 secret 拒绝启动）姿态相反 | P2·已验证 | `httppush/webhook.go:26-52`（`if t.webhookSecret != ""` 才校验）；对照 `mqtt/publisher.go:294-302`（无 secret 直接拒绝启动） | 数据面投毒：伪造测点经规则链洗白后可达云/MES。风险低于控制面但真实存在。建议至少对齐 MQTT：配置 webhook-addr 而未配 secret 时拒绝启动或打印显式启动告警（现在完全静默） |
| TRA-2 | **MQTT 无 LWT（遗嘱消息）**：全仓无 `SetWill` | P2·已验证 | grep `Will\|LWT` transport/mqtt 无结果 | CoreC 节点掉线时 broker 侧无感知，下游无法通过遗嘱主题感知网关离线；只能靠 scraping connected gauge。边缘场景标配能力缺失 |
| TRA-3 | webhook 批量 payload 逐条解析失败仅跳过，仍统一回 202；调用方不知道几条成功 | P3·已验证 | `httppush/webhook.go:73-88` | 批量上游无法感知部分失败；建议响应体带 accepted/skipped 计数 |
| TRA-4 | 入站命令丢弃进 transport 本地死信不可见（与 RES-1 同源，从传输视角：dropped_commands_total 有计数、无明细） | P1·已验证 | 同 RES-1 | 同 RES-1 |
| TRA-5 | paho 断连期间的 QoS0 publish 行为依赖 paho 内存 store 的窗口，engine 侧靠 publishTimeout+batcher 重试兜底 | P3·已验证·设计取舍 | `publisher.go:190-196`（publishTimeout 默认 5s）；dataflow.md 已写 at-least-once 语义 | 语义已文档化；QoS0 消息在断连窗口内丢失属预期，但值得在 transports 文档再显式提示一句 |

### 7.4 增/删建议

**建议增加**：① MQTT LWT 配置（`will-topic`/`will-payload`/`will-qos`）；② webhook fail-open 至少加启动告警（或对齐 fail-closed）；③ TRA-3 响应计数；④ MQTT 出站持久化 store 选项（文件 store，断连窗口 QoS1 更稳）。
**建议删除/简化**：无需删除。Notify 四渠道实现干净，generic webhook 已覆盖长尾渠道，不建议再加专用渠道。

---

## 8. 规则引擎

### 8.1 实现现状（盘点）

- **表达式**（`rule/expr.go`）：expr-lang/expr 之上 ~100 行 DSL 翻译层（`=~`/`!~`/`contains`/`suffix`/`prefix`/`in A..B`），带字符串字面量感知的范围替换；环境变量 `driver/device/group/tag/quality/type/value`；`ALL` 特判。
- **规则链**（`rule/engine.go` + `rule_build.go`）：优先级升序 + `continue` 短路；同优先级按配置序 tie-break（B7-1 修复，SliceStable + build 序号）；driver 过滤启发式（顶层 `driver == 'x'` 前缀才走 driver-index 快路径，其余按 universal——注释诚实标注这是保守启发式）。
- **五种动作**：forward/drop/alert/transform/mirror；transform 支持 expr + tag 重命名；drop+continue 配置互斥校验。
- **管理面**：命中/未命中统计（RuleStat）、运行时禁用（by name 优先 + legacy index）、preview（scratch 引擎隔离编译）、rule-providers（文件+周期热载）、rule-groups（子规则集引用）。
- **派生标签**与规则引擎解耦（独立 evaluator，见第 4 章）。

### 8.2 评估

作为配置驱动的路由/过滤引擎，能力面完整、执行语义（优先级/tie-break/短路）经得起推敲且测试充分。**两个概念级问题：动作语义有"文档发明"（RULE-1），错误反馈面太窄（RULE-2）。**

### 8.3 发现的问题

| # | 问题 | 级别 | 证据 | 影响 |
|:--|:---|:---|:---|:---|
| RULE-1 | **mirror 与 forward 在实现上完全等价**：processing.go 对两者走同一 `publishToTargets`；`targets` 数组对任何动作都生效（校验层对全部动作验证 Targets，validate.go:333-337）；文档却把 mirror 定义为"多目标复制"、forward 定义为"单目标"（docs/config/rules.md:133-204） | P3·已验证 | `engine/processing.go:186-188`（`case ActionForward, ActionMirror:` 同分支）；`rule/rule_build.go:295-299`（所有动作统一 targets 归一） | 五个动作实际只有四种行为；文档暗示的差异（复制/单发）不存在。要么赋予 mirror 真实语义（如默认 continue、或独立于 targets 的镜像目的地），要么文档改为"forward 同样支持 targets"，避免用户配置了以为不同而实同的东西 |
| RULE-2 | 求值错误静默 false（与 PIPE-3 同根，规则作者视角）：无错误计数、无错误日志、preview 不透出 | P2·已验证 | 同 PIPE-3 | 规则排障三无：不触发时不知道是"没匹配"还是"求值炸了" |
| RULE-3 | 表达式环境缺 `timestamp`/`metadata`，写不了时间窗/班次类规则 | P3·已验证 | `rule/expr.go:62-72`（pointEnv 七字段） | "仅白天告警""换班清零"这类常见工业需求无法在规则层表达，只能外置 |
| RULE-4 | transform 仅数值（非数值原样返回）；tag 重命名后的缓存/下游语义未文档化 | P3·已验证 | `processing.go:231-255` | string/bool 拼接变换、单位字符串后缀等场景不支持；重命名 tag 会在缓存里产生新键（旧键成幽灵值，与 PIPE-1 叠加），文档需写明 |

### 8.4 增/删建议

**建议增加**：① 求值错误计数进 RuleStat（`eval_errors` 字段）+ preview 返回 eval error（成本极低，价值高）；② 环境变量加 `timestamp`（与加 metadata 的风险是基数，timestamp 无此问题）；③ RULE-1 的语义澄清（改文档或改实现，二选一）。
**建议删除/简化**：mirror 动作考虑并入 forward（保留 alias 兼容一个版本）；`exprNode` 接口壳（expr.go:39-41，为兼容保留的包装层）可在大版本清理。

---

## 9. 控制面 API

### 9.1 实现现状（盘点）

- **路由面**（`hub/route/router.go`，共 28 条）：`/`、`/version`、`/healthz/live|ready`（免认证）；认证组内：configs 六端点（含 backups 四件套）、drivers 四端点（列表/单查/tags/test）、transports 三端点、tags、write 三端点（写/死信/历史）、rules 三端点（列表/禁用/预览）、stats、memory、四条 WS 流（logs/traffic/tags-stream）、metrics、pprof（可关/可独立端口）。
- **认证**（`middleware.go:181-222`）：单 Bearer secret；常时比较且先哈希到定长防长度泄露；空 secret fail-closed；`?token=` 仅限 WebSocket 升级请求（普通 HTTP 强制 header）；日志对 token 查询参数脱敏（safeRequestLogger）。
- **限流**（`middleware.go:57-152`）：per-IP token bucket（默认关，`rate-limit-per-sec` 配置），后台桶驱逐防 map 无界。
- **CORS**：未配置 origins 时 `*`（开箱即用），配置后 allowlist + Vary。
- **HTTP 指标**：全请求计数（method:status）+ 时长直方图，WS 升级的 Hijack/Flush 透传都处理了。
- **WS 流**：logs（slog 全捕获 fan-out + 慢消费者丢弃计数）、traffic（周期推累计值）、tags/stream（按 driver 过滤）、memory（runtime/metrics 免 STW 采样）。

### 9.2 评估

API 的安全细节（常时比较、token 限定 WS、日志脱敏、maxBody 限制、探针免认证独立于业务面）做得比大多数同类认真。**短板是 API 语义（错误分类）、运维动作的覆盖面（pause/审计/告警）和查询能力**。

### 9.3 发现的问题

| # | 问题 | 级别 | 证据 | 影响 |
|:--|:---|:---|:---|:---|
| HUB-1 | **限流键 = `r.RemoteAddr` 原样（含源端口）**：每条新 TCP 连接都是新桶 | P2·已验证 | `middleware.go:126`（`ip := r.RemoteAddr`，无 `SplitHostPort`） | "防 API secret 暴力破解"的设计目的失效——攻击者每次换源端口就拿到满额桶；正常客户端（长连接）反而被严格限流。修复一行：按 host 部分 keying |
| HUB-2 | PUT /configs 一切失败皆掩码 500（与 CFG-3 同源，API 视角：客户端可修复错误应 4xx） | P1·已验证 | 同 CFG-3 | 同 CFG-3 |
| HUB-3 | **资源级 CRUD 仍缺**：删除/重命名单个 driver、transport、rule 都要整份 PUT；规则重排没有专门端点 | P2·已验证 | `router.go` 全表；diff 链 `executor_diff.go:11-95` 只被整份 reload 驱动 | 对 API 消费者（非 Dashboard 的自动化脚本、上层 SCADA）不友好；前端已把整份 YAML 编辑当唯一路径（可接受），但后端没有提供更安全的窄接口与之配套 |
| HUB-4 | **配置变更无审计事件**：PUT /configs、PATCH /rules/disable、恢复备份都只有 slog.Info，无查询端点 | P2·已验证 | `configs.go:188-193`（仅日志）；对照写操作有 /write/history | "谁在什么时候把配置改坏了"只能翻日志。数据写有审计，配置写没有——而配置错的后果往往更大 |
| HUB-5 | 查询能力单薄：/tags 无 driver/tag 过滤、无分页；/write/history 固定全量 1000 条返回（无时间/驱动过滤） | P3·已验证 | `tags.go:38-44`（无 query 参数）、`write_history.go:21-32` | 数千 tag 场景 /tags 响应体积大且无法定向查询；审计历史无法按时间窗检索 |
| HUB-6 | 告警无 API 面：无 /alerts 端点（历史/状态/流），Dashboard 从 /logs 里按日志级别"凑"告警 | P1·已验证 | `router.go` 无 alerts 路由；前端 `AlertStreamProvider` 按 level≥4 过滤日志行 | 告警=日志行的现状让告警页与系统警告混为一谈（重连 Warn 也进告警流）；无状态（active/cleared）、无确认、无历史（见 OBS-4） |
| HUB-7 | 驱动暂停/恢复无端点（与 RES-4 同源，API 视角） | P2·已验证 | 同 RES-4 | 同 RES-4 |

### 9.4 增/删建议

**建议增加**：① `/alerts` API 面（stream + history + ack）——同时解决 OBS-4/HUB-6；② `POST /drivers/{name}/pause|resume`；③ 配置变更审计端点（复用 /write/history 的 bounded-ring 模式）；④ /tags 过滤与分页参数；⑤ PUT /configs 的 400 语义修复（同 CFG-3）。
**建议删除/简化**：`/traffic` WS 与 `/memory` WS 的价值部分重叠于 /metrics（可保留，但建议 traffic 改推速率，见 OBS-11）；无其他冗余。

---

## 10. 横切议题

### 10.1 安全姿态

| 项 | 现状 | 评估 |
|:---|:---|:---|
| 认证模型 | 单一 Bearer secret（≥8 字符强制），无用户/RBAC/轮换机制 | 对单实例边缘网关是合理取舍；多租户/多操作员场景会撞墙（已知边界，非 bug） |
| secret 比较 | sha256 定长 + hmac.Equal 常时比较（middleware.go:209-218） | 教科书级，赞 |
| token 传输 | header 强制；`?token=` 仅限 WS 升级（middleware.go:198-207）；日志脱敏（161-171） | 设计合理；反向代理日志仍会记录 query，文档已提示 |
| 限流 | per-IP token bucket，**但键含端口（HUB-1）** | 有 bug，见 HUB-1 |
| 入站认证姿态 | MQTT 命令 fail-closed；webhook 数据 fail-open（TRA-1） | 不一致，建议对齐 fail-closed |
| 配置文件 | 0600 + chmod 收紧 + 备份快照（executor.go:220-237） | 良好 |
| TLS | MQTT/驱动/API 均支持 TLS/mTLS | 覆盖完整 |
| 路径安全 | resolveConfigPath 防穿越（executor.go:105-149）、请求体大小限制（common.go:88-92） | 良好 |

### 10.2 文档一致性（doc-mismatch 汇总）

| 文档 | 失实点 | 证据 |
|:---|:---|:---|
| `docs/guide/observability.md:126` | 说 runtime 指标"直接读取 runtime.MemStats"，实现已是 runtime/metrics（STW-free） | `metrics_runtime.go:15-31` |
| `docs/guide/observability.md:45` | `corec_points_per_second` 标注"当前数据处理速率"，实现是启动均值 | OBS-1 |
| `README.md:240` | `/traffic` 称"实时吞吐量统计（每秒推送）"，推送的是累计计数非速率 | OBS-11 |
| `docs/config/rules.md:133-204` | mirror 定义为多目标复制、forward 单目标；实现两者等价且 forward 也接受 targets | RULE-1 |
| `docs/config/drivers.md:457-472` | scale/offset 只描述读变换，对写语义只字未提——而写不反演（DRV-1），文档缺失加剧风险 | DRV-1 |

### 10.3 测试与工程化健康度

- **实测**：`go build`、`go vet`、`go test -short`（25 包）全绿；`archtest` 结构约束在 CI 强制；tech-debt tracker 声称覆盖率 85.5%（audit-baseline-verification.md 曾独立复核过，本轮未重测——基线文档可信）。
- **e2e 覆盖**（`e2e/e2e_test.go` 仅 6 场景）：modbus→httppush、transform、alert、drop、write、reload。**缺口**：MQTT 传输无 e2e（单测深但无 broker 级链路）、offline buffer 断网回放无 e2e、chained-core 转发无 e2e（demo 有独立 main 但不在测试链）。DRV-1 这类横跨 API→驱动编码的漏洞，正是 e2e 盲区的产物——建议为"带 scale 的 tag 写回环"补一个 e2e。
- **结构测试**：字段/方法数门禁（engine/structure_test.go）已机械固化 God Object 拆分成果，这个做法值得保持。

### 10.4 性能备注

- S7 N+1 已修（PERF-003 闭环：100 tag 1 次往返）；modbus 批量合并同思路。
- `LatestCache` 分片 + 快照复用正确；`/tags` 全量序列化在数千 tag 时是 O(N) 锁扫描，可接受但配 HUB-5 的过滤参数更稳。
- 直方图无标签导致 P99 被"平均稀释"（OBS-2）——这不是性能问题，是可观测性结构问题，路线图已列。

---

## 11. 优化路线图（合并总表）

> 合并第 2–10 章全部已验证发现，按优先级排序。**P0=错误数据/数据丢失级；P1=生产可靠性/可运营性硬缺口；P2=重要改进；P3=打磨。** 每项给出来源编号（正文有完整证据与修复建议）。

### P0（控制系统错误数据，立即修）

| # | 工作项 | 来源 | 验收标准 |
|:--|:---|:--|:--|
| 1 | **写路径 scale/offset 反演**：三个驱动写编码前应用 `(value-offset)/scale`；提供 raw 写逃生口；文档补写语义；e2e 写回环测试 | DRV-1 | 带 scale 的 tag 经 /write 写工程值，设备寄存器收到正确原始值；raw=true 时跳过反演 |

### P1（不做就无法安全生产运营）

| # | 工作项 | 来源 | 验收标准 |
|:--|:--|:--|:--|
| 2 | 修复 `corec_points_per_second` 语义（推荐删除该指标，PromQL rate() 足矣；或改滑动窗口） | OBS-1 | 断流后 1 分钟内指标反映为 0/骤降 |
| 3 | 新增 `corec_driver_last_read_seconds` / `last_read_timestamp` per-driver gauge | OBS-5、2.2-A | Prometheus 可直接告警"驱动 N 秒未读" |
| 4 | 延迟直方图加 driver/transport 标签（per-driver 实例表） | OBS-2 | `corec_read_latency_seconds_bucket{driver=...}` 可查 |
| 5 | 死信可观测：`corec_dead_letter_queue_size` gauge + MQTT 入站死信并入 `/write/failed`（`?source=ingress`） | OBS-3、RES-1、TRA-4 | 死信堆积可告警；入站丢弃的命令可查明细 |
| 6 | **告警一等公民化**：`/alerts`（active 状态+历史+`/alerts/stream` WS）+ `corec_alerts_total{rule}` + 引擎层告警去重/冷却 | OBS-4、HUB-6 | Dashboard 告警页不再从日志级别过滤；告警有 active/cleared 状态 |
| 7 | 调度器状态指标：degraded/overruns_total（不清零）/consecutive_errors per driver | OBS-5 | 降级发生时可在 Prometheus 看到 |
| 8 | 热重载覆盖面修复：derived-tags 热应用（或至少进 warnRestartRequired）；node/buffer 显式警告 | CFG-1 | 改派生标签+PUT 后实际生效或收到明确警告 |
| 9 | reload 失败善后：diff 失败时回滚已删组件，或提供"引擎当前真实组件集"API 消除视图漂移 | CFG-2 | 失败后 /configs/raw 与 /drivers 一致 |
| 10 | PUT /configs 错误分类：parse/validate 失败 400 透传原因+warnings，仅内部错误 500 掩码 | CFG-3、HUB-2 | Dashboard 能展示具体校验错误 |
| 11 | OPC UA 订阅质量映射：读 `item.Value.Status` → Quality（Bad/Uncertain→Bad 保守映射亦可），与轮询路径对齐 | DRV-2 | 服务器报 Bad 时订阅点不再以 Good 入管线 |

### P2（重要改进）

| # | 工作项 | 来源 |
|:--|:--|:--|
| 12 | dropped 指标按来源拆分（databus{channel}/bad-quality/高优），bad-quality drop 不再混入 total_errors | OBS-6 |
| 13 | 删除或合并 `engine/statistic` 双轨计数（保留 statsCollector 单一口径） | OBS-7、PIPE-5 |
| 14 | 接通 `core.Metrics` 端口（驱动协议级指标）或摘掉死接口 | OBS-8 |
| 15 | 规则求值错误可见化：RuleStat 加 eval_errors、preview 透出 eval error、（可选）Debug 日志升 Warn 节流 | PIPE-3、RULE-2 |
| 16 | 缓存逐出：RemoveDriver/删 tag 时清理对应缓存条目 | PIPE-1 |
| 17 | 派生标签/`/tags` 支持 `driver.tag` 限定引用，消除跨驱动同名覆盖 | PIPE-2 |
| 18 | 限流键 SplitHostPort 修复 | HUB-1 |
| 19 | webhook 入站 fail-open 对齐 MQTT（未配 secret 时拒绝启动或显式启动告警） | TRA-1 |
| 20 | MQTT LWT 遗嘱消息支持 | TRA-2 |
| 21 | `POST /drivers/{name}/pause|resume` 端点（调度器能力已存在） | RES-4、HUB-7 |
| 22 | 配置变更审计端点（bounded ring，同 /write/history 模式） | HUB-4、3.4 |
| 23 | PATCH /configs 扩面（log-format、staleness-threshold 等） | CFG-6 |
| 24 | `/configs/raw` 秘密材料化问题：文档明示 + （可选）占位符回写策略 | CFG-5 |
| 25 | settings schema 校验钩子（per-driver/transport 类型校验提前到 validate） | CFG-4 |
| 26 | 资源级窄接口糖端点（DELETE /drivers/{name} 等，内部走 diff 链） | HUB-3 |
| 27 | metrics 独立监听/只读凭证或 IP 白名单方案 | OBS-12 |
| 28 | offline buffer 字节水位 gauge（批次数之外） | 2.4-12 |
| 29 | 死信落盘选项（崩溃后死信可查） | RES-2 |
| 30 | discovery HTTP 端点接线或移除半成品分支 | RES-3 |

### P3（打磨）

| # | 工作项 | 来源 |
|:--|:--|:--|
| 31 | runtime 累计指标改 counter TYPE（total_alloc/gc_count/gc_pause_total） | OBS-10 |
| 32 | `/traffic` WS 改推速率（服务端差分） | OBS-11 |
| 33 | mirror 语义澄清（改文档或并入 forward） | RULE-1 |
| 34 | 规则环境加 `timestamp` | RULE-3 |
| 35 | /tags 过滤分页、/write/history 过滤 | HUB-5 |
| 36 | webhook 批量响应带 accepted/skipped 计数 | TRA-3 |
| 37 | 批量写 API（driver.Write 本就支持数组） | RES-5 |
| 38 | 派生标签循环引用检测（编译期） | PIPE-4 |
| 39 | docs 同步：observability.md MemStats 表述、points_per_second 描述、mirror 文档 | 10.2 |
| 40 | `corec_transport_dropped_commands_total` 之外补 ingress 死信计数（并入 #5） | TRA-4 |

**明确不建议做的**：① 引入 prometheus/client_golang（2.5 节）；② 给 Notify 加更多专用渠道（generic webhook 已覆盖）；③ 为指标体系上 per-tag 全量标签（基数爆炸，用 per-driver + top-N 替代）。

---

## 12. 与历史审计文档的对照

本仓库此前已经历多轮审计与修复（`audit-features-crud.md`、`audit-phaseB-logic-1/2.md`、`audit-baseline-verification.md`、`ux-audit-*.md`，均为 2026-10-17 产物）。**本次逐项核对了旧发现在当前 HEAD（aee3946）的状态**——结论是历史闭环做得很好，本轮审计不必重复旧账：

| 历史发现 | 当前状态 | 证据 |
|:---|:---|:---|
| CRUD 缺口：rules/disable 仅按 index（漂移风险） | ✅ 已修复：name 寻址优先，index 保留为 legacy | `hub/route/rules.go:17-27,45-52` |
| drivers 无 test 端点 | ✅ 已修复：`POST /drivers/test` | `hub/route/router.go:72`、`hub/route/driver_check.go` |
| 无规则试算端点 | ✅ 已修复：`POST /rules/preview` | `hub/route/router.go:85`、`engine/rules.go:61-81` |
| configs 无备份/回滚 | ✅ 已修复：`/configs/backups` 全套（列表/创建/读取/恢复） | `hub/route/router.go:64-67`、`hub/executor/backups.go` |
| 写操作无审计历史 | ✅ 已修复：`GET /write/history`（1000 条，成功+失败） | `engine/write_manager.go:42-61,96-131` |
| B1.1 batcher 无 flush-interval 时尾批无限滞留 | ✅ 已修复：IdleWarnings 非阻塞告警 | `config/validate.go:128-162` |
| B2.1 offline buffer 逐出完全静默 | ✅ 已修复：evicted_total 指标 + 日志 | `engine/offlinebuffer.go:50-57,233-243`、`metrics_engine.go:31-42` |
| B2.3 多 transport 共享回放错投/重复 | ✅ 已修复：drainMu 互斥 + 按 Source 过滤回放 | `engine/offlinebuffer.go:33-38,247-265` |
| B3.0 第二次 SIGINT 被吞、关停卡死只能 kill -9 | ✅ 已修复：armForcedExit 强制退出 | `cmd/corec/main.go:104-115,158-167` |
| B3.2 关停中在途命令静默丢失 | ✅ 已修复：stranded commands 统一 dead-letter + 日志 | `engine/write_manager.go:153,179,193-224` |
| B5-1 bad-quality alert 策略在零规则时不触发 | ✅ 已修复：策略告警先于规则匹配 | `engine/processing.go:130-143` |
| B5-2/B5-3 deadband 吞坏质量点/污染基线、非数值冻结 | ✅ 已修复：坏质量与非数值一律穿透且不更新基线 | `engine/scheduler.go:347-397` |
| B6-1 无 timestamp 命令绕过重放检测；不可重试错误也重试 | ✅ 已修复：replayWindow 覆盖 legacy 路径（+`command-strict-replay` 强制）；permanent write error 立即死信 | `transport/mqtt/command_handler.go:50-113`、`engine/write_manager.go:290-299,341-355` |
| B7-1 同优先级隐式 universal 优先且排序不稳定 | ✅ 已修复：SliceStable + 配置序 tie-break + 专项测试 | `rule/rule_build.go:93-105`、`rule/tiebreak_test.go` |
| Finding 4 发布计数虚高（enqueue 即计数） | ✅ 已修复：onBatchPublish 按实际发布计数 | `engine/stats_collector.go:178-191` |
| B1.4 at-least-once 语义未写入文档 | ✅ 已修复：dataflow.md 明确记录两类重复来源与下游幂等建议 | `docs/architecture/dataflow.md:51-55` |
| B1.6/B3.4 关停竞态：processingLoop 与 batcher.stop 之间的静默丢失窗口 | ✅ 已修复：stopped 原子标志 + publishRejected 计数把静默丢失变成计数+日志 | `engine/batcher.go:86-118,101-114`（注释引用 B1.6/B3.4） |
| B4-1 陈旧读错误拆除刚重连成功的连接 | ✅ 已修复（三驱动一致）：`handleConnLostIfCurrent` 代际校验 + 回归测试 | `modbus_base.go:200-207`、`s7.go:144-151`、`opcua/client.go:234-241`，各驱动 *_test.go 回归 |
| B4-2 Write 路径连接类错误不触发重连 | ✅ 已修复（三驱动一致）：写错误经 `IsConnectionError` 分类后同样触发拆除重连 | `modbus_write.go:50-56`、`s7.go:266-272`、`opcua_write.go:94-107` |
| B5-4 staleness 时间源跨驱动不一致 | ✅ 已修复：三驱动统一在读取时打本地时钟戳，并写明拒绝 SourceTimestamp 的理由 | `opcua_read.go:79-86`、`opcua_subscription.go:109-115` |
| IMPROVEMENTS #1/#2/#5/#8（批处理背压可观测、命令并发闸、高优先级通道、per-instance 统计） | ✅ 已落地 | `metrics_engine.go:49-58`、`write_manager.go:26-34`、`processing.go:68-110`、`stats_collector.go:41-46` |

**结论**：历史阶段 B 的全部逻辑发现（B1.x–B7-1）与 CRUD 缺口在当前 HEAD 均已闭环，且大多附带回归测试。本轮审计据此**只报告新问题**。另注意：IMPROVEMENTS #8 的落地方式本身留下了双轨计数的新债（见第 2 章 OBS-7）。
