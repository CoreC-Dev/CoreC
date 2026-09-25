# CoreC 项目改进建议

> 基于全量源码阅读 + `go build`/`go vet` 验证 + 关键路径代码核查。所有建议标注了具体代码位置与优先级。
> 先说结论：**这是一个工程质量相当高的项目**——测试文件数(78)多于源文件数(73)，几乎无 TODO/FIXME 残留，OfflineBuffer 已做 fsync+目录fsync+原子rename（崩溃安全），延迟直方图已实现并暴露。以下建议聚焦"从优秀到卓越"的改进空间。

---

## 一、实时性/性能（高优先级）

### 1. ⚠️ processingLoop 同步 Publish 阻塞 worker —— 最严重的实时性瓶颈

**位置**：`engine/engine.go:1244` (`processingLoop`) → `:1560` (`publishToTargets`)

**问题**：worker 从 DataBus 取出数据点后，**同步调用** `publishToTargets`，后者遍历所有目标传输并**同步** `Transport.Publish`。若某传输慢（MQTT broker 延迟、HTTP webhook 超时），该 worker 被阻塞，无法消费 DataBus 后续数据，导致缓冲堆积 → `pushDropped` 增长 → **数据丢失**。

一个慢传输会污染所有不相关的数据点：哪怕它们的目标传输很快，也得排在被阻塞的 worker 后面。

**改进**：
- **方案A（推荐）**：Publish 异步化。`publishToTargets` 把点投入 per-transport 的发布队列（带缓冲 channel），独立 publish goroutine 消费。worker 立即返回继续消费 DataBus。
- **方案B**：强制所有传输默认走 batcher（当前 batcher 仅在配置了 batchSize/flushInterval/retryCount/offlineBuffer 时启用，见 `batcher.go:72`）。⚠️ 注意：batcher 的 `publish` 并非总是非阻塞——`batcher.go:183 publish()` 在 `len(buffer) >= batchSize`（`:186`）时**同步**调用 `flush()`（`:190`，运行于调用方 goroutine，即 processingLoop worker，经 `engine.go:1600` 进入），`flush()`→`publishWithRetryAndBuffer`（`:206`）内含 `time.After(delay)` 指数退避 sleep（`:269`）。即 **buffer 未满时才非阻塞，buffer 填满（负载突发）时仍同步阻塞 worker**，故方案B 只是把阻塞推迟到 buffer 填满之际，不能真正解除 #1 瓶颈。当前 `publishToTargets` 对无 batcher 的传输仍走直发。
- **方案C**：Publish 超时与 DataBus 消费解耦——给每次 Publish 加 `context.WithTimeout`，超时即落 OfflineBuffer，不拖累 worker。

### 2. ⚠️ startCommandListener 单 goroutine 串行 + 重试退避阻塞

**位置**：`engine/engine.go:1314` (`startCommandListener`) → `:1370` (`executeWriteWithRetry`)

**问题**：每个传输的命令监听是**单个 goroutine**，`executeWriteWithRetry` 在其中**同步执行**（含退避 sleep 100ms→200ms→400ms，上限2s）。一条慢写（如 Modbus 写超时1s × 4次重试 + 退避）可阻塞该传输**数秒**，期间 `commandCh` 里堆积的后续命令全部延迟——**控制指令的延迟被串行化放大**。

更糟：`executeWriteWithRetry` 内 `forwardCommand` 先在 `e.mu.RLock` 下**快照** transports 切片（`engine.go:1345-1350`），随后**释放锁**（`:1350`），再在 `:1352-1363` 遍历并调用 `ForwardCommand`——遍历时**不持锁**，故 RLock 仅覆盖快照、不阻塞管理操作；但叠加在串行路径上的转发仍占时间。

**改进**：
- 命令执行并发化：listener 取出命令后投入 worker pool 并发执行，或每命令起独立 goroutine（需限流防雪崩）。
- 重试移入独立重试队列（异步），不阻塞 listener 主循环。
- 控制场景可配置"命令并发度"上限。

### 3. 规则线性扫描 O(N)

**位置**：`rule/engine.go` `Match`

**问题**：每个数据点遍历全部规则。代码注释说明是"为保证优先级全局有序 + 每条规则都能记 hit/miss 统计"而刻意选线性扫描。规则数 <100 时无感，但 >500 时每点多微秒级开销，在高频采集(200ms × 数千点)下累积可观。

**改进**：
- 加预筛索引：按 `Driver`/`Tag`/`Group` 建倒排，先缩小候选集再线性扫描（牺牲极少统计精度）。
- 或对 `Match: "ALL"` 这类通配规则走快速路径，不进扫描。
- 保持优先级有序：候选集内仍按优先级排序扫描。

### 4. 命令通道满直接丢命令

**位置**：`transport/mqtt/publisher.go:665`

**问题**：`commandCh`（默认 buffer 100）满时，命令被丢弃并告警。对**控制指令**而言，"丢"比"延迟"更危险——操作员下发急停却丢了，后果严重。

**改进**：
- 控制场景应可配置更大缓冲，或提供持久化命令队列（类似 OfflineBuffer 但用于命令）。
- 区分"控制命令"与"普通写"：控制类永不丢，宁可阻塞 MQTT 回调（背压到上游）。
- 死信队列已有，但"通道满即丢"发生在进死信队列之前——建议满时直接入死信队列而非静默丢。

