# CoreC 数据采集与控制实时性评价报告

> 基于对 CoreC 全量源码的逐文件阅读（`core/`、`engine/`、`driver/`、`transport/`、`rule/`、`common/metrics/`、`config/`），并经 `go build ./...` 与 `go vet` 验证代码可编译。

---

## 一、先理解代码：数据流与控制流全景

CoreC 是一个 IIoT 数据采集与控制核心，采用六边形插件化架构。其核心数据通路为：

```
南向驱动(Driver) ──Read──> Scheduler ──> onDriverData ──> DataBus(带缓冲Channel)
                                                              │
                                                   N个processingLoop协程消费
                                                              │
                                              ┌───────────────┼───────────────┐
                                              ▼               ▼               ▼
                                          LatestCache    RuleEngine.Match   DataBus.Broadcast
                                        (分片缓存)      (线性扫描匹配)      (订阅者推送)
                                                              │
                                                        publishToTargets
                                                              │
                                              ┌───────────────┴───────────────┐
                                              ▼                               ▼
                                          Transport.Publish              transportBatcher
                                        (直接发布/MQTT/HTTP)           (批量缓冲+重试+离线落盘)
```

**控制（下行）流：**
```
北向Transport.OnCommand(MQTT command-topic) ──> commandCh(带缓冲)
                                                  │
                                     startCommandListener协程消费
                                                  │
                                     executeWriteWithRetry
                                     ├── 本地有驱动 → WriteTag → Driver.Write (重试+死信队列)
                                     └── 无本地驱动 → forwardCommand (级联转发到下游节点)
API: POST /write ──> WriteTag ──> Driver.Write (同步直写)
```

---

## 二、实时性机制逐层剖析

### 1. 采集触发方式：两种模式

| 模式 | 实现位置 | 实时性特征 |
|:---|:---|:---|
| **周期轮询** | `engine/scheduler.go` `runTask` | 每个 tag 按 `interval` 用 `time.NewTicker` 触发，**每采集任务一个独立 goroutine**，互不阻塞 |
| **事件订阅** | `driver/opcua/client.go` `subscriptionLoop` | OPC UA Subscription 模式下，设备值变化即推送，经 `subChannel`(buffer 1024) 异步转发，**真正的事件驱动** |

**关键设计：**
- 调度器按"同 interval 的 tags 合并为一个 task"分组（`scheduleDriverTags`），减少 goroutine 数量。
- Modbus 支持批量读（`performBatchReads`）：将连续/重叠地址合并为单次 FC03/FC04 请求（上限 125 寄存器），**一次网络往返读多个点**，显著降低批量采集延迟。
- `ReadTimeout` 与 `interval` 解耦（`types.go:175`）：高频采集（200ms）可配更长读超时（1s），避免误杀慢响应。

### 2. 数据总线：Go Channel 无锁流水线

```go
// engine/databus.go
type DataBus struct {
    ch          chan core.DataPoint  // 带缓冲，默认 8192
    subCount    atomic.Int64         // 无订阅者时快速跳过
}
```

- `Push` 非阻塞：缓冲满时**丢弃最旧数据**腾位（`pushDropped` 计数），保证采集端不因消费端慢而阻塞——**这是实时性的核心取舍：保新弃旧**。
- `Broadcast` 给订阅者也是非阻塞 `select default`：慢订阅者丢消息（`dropped` 计数），不拖累管线。
- 默认缓冲 8192，可经 `engine.data-bus-size` 配置调大。

### 3. 处理管线：N-worker 并行消费

```go
// engine/engine.go processingLoop
e.wg.Add(numWorkers)   // 默认 runtime.NumCPU()
for i := 0; i < numWorkers; i++ { go e.processingLoop() }
```

- 多 worker 从同一 `DataBus.Channel()` 竞争消费，**缓存更新 + 规则匹配 + 发布并行化**。
- `LatestCache` 采用 **64 分片**（`numCacheShards=64`）+ FNV 哈希分桶，降低并发写锁争用。
- `GetByDriver` 用 `atomic.Pointer` 快照：未脏时直接返回缓存快照，避免每次 API 调用全量复制。