### 5. 全局 worker 池无优先级隔离

**位置**：`engine/engine.go:154`（`numWorkers: runtime.NumCPU()` 赋值，可被 `cfg.Workers` 于 `:176` 覆盖）→ `:341-342`（`for i:=0; i<numWorkers; i++ { go e.processingLoop() }`，N=NumCPU 个 processingLoop）

**问题**：高频采集任务(200ms)与低频任务(5s)共享同一 worker 池。低频大批量数据（如 tags-file 几百点一次读回）可能瞬间占满 worker，让高频任务排队，破坏高频任务的周期确定性。

**改进**：
- 按 interval 分池：高频任务独占小池（如2-4 worker），低频任务用大池。
- 或加权调度：DataBus 改为优先级队列，高频任务的点优先出队。

---

## 二、架构/设计（中优先级）

### 6. core.Logger / core.Metrics 端口已定义但全局未注入

**位置**：`core/logger.go`、`core/metrics.go` 定义了端口；但 `engine/engine.go` 全程直接用全局 `slog.Error/Info/Warn`（如 `:221,232,245...`）。`QUALITY_ASSESSMENT.md:35` 也确认"未注入"。

**问题**：依赖倒置做了接口抽象却没落地。测试时无法注入 mock logger，日志级别/输出无法按实例隔离，多实例嵌入时日志混在一起。

**改进**：
- 引擎构造时注入 `core.Logger`，内部所有 `slog.X` 改为 `e.logger.X`。
- 同理 `core.Metrics` 端口落地，替换全局 `statistic.DefaultManager`（当前是包级单例，多实例会串数据）。

### 7. engine.go 单文件 1792 行过长

**位置**：`engine/engine.go`

**问题**：单文件承载引擎生命周期、驱动/传输管理、命令调度、发布、发现、配置自动填充、死信队列等十余个职责。`gocyclo` 在 `engine.go` 内有 **1 处** nolint（`:198` `Start`，complexity 26）；全仓共 **5 处** nolint:gocyclo，complexity 范围 22–48（48 在 `config/config.go:104`，22 在 `hub/executor/executor.go:173`，27 在 `engine/scheduler.go:178`，26 在 `driver/modbus/modbus_base.go:397`）。维护成本高，改动易引入回归。

**改进**：按职责拆分文件（同包内，不破坏接口）：
- `engine_lifecycle.go`（Start/Stop/Reload/Suspend/Resume）
- `engine_command.go`（startCommandListener/executeWriteWithRetry/forwardCommand/deadLetter）
- `engine_publish.go`（processingLoop/publishToTargets/tryFallback）
- `engine_discovery.go`（autoFillNodeConfig/startDiscovery）
- `engine_config.go`（applyEngineConfig）

### 8. statistic.DefaultManager 是包级全局单例

**位置**：`engine/statistic/manager.go:13`

**问题**：`var DefaultManager = NewManager()` 是进程级单例。若 CoreC 作为库被嵌入上层系统、实例化多个 Engine，所有实例的计数会混入同一个 Manager，统计失真。

**改进**：Manager 改为引擎实例字段（与 #6 配合，随 Logger/Metrics 一起注入）。

---

## 三、可观测性（中优先级）

### 9. 缺端到端延迟追踪，只有分段直方图

**现状**：`readLatency`（驱动读）、`publishLatency`（传输发）已实现。但"数据点从被采集到抵达北向"的**全链路延迟**没有追踪。trace 基础设施**已部分落地、并非空壳**：`common/trace/middleware.go` 的 `Middleware`（`:27`，解析入站 `traceparent`）与 `InjectTraceparent`（`:86`，注出站）已接线 `hub/route/server.go:290`（入站）+ `transport/httppush/push.go:336`（出站），HTTP→HTTP 的 W3C 传播链是通的；`engine.go:1146` 在 `readFromDriver` 起了 `trace.Start` span。但 `README:50` 明确"未配置 OTLP 导出后端，span 仅以 slog.Debug 记录"（debug 关闭时 `trace.Start` 返回 noopSpan），且 **DataPoint 不携带 trace context**（`core/types.go` 无 trace 字段），导致 trace 无法从采集端贯穿到 Publish 端——这是端到端追踪缺口的根因。

**改进**：
- 落地 OTLP 导出（接 Jaeger/Tempo），让 span 真正可视化。
- 增加 `corec_pipeline_latency_seconds` 直方图：记录 DataPoint.Timestamp（采集时刻）到 Publish 完成的端到端延迟——这是用户最关心的"数据有多新鲜"。
- 在 DataPoint 里透传 trace context（当前 trace 主要在 HTTP 入口，未贯穿采集→发布全链路）。

### 10. 缺数据新鲜度分布统计

**现状**：有 `is_stale` 标记（缓存值超阈值），但只是布尔。无法回答"我的数据平均延迟多少、p99 多新"。

**改进**：增加 `corec_data_age_seconds` 直方图，记录 Publish 时刻与 DataPoint.Timestamp 的差值分布。

---

## 四、文档（低优先级但重要）

### 11. QUALITY_ASSESSMENT.md 已过时

**位置**：`QUALITY_ASSESSMENT.md:35,168,173,256,276,324`

**问题**：多处称"仍缺延迟直方图(histogram/summary)"，但实际 `common/metrics/latency.go` + `hub/route/metrics.go:258-259` 已实现并暴露 `corec_read_latency_seconds`/`corec_publish_latency_seconds`，桶 `[1ms,5ms,10ms,50ms,100ms,500ms,1s,5s,10s]`。文档与代码不同步，会误导接手者。

**改进**：更新该文档对应条目为"已实现"，或标注"延迟直方图已于 vX.X 落地"。

### 12. 缺架构决策记录（ADR）

**现状**：`AI_HANDOVER.md` 很详尽，但多为"是什么"，少有"为什么这样选"。例如规则为何选线性扫描、DataBus 为何选"弃旧"而非阻塞、为何用全局 slog 而非注入——这些设计取舍散落代码注释，无集中索引。

**改进**：补一份 `docs/adr/` 记录关键决策与权衡，方便后续维护者理解"为何不换方案"。

---

## 五、可靠性（低优先级，现状已较好）

### 13. DataBus 弃旧策略对控制类数据无区分

**位置**：`engine/databus.go:72-89`

**问题**：缓冲满时无差别丢弃最旧数据。对遥测数据正确（保新弃旧），但若 DataBus 也承载控制响应/确认类数据，丢旧可能丢掉未确认的关键控制反馈。

**改进**：若未来引入控制反馈流，应按数据类别区分背压策略（控制类阻塞或入死信，遥测类弃旧）。当前纯遥测场景无此问题。

### 14. 断路器冷却期探活间隔粗（已修正原"无探活"误判）

**位置**：`common/util/util.go:303` `ReconnectLoopWithBreakerCounted`

**现状（核实后）**：连续失败达阈值后触发断路器，`backoff` 置为 5 分钟（`:348`），随后循环继续——`time.After(5min)` 后**仍调用 `connect()` 探活**（`:326→:337`）。因此**冷却期并非"不探活"，而是以 5 分钟为间隔持续探活**。最坏情况下恢复需等到下一个 5 分钟窗口，恢复延迟上界 ≈5 分钟（±20% jitter）。

**改进**：探活机制本身存在且正确，无需"增加探活"。若希望更快恢复，可缩短冷却探活间隔（如 5min→1min）或冷却期改用轻量 TCP connect 探测、成功后再切回完整 `connect()`，缩短数据缺口窗口。属于低优先级优化，非缺陷。

> ⚠️ 核实纠正：本文档初版称"5分钟内不探活"，经重读 `util.go:317-363` 循环逻辑后确认**有探活**，此条已修正。原结论不成立。

---

## 改进优先级总览

| 优先级 | 建议 | 收益 | 难度 |
|:---:|:---|:---|:---:|
| 🔴 P0 | #1 Publish 异步化，解除 worker 阻塞 | 消除最大实时性瓶颈 | 中 |
| 🔴 P0 | #2 命令执行并发化，解除串行阻塞 | 控制延迟降一个量级 | 中 |
| 🟡 P1 | #6 落地 Logger/Metrics 依赖注入 | 可测试性+多实例支持 | 中 |
| 🟡 P1 | #9 端到端延迟追踪 + OTLP 导出 | 可观测性质变 | 中 |
| 🟡 P1 | #11 修正过时文档 | 防误导 | 低 |
| 🟢 P2 | #3 规则索引预筛 | 大规则集性能 | 中 |
| 🟢 P2 | #4 控制命令不丢策略 | 控制可靠性 | 低 |
| 🟢 P2 | #7 拆分 engine.go | 可维护性 | 低 |
| 🟢 P2 | #8 Manager 实例化 | 多实例支持 | 低 |
| ⚪ P3 | #5/#10/#12/#13 | 锦上添花 | 低 |
| ✅ 非缺陷 | #14 断路器冷却探活 | 已核实机制正确，仅间隔可调 | — |

---

## 值得肯定的设计（不建议改）

避免"为改而改"，以下设计经核查是**正确取舍**，建议保留：

- **DataBus 非阻塞 Push + 弃旧**：对遥测场景是正确的背压策略，保新弃旧优于阻塞采集端。
- **OfflineBuffer fsync + 目录fsync + 原子rename**：`offlinebuffer.go:140-179` 已做完整崩溃安全，无需改。
- **规则线性扫描刻意保留优先级与统计**：权衡合理，规则数不大时无需 prematurely optimize。
- **Modbus 批量读合并（125寄存器/次）**：`modbus_base.go` 批读已优化到位。
- **64分片 LatestCache + atomic.Pointer 快照**：`cache.go` 并发设计优秀。
- **测试覆盖（78>73）+ golangci严格 + 几乎无TODO**：工程纪律好。

---

## 核实记录（第二轮：逐项重读代码验证）

> 应要求重新完整阅读代码，确认每条改进项是否属实。结论：**11 项中 10 项属实，1 项（#14）原结论错误已修正**；"值得肯定"6 项全部经代码验证成立。