### 4. 规则匹配：线性扫描，O(N) 但有意识

```go
// rule/engine.go Match
for _, r := range rules { if r.Match(point) { return ... } }  // 优先级序，首个命中即返回
```

- **刻意选择线性扫描而非索引**（代码注释明确说明）：为保证优先级全局有序 + 每条规则都能记录 hit/miss 统计。
- 表达式在 `SetRules` 时预编译（`compileExpr`），运行时只 eval，无解析开销。
- 命中即返回，实际只评估到首个匹配——规则数少时（典型 <100）延迟可忽略（微秒级）。

### 5. 传输发布：直发 vs 批量缓冲

| 路径 | 行为 | 延迟 |
|:---|:---|:---|
| **无 batcher** | `Transport.Publish` 同步发，MQTT QoS1 等 `WaitTimeout(publishTimeout)` | 单点发布延迟 ≈ 网络RTT |
| **有 batcher** | 攒满 `batchSize` 或 `flushInterval` 到期才发 | 牺牲延迟换吞吐，可配 `flush-interval` 控制 |

- 批量发布失败：指数退避重试（100ms→200ms→...上限5s），耗尽后落盘 `OfflineBuffer`，恢复后 30s 周期回放。
- `fallback` 传输：主传失败自动切备用。

### 6. 控制下发：重试 + 死信 + 级联

```
executeWriteWithRetry: writeRetryCount+1 次（默认4次），退避 100ms→200ms→400ms，上限 2s
  ├── 全部失败 → DeadLetterQueue (上限1000，FIFO淘汰)
  └── 无本地驱动 → forwardCommand 遍历支持 CommandForwarder 的传输转发
```

- 命令通道 `commandCh` 默认 buffer 100，满时**丢弃命令并告警**（`mqtt publisher.go:667`）——控制指令不排队堆积，宁可丢也不阻塞。
- 控制路径是**异步**的：MQTT 收到命令 → commandCh → listener 协程 → 写驱动，解耦了 MQTT 回调线程。
- API `POST /write` 是**同步直写**（`WriteTag` → `Driver.Write`），延迟 = 驱动写入 RTT。

---

## 三、实时性评价：如何看与如何评价

### 评价维度与结论

| 维度 | 机制 | 评价 |
|:---|:---|:---|
| **采集确定性** | Ticker 周期触发 + 独立 goroutine | 🟢 **良好**。每任务独立调度，互不阻塞；但 Ticker 受 GC pause / 调度抢占影响，非硬实时 |
| **端到端延迟** | Driver.Read → Channel → Worker → Publish | 🟢 **低延迟**。全内存通道传递，无序列化中间层；典型路径 <1ms（不含网络IO） |
| **吞吐能力** | 8192 缓冲 + N-worker + 批量读/发 | 🟢 **高吞吐**。批量读合并请求、批量发合并发布，分片缓存降争用 |
| **背压策略** | 非阻塞 Push/Broadcast，满则弃旧 | 🟡 **保新弃旧**。实时系统正确取舍，但需监控 `pushDropped`/`dropped` 防数据缺口 |
| **控制可靠性** | 重试+死信+级联转发 | 🟢 **健壮**。失败可追溯、可重放；但命令通道满会丢命令（需监控） |
| **故障恢复** | 指数退避重连+断路器+自动降级 | 🟢 **生产级**。断线不刷屏（10s限频）、连续失败降频10x、断路器5分钟冷却 |
| **可观测性** | 延迟直方图+Prometheus+pprof+WebSocket流 | 🟢 **完善**。read/publish 延迟直方图已实现并暴露（注：QUALITY_ASSESSMENT.md 称"缺直方图"已过时） |
| **确定性边界** | 无优先级调度、无 deadline 隔离 | 🔴 **非硬实时**。Go runtime 无实时调度，高负载下 jitter 不可控；规则线性扫描在规则数极大时成瓶颈 |