| # | 改进项 | 核实结论 | 代码证据 |
|:---:|:---|:---:|:---|
| 1 | processingLoop 同步 Publish 阻塞 worker | ✅ 属实 | `engine.go:1292/1294/1296` 在循环内同步调 `publishToTargets`；`:1615` 同步 `entry.transport.Publish(e.ctx, point)`，worker 阻塞期间不消费 DataBus |
| 2 | 命令监听单 goroutine + 重试退避阻塞 | ✅ 属实 | `engine.go:1319-1333` 每传输单 goroutine；`:1330` 同步调 `executeWriteWithRetry`；`:1408-1422` `time.After(delay)` 退避 sleep 阻塞 listener |
| 3 | 规则线性扫描 O(N) | ✅ 属实（但刻意） | `rule/engine.go:282-297` `for _, r := range rules` 线性扫描；`:257-273` 注释明示"Correctness is preserved over performance"，为保优先级全局有序+统计完整性 |
| 4 | 命令通道满即丢 | ✅ 属实 | `publisher.go:664-668` `select { case commandCh<-cmd: default: slog.Warn(...dropping...) }` |
| 5 | 全局 worker 池无优先级隔离 | ✅ 属实 | `engine.go:154`（`numWorkers=runtime.NumCPU()`）→ `:341-342`（`go e.processingLoop()`）单一 `processingLoop` 池，高频/低频任务共享 |
| 6 | Logger/Metrics 端口未注入 | ✅ 属实（但有意分阶段） | `core/logger.go:9-12` 与 `core/metrics.go:9-12` 注释均明示"defined but NOT yet injected... larger, separate refactor; establish the port so future work can adopt it"——作者明知，非疏忽 |
| 7 | engine.go 单文件过长 | ✅ 属实 | 1792 行；`engine.go` 内 1 处 `nolint:gocyclo`（`:198 Start`，complexity 26），全仓共 5 处（complexity 22–48） |
| 8 | statistic.DefaultManager 全局单例 | ✅ 属实 | `statistic/manager.go:13` `var DefaultManager = NewManager()`；11 处调用（engine.go×9, scheduler.go×2）直接用包级单例 |
| 9 | 缺端到端延迟追踪 | ✅ 属实 | grep `pipeline_latency/data_age/e2e` 仅命中 e2e 测试注释；`trace.go:88-89` span 仅 debug 级才分配否则 noopSpan；`core/types.go` DataPoint 不携带 trace context——trace 未贯穿采集→发布。**注**：traceparent 边界传播已落地（`middleware.go:27/86` + `server.go:290` + `push.go:336`），非空壳，缺口仅在采集→Publish 贯穿 |
| 11 | QUALITY_ASSESSMENT 过时 | ✅ 属实 | 第 35/168/173/256/276/318/324 行均称"仍缺延迟直方图"，但 `latency.go` + `metrics.go:258-259` 已实现并暴露 `corec_read/publish_latency_seconds` |
| 14 | 断路器冷却"无探活" | ❌ **原结论错误，已修正** | `util.go:346-348` 触发后 backoff=5min；`:317-327` 循环 `time.After(5min)` 后**仍调 `connect()`**（:337）——冷却期**有探活**，仅间隔粗。最坏恢复延迟≈5min |

**"值得肯定"6 项验证：**
- DataBus 弃旧：`databus.go:72-89` 非阻塞 select+弃最旧 ✅
- OfflineBuffer 崩溃安全：`offlinebuffer.go:140-179` Write→Sync→rename→dir.Sync ✅
- 规则线性扫描取舍：`rule/engine.go:257-273` 注释佐证刻意 ✅
- Modbus 批读合并：`modbus_base.go:553-687` `performBatchReads`+`mergeBatches` 贪心合并连续/重叠地址，125寄存器/请求 ✅
- 64分片缓存：`cache.go:11,96-128` numCacheShards=64 + atomic.Pointer 快照 ✅
- 测试覆盖：实测 test 78 / src 73 ✅

**总体判定**：改进建议经核实绝大多数成立（10/11），唯一误判（#14）已修正。其中 #3、#6 属"作者已知的有意取舍"，应标注为"可优化但非缺陷"，避免误读为疏忽。

---

*建议与 `REALTIME_EVALUATION.md` 配合阅读：前者评价现状，本文给出改进路径。*

---

## 核实记录（第三轮：独立全量重读验证）

> 本轮按行号逐条回到源码核对全部 14 项 + 6 项"值得肯定"。结论：**12 项完全属实，2 项（#1、#9）部分属实，0 项被证伪**。#1、#9 的核心问题均成立，但正文各有一处"前提/现状"表述不够准确（见修正 C、D）；另有两处支撑性细节表述不精确（修正 A、B）。

| # | 改进项 | 核实结论 | 备注 |
|:---:|:---|:---:|:---|
| 1 | processingLoop 同步 Publish 阻塞 worker | ⚠ 部分 | **核心属实**：无 batcher 路径 `engine.go:1615 transport.Publish` 同步阻塞 worker。**但方案B前提不准确**：batcher 路径并非"安全非阻塞"——见修正 C |
| 2 | 命令监听单 goroutine + 退避阻塞 | ✅ | `:1314/1319-1333` 每传输单 goroutine，`:1330` 同步 `executeWriteWithRetry`；`:1408/1422` `time.After(delay)` 退避（100→200→400ms，上限 2s，`batcher.go:18/37`） |
| 3 | 规则线性扫描 O(N) | ✅（刻意） | `rule/engine.go:283` `for _, r := range rules`；`:257-273` 注释明示"Correctness is preserved over performance" |
| 4 | 命令通道满即丢 | ✅ | `mqtt/publisher.go:664-668` `select{case commandCh<-cmd:default:Warn(dropping)}`；缓冲默认 100（`core/types.go:218`） |
| 5 | 全局 worker 池无优先级隔离 | ✅ | `engine.go:154` `numWorkers=runtime.NumCPU()`，`:341-342` 单一 `processingLoop` 池共享 |
| 6 | Logger/Metrics 端口未注入 | ✅（有意） | `core/logger.go:9-12`、`core/metrics.go:9-12` 明示"defined but NOT yet injected"；engine.go 全程 55 处直接 `slog.*` |
| 7 | engine.go 单文件过长 | ✅ 已修正 | 1792 行（实测三工具一致）；`engine.go` 内 1 处 `nolint:gocyclo`（`:198 Start`，complexity 26），全仓 5 处（complexity 22–48）。正文已回写 |
| 8 | statistic.DefaultManager 全局单例 | ✅ | `statistic/manager.go:13`；调用 11 处＝engine.go×9(`1263/1605/1609/1620/1624/1646/1649/1660/1663`)＋scheduler.go×2(`246/307`) |
| 9 | 缺端到端延迟追踪 | ⚠ 部分 | **4 个子项全部属实**（DataPoint 无 trace 字段、debug 关闭返回 noopSpan、无 pipeline 指标、Publish 路径无 span）。**但正文低估了 trace 现状**——见修正 D |
| 10 | 缺数据新鲜度分布 | ✅ | 仅 `IsStale bool`（`core/types.go:137`），无 `corec_data_age_seconds` 直方图 |
| 11 | QUALITY_ASSESSMENT 过时 | ✅ | 第 35/168/173/256/276/318/324 行仍称"缺延迟直方图"，但 `common/metrics/latency.go:19/24` 已实现、`hub/route/metrics.go:258-259` 已暴露 `corec_read/publish_latency_seconds` |
| 12 | 缺 ADR | ✅ | `docs/` 下无 `adr/` 目录与任何 ADR 文件 |
| 13 | DataBus 弃旧无控制类区分 | ✅ | `databus.go:72-89` 非阻塞 Push+evict 最旧；当前仅承载遥测 |
| 14 | 断路器冷却探活 | ✅（已修正版正确） | `util.go:346-350` 触发后 backoff=`5min`(`:24`)，循环继续 `:326 time.After(wait)` 后 `:337` 仍 `connect()` —— 冷却期**有探活**，仅间隔粗 |

**修正 A（#7 支撑细节）**：`engine.go` 内实际**仅 1 处** `nolint:gocyclo`（第 198 行 `Start`，complexity 26）。"complexity 48" 位于 `config/config.go:104`（`validate`）。全仓共 5 处 `nolint:gocyclo`：config.go(48)、executor.go(22)、engine.go(26)、scheduler.go(27)、modbus_base.go(26)。故 "26-48" 是全仓范围、非 engine.go；结论"1792 行、十余职责"不受影响。

**修正 B（#2 附注）**：正文"`forwardCommand` 持 RLock 遍历所有传输"不精确——`engine.go:1345-1350` 仅在 RLock 内**快照** transports 切片，随后 `:1350` 释放锁，`:1352-1363` 才遍历并调用 `ForwardCommand`（不持锁）。核心结论"单 goroutine＋退避阻塞"不受影响。

**修正 C（#1 方案B 前提不准确）**：正文方案B称"batcher 的 `publish` 已是缓冲+定时刷新"，暗示走 batcher 即非阻塞。**实际不然**：`batcher.go:183 publish()` 在 `len(buffer) >= batchSize`（`:186`）时**同步**调用 `flush()`（`:190`，运行于调用方 goroutine，即 processingLoop worker，经 `engine.go:1600 entry.batcher.publish` 进入）；`flush()`→`publishWithRetryAndBuffer`（`:206`）内 `for attempt`（`:240`）含 `time.After(delay)` 指数退避 sleep（`:250/:269`，base 100ms，cap `defaultRetryMaxDelay`）。即便 `retryCount=0`（maxAttempts=1 无 sleep），单次 `PublishBatch`（`:242`）仍是同步网络 I/O。**故 batcher 仅在 buffer 未满时才非阻塞——恰在负载突发、buffer 填满（最需要批处理的时刻）时同步阻塞 worker**。结论：方案B"强制全部走 batcher"不能解除 #1 瓶颈，只会把阻塞推迟到 buffer 填满之际；真正解法仍是方案A（per-transport 独立发布 goroutine）或方案C（Publish 超时落 OfflineBuffer）。核心问题（同步 Publish 阻塞 worker）成立。