### 如何"看"实时性（可观测手段）

1. **`GET /metrics`（Prometheus）**：
   - `corec_read_latency_seconds` / `corec_publish_latency_seconds`：直方图，桶 `[1ms,5ms,10ms,50ms,100ms,500ms,1s,5s,10s]`
   - `corec_http_request_duration_seconds`：API 请求延迟
   - Go 运行时指标：goroutine 数、heap、GC pause、CPU
2. **`GET /stats`**：`points_per_sec`、`total_dropped`（含 DataBus 弃旧 + 订阅者丢 + 日志丢）
3. **`GET /traffic` WebSocket**：每秒推送吞吐量
4. **`/tags/stream` WebSocket**：实时数据点流，可按 driver 过滤
5. **`/debug/pprof/*`**：CPU/heap/goroutine profile，定位瓶颈
6. **调度器 overrun 告警**：`latency > interval` 时限频告警（10s 窗口），提示采集周期被读超时撑破
7. **`GET /drivers/{name}`**：`last_read`、`error_count`、`reconnect_count` 判断驱动健康

### 如何"评价"实时性（关键指标解读）

- **看 `corec_read_latency_seconds` 的 p99**：若 p99 < 采集 interval，说明采集能按时完成；若 p99 > interval，会出现 overrun（调度堆积）。
- **看 `total_dropped` 是否增长**：增长说明消费端（worker/订阅者）跟不上生产端，需调大 `data-bus-size` 或 `workers`。
- **看 `pushDropped` vs `dropped`**：前者是 DataBus 满（worker 跟不上），后者是订阅者慢（如 WebSocket 客户端慢）。
- **看 `points_per_sec` 与配置预期对比**：实际吞吐 = Σ(1/interval × tag数)，若明显偏低说明驱动重连/降级中。
- **看调度器 overrun 频率**：高频出现说明 interval 设得过短或网络往返过长，需调大 interval 或拆分批量。

---

## 四、瓶颈与改进建议

| 瓶颈 | 位置 | 影响 | 建议 |
|:---|:---|:---|:---|
| 规则线性扫描 | `rule/engine.go:282` | 规则数 >500 时每点多微秒 | 加 tag/driver 索引预筛（牺牲部分统计精确性） |
| 同步 Publish 阻塞 worker | `engine.go:1615` | 慢传输拖累整个管线 | 默认走 batcher 异步，或 Publish 也走独立 channel |
| 命令通道满丢命令 | `mqtt/publisher.go:667` | 控制指令丢失 | 控制场景应增大 buffer 或提供持久化命令队列 |
| GC pause 抖动 | Go runtime | 高频采集(200ms)下可见 jitter | 调 GOGC、用 sync.Pool 减少 alloc（已部分用 atomic） |
| 无优先级隔离 | 全局 worker 池 | 高频任务与低频任务争 worker | 按 interval 分池或加权调度 |

---

## 五、总体结论

CoreC 的实时性设计**在"软实时"范畴内属于优秀水平**，具体体现在：

1. **架构层面**：全异步、无锁通道、分片缓存、批量IO、多worker并行——典型的高性能数据管线设计。
2. **工程层面**：背压用"保新弃旧"、故障用"退避+断路器+降级"、控制用"重试+死信+级联"——生产级容错完备。
3. **可观测层面**：延迟直方图+Prometheus+pprof+WebSocket流，能实时看清延迟与吞吐。
4. **定位层面**：明确是"工业网关/边缘核心"，非硬实时系统；对 ms~s 级采集周期支持良好，不适合 sub-ms 确定性控制。

**它不是硬实时控制系统**（无 RTOS 调度、无优先级抢占、无 deadline 隔离），而是**高吞吐、低延迟、高可靠的工业数据中台**。对于 IIoT 数据采集与控制指令下发的典型场景（秒级/百毫秒级采集、异步控制下发），其实时性是充裕且可观测、可调优的。