**修正 D（#9 低估 trace 现状）**：正文称 trace "仅以 slog.Debug 记录"、聚焦 OTLP 未导出，易被读成"trace 是空壳"。**实际 W3C traceparent 传播已在 HTTP 边界 operational 落地**：`common/trace/middleware.go` 的 `Middleware`（`:27`）解析入站 `traceparent` 头（`:32`，`parseTraceparent` `:55`），`InjectTraceparent`（`:86`）注出站头（`:94`）；已接线 `hub/route/server.go:290`（入站）+ `transport/httppush/push.go:336`（出站）。即 HTTP→HTTP 的 W3C 传播链是通的。真正的缺口只是 **DataPoint 不携带 trace context**（`core/types.go:122-141` 无 trace 字段），导致 trace 无法从采集端贯穿到 Publish 端。故 #9 的"缺端到端延迟追踪/贯穿"结论成立，但"trace 未落地"的印象应纠正为"traceparent 边界传播已通，仅未贯穿采集→发布"。

**"值得肯定" 6 项核验**：DataBus 弃旧(`databus.go:72-89`)、OfflineBuffer fsync+dir fsync+atomic rename(`offlinebuffer.go:139-180`)、规则线性扫描刻意(`rule/engine.go:257-273`)、Modbus 125 寄存器批读合并(`modbus_base.go:53/560/667`)、64 分片缓存+`atomic.Pointer`(`cache.go:11/31`)、测试 78>源 73＋0 TODO/FIXME —— **全部属实**。

**最终判定**：14 项中 **12 项完全属实、2 项（#1、#9）部分属实、0 项被证伪**。本轮已将所有纠正**回写正文**（不再仅以附注形式存在）：#1 方案B 补"buffer 满仍同步阻塞"（修正 C）；#2 附注改"快照后释放锁、遍历不持锁"（修正 B）；#5 位置补 `:154→:341-342`；#7 nolint 改"engine.go 1 处、全仓 5 处、complexity 22–48"（修正 A，行数实测 1792 不动）；#9 现状补"traceparent 边界传播已落地、非空壳"（修正 D）。第二、三轮表同步更正。"值得肯定"6 项全部属实。其中 #3、#6 为作者已知的有意取舍，#14 已在上轮修正为"冷却期有探活"。

> 附：行号/计数核验（以代码实测为准）。engine.go 行数经 `wc -l`/`awk NR`/`grep -c ''` 三种工具一致为 **1792**（曾出现的"1793"系末行无换行的计数口径差异，实测为 1792）；#5 的 `numWorkers=runtime.NumCPU()` 赋值在 `engine.go:154`，`:341-342` 为 `go processingLoop()` 启动点；#3 的 `Match` 入口在 `rule/engine.go:274`，线性扫描实际在 `matchInRules:282-297`。

---

## 实施记录（2025-09-25）

> 以下记录本轮实施的实际状态。每项标注 ✅ 已完成 / ⚠ 部分完成 / ⏸ 延迟（附 ADR 编号）。所有已实施项均通过 CONTRIBUTING §0 全部 5 项 gate（go build / CGO_ENABLED=0 build / go vet / golangci-lint 0 issues / go test -race -short）。

### ✅ #1 Publish 异步化（batcher 路径）

- **实施范围**：`engine/batcher.go` 的 `publish()` 在 buffer 满时将整批数据移入 `flushBatches chan []core.DataPoint` 队列（非阻塞，满时弃最旧），由独立 `flushLoop` goroutine 异步消费。`flushLoop` 在 `ctx.Done` 时 `drainFlushBatches` 排空剩余批次。
- **可观测性**：`publishLatency` 和 `dataAge` 直方图在 `publishWithRetryAndBuffer`（实际 `PublishBatch` 调用处）观察，不在 `publish()` 入队处观察——异步化后入队近乎即时，观察必须移到真实 I/O 发生点。`flushBatchesDropped` 计数器通过 `core.FlushBatchesDroppedProvider` 角色接口暴露为 Prometheus `corec_flush_batches_dropped_total`。`totalPublish`/`statManager.PushPublish` 计数通过 `onPublish func(int)` 回调在 `publishWithRetryAndBuffer` 和 `drainOnce` 成功时触发——不在 `publish()` 入队处计数（Finding 4 修复：避免将入队计为已发布）。`publish()` 在 flush 队列满且批次被丢弃时返回 error，激活 publish.go 的 fallback 分支（Finding 4 修复：原为死代码）。
- **测试**：`batcher_integration_test.go` 的 `TestBatcherPublishAsyncNonBlocking`（慢传输 150ms，2 次 publish 断言 <100ms 返回 + 数据最终发布）。
- **诚实声明**：仅 batcher 路径已异步化。**非 batcher 的直接 `transport.Publish` 路径仍同步**（`publish.go` 的 `entry.transport.Publish(e.ctx, point)`）。配置了 `batch-size` 或 `offline-buffer` 的传输走 batcher（已异步），未配置的传输仍阻塞 worker。完全解除 #1 瓶颈需为所有传输配置 batcher 或实施 per-transport 发布 goroutine（方案A，未做）。

### ✅ #2 命令执行并发化

- **实施范围**：`command_manager.go` 的 `startCommandListener` 使用 `commandSem`（有界信号量，默认 16）限制并发命令数。每条命令获取 slot 后 spawn 独立 goroutine 执行 `executeWriteWithRetry`，slot 满时通过 `select { case commandSem<-: case <-tCtx.Done() }` 施加背压。`command-concurrency` 配置项（`core.EngineConfig`，默认 16，设 1 为串行模式）。
- **测试**：`command_forward_test.go` 的 `TestCommandListenerDispatchesConcurrently`（4 命令 × 100ms，并发 8，断言 <250ms + peak≥2）+ `TestCommandListenerSerialWhenConcurrencyOne`（并发 1，断言 peak=1）。

### ✅ #4 控制命令不丢策略

- **实施范围**：`transport/mqtt/publisher.go` 新增 transport-local 有界死信存储（`commandDeadLetter`，`commandDeadLetterMaxLen` 上限，满时弃最旧）。命令通道满时 `droppedCommands` 计数 + 入死信存储。通过 `Status()` 暴露 `DroppedCommands`（JSON `dropped_commands` 字段）+ Prometheus `corec_transport_dropped_commands_total` 指标。`CommandDeadLetterEntries()` 导出方法供操作员查询死信条目。
- **架构边界**：死信存储在 transport 层（不能 import engine），复用 `core.DeadLetterEntry` 类型。
- **测试**：`command_auth_test.go` 的 `TestMQTTCommandChannelFullDivertsToDeadLetter`（缓冲 1，2 条不同时间戳命令，断言 DroppedCommands==1 + 1 条死信）+ `TestMQTTCommandDeadLetterBound`（上限 3，5 次溢出，断言存储长度==3 + DroppedCommands==5）。

### ✅ #5 Worker 池优先级隔离

- **实施范围**：
  - `engine/databus.go`：新增 `highPriCh` 通道（容量 = bufferSize/4，范围 64–1024）+ `PushHighPriority(point)` 方法（弃最旧语义同 Push）+ `HighPriorityChannel()` + `HighPriorityPushDropped()` + `Close()` 同时关闭 highPriCh。
  - `engine/processing.go`：提取 `processPoint(point)` 共享逻辑；`processingLoopHighPriority()` 从 highPriCh 消费。
  - `engine/engine.go`：`Start()` 启动 `numHighPriorityWorkers` 个高优先级 worker。
  - `engine/driver_manager.go`：`scheduleDriverTags` 按 interval ≤ `core.DefaultHighPriorityInterval`（1s）设置 `Priority: 1`。
  - `engine/scheduler.go`：`onData` 签名增加 `priority int` 参数，`runTask` 传递 `task.Priority`。
  - `engine/driver_manager.go`：`onDriverData` 按 priority>0 路由到 `PushHighPriority`，否则 `Push`。
- **测试**：`databus_priority_test.go`（4 项测试：通道隔离、弃旧计数、高优 worker 不受主通道饱和影响、scheduleDriverTags 优先级分类）。

### ✅ #7 拆分 engine.go

- **实施范围**：`engine.go` 从 1923 行拆为 823 行 + 6 个职责文件：
  - `engine.go`（823 行）：struct 定义、New、Start/Stop/Suspend/Resume/Status/Reload、applyEngineConfig、Stats/OfflineBufferStats/Latency histograms、autoFillNodeConfig/startDiscovery、规则管理。
  - `driver_manager.go`（395 行）：AddDriver/RemoveDriver/GetDriver/ListDrivers、tag-file watcher、readFromDriver/onDriverData/scheduleDriverTags。
  - `transport_manager.go`（193 行）：AddTransport/RemoveTransport/GetTransport/ListTransports、startDataListener。
  - `command_manager.go`（226 行）：ReadTag/WriteTag/hasDriver、startCommandListener/forwardCommand/executeWriteWithRetry/dead-letter。
  - `processing.go`（96 行）：processingLoop/processingLoopHighPriority/processPoint。
  - `publish.go`（201 行）：publishToTargets/tryFallback/applyTransform/numericValue/fireAlert。
  - `query.go`（48 行）：LatestValues/StaleThreshold/Subscribe/SubscribeWithBuffer/OnAlert。
- 纯重构：无逻辑变更，无接口变更，同包内文件拆分。

### ✅ #8 statistic.Manager 实例化

- **实施范围**：`statistic.DefaultManager` 包级单例改为 `CoreCEngine.statManager` 实例字段（`engine.go`），通过 `NewScheduler` 第 4 参数注入 scheduler。`processing.go`/`publish.go`/`scheduler.go` 全部 `statistic.DefaultManager.X()` 改为 `e.statManager.X()` / `s.statManager.X()`。
- **诚实声明**：`statistic.DefaultManager` 仍保留（向后兼容）但引擎内已不使用。`statistic.Manager.Snapshot()` 当前无生产消费端（仅测试调用）——engine 的 `Stats()` 使用自己的 atomic 计数器。未来可将 `statManager.Snapshot()` 接入 route metrics 以提供独立于 engine.Stats() 的计数视角。

### ✅ #10 数据新鲜度分布统计

- **实施范围**：`core.DataAgeProvider` 角色接口（`core.LatencySnapshot DataAgeHistogram()`）。`CoreCEngine.dataAge` 直方图（`metrics.DefaultDataAgeBuckets`）。在 4 个发布站点调用 `e.dataAge.Observe(time.Since(point.Timestamp))`：batcher publish、direct publish、fallback batcher、fallback direct。`hub/route/metrics.go` 暴露 `corec_data_age_seconds` Prometheus 直方图。
- **测试**：`hub/route/metrics_test.go` 验证直方图输出格式与 bucket 范围。

### ✅ #11 修正 QUALITY_ASSESSMENT.md

- **实施范围**：修正 QUALITY_ASSESSMENT.md 中"仍缺延迟直方图"等过时表述（`corec_read_latency_seconds` / `corec_publish_latency_seconds` 已实现并暴露）。

### ✅ #12 新增 ADR

- **实施范围**：`docs/architecture/decisions.md` 记录 ADR-001 至 ADR-009。sidebar 已接线。ADR-002 记录 #3 的非实施决策，ADR-008 记录 #6 的延迟决策，ADR-009 记录 #9 的部分实施状态。

### ⏸ #3 规则索引预筛（不实施）

- **决策**：不实施。线性扫描保证优先级全局有序 + 统计完整性，刻意优先正确性。详见 ADR-002。当前工业场景规则数 <100，无性能感知。

### ⏸ #6 Logger/Metrics 端口注入（延迟）

- **决策**：延迟。涉及 94 处 `slog.X` 调用 + 10 个文件 + 5+ 构造器签名。`core.Logger`/`core.Metrics` 端口已定义，注入时不需改 core 契约。详见 ADR-008。

### ⚠ #9 端到端延迟追踪（部分完成）

- **已完成**：pipeline 延迟直方图 = `corec_data_age_seconds`（#10 的 `dataAge` 直方图，记录 Publish 时刻与 `DataPoint.Timestamp` 的差值 = 采集到发布的端到端延迟）。
- **延迟**：(1) OTLP 导出接 Jaeger/Tempo（运维基础设施配置，非代码变更）；(2) DataPoint 透传 trace context（core 契约评估）。详见 ADR-009。

### 未改动项

- **#13**（DataBus 弃旧对控制类数据无区分）：当前 DataBus 仅承载遥测，控制命令走独立 commandCh。#4 已为命令通道满提供死信存储。无需改动。
- **#14**（断路器冷却探活）：已核实机制正确（冷却期有探活，仅间隔 5min 粗）。非缺陷。

### Gate 验证

| Gate | 命令 | 结果 |
|:---:|:---|:---:|
| 1 | `go build ./...` | ✅ exit 0 |
| 2 | `CGO_ENABLED=0 go build -o /dev/null ./cmd/corec` | ✅ exit 0 |
| 3 | `go vet ./...` | ✅ exit 0 |
| 4 | `golangci-lint run --timeout 5m` | ✅ 0 issues |
| 5 | `go test -race -short -timeout 120s ./...` | ✅ 21 packages ALL PASS |

### 独立代码审查修复

> 独立 subagent 对全部 19 个变更文件进行了逐行审查。结论：并发安全、架构边界、六边形契约全部 clean（无竞态、无 goroutine 泄漏、无 send-on-closed-channel、无死锁、无架构违规）。发现 4 个问题，全部已修复：

| 严重度 | 问题 | 修复 |
|:---:|:---|:---|
| 🔴 中 | **Finding 1**：异步化后 `publishLatency`/`dataAge` 观察在 `publish()` 入队处（近乎即时），未在真实 `PublishBatch` 处观察——使 batcher 传输的两个 Prometheus 直方图失真 | 将观察移入 `publishWithRetryAndBuffer`（batcher.go），在 `PublishBatch` 调用前后观察；通过 `newTransportBatcher` 注入引擎直方图 |
| 🟡 低 | **Finding 2**：flush 队列满时驱逐日志 `evicted_batch_size` 记录的是新批次大小而非被驱逐批次大小 | `case evicted := <-b.flushBatches` 捕获被驱逐批次，记录 `len(evicted)` |
| 🟢 低 | **Finding 3a**：`flushBatchesDropped` 计数器从未暴露 | 新增 `core.FlushBatchesDroppedProvider` 接口 + `corec_flush_batches_dropped_total` Prometheus 指标 |
| 🟢 低 | **Finding 3b**：`DroppedCommands` 在 JSON 中但未在 `/metrics` 暴露 | 新增 `corec_transport_dropped_commands_total` Prometheus 指标 |
| 🟢 低 | **Finding 3c**：`commandDeadLetterEntries()` 未导出，操作员无法查询死信条目 | 导出为 `CommandDeadLetterEntries()` |
| ⚪ 已知→✅ | **Finding 4**（预存）：`batcher.publish()` 始终返回 nil，publish.go 的 batcher error/fallback 分支为死代码；且 `totalPublish`/`PushPublish` 在入队后立即计数（premature counting），异步发布失败时仍计为已发布 | `publish()` 在 flush 队列满且批次被丢弃时返回 error（激活 fallback 分支）；`totalPublish`/`PushPublish` 计数移入 `publishWithRetryAndBuffer` 和 `drainOnce` 的成功路径，通过 `onPublish func(int)` 回调注入；publish.go batcher 路径移除 premature 计数 |
