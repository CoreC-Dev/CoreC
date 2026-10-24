# 高性能设计

核心的高性能不是单一技巧，而是从数据流到 I/O 的**六层设计**，每层解决一个瓶颈。

## 第一层：Channel 驱动的无锁数据流

```
Driver goroutine → DataBus 主通道 (cap=8192) → processingLoop (N worker) ──┬→ Transport
        └────────→ DataBus 高优先级通道 (cap≤1024) → 专属 worker (默认 2 个) ──┘
```

采集→处理→发布管道用带缓冲的 Go channel 串联：主通道默认 8192 缓冲（`core.DefaultDataBusSize`），快周期任务（interval ≤ 1s）路由到高优先级通道（容量为主通道 1/4、上限 1024、下限 64）。生产端跑在调度 goroutine 里，消费端由 N 个 worker（默认 `runtime.NumCPU()`）并发消费主通道、2 个专属 worker 消费高优先级通道，**零锁传递数据**——低频批量读取的突发挤占不了高频采集。

```go
// engine/engine.go — 引擎默认值（New() 中设置）
e.processing.numWorkers = runtime.NumCPU()
e.processing.dataBusSize = core.DefaultDataBusSize          // 8192
e.processing.numHighPriorityWorkers = core.DefaultHighPriorityWorkers // 2

// engine/lifecycle_support.go — Start 时创建 DataBus（两条通道）
l.eng.processing.dataBus = NewDataBus(l.eng.processing.dataBusSize)

// engine/processing.go — 启动两组消费 worker
for i := 0; i < numWorkers; i++ { go p.processingLoop() }
for i := 0; i < numHP; i++    { go p.processingLoopHighPriority() }
```

::: tip 为什么快
channel 是 Go runtime 原语，底层 mutex 只在 enqueue/dequeue 的极短临界区内加锁。8192 的缓冲深度吸收了采集突发，消费端不会被反压。
:::

## 第二层：Drop-oldest 背压策略

当采集速度超过处理速度时，DataBus 不阻塞生产端，而是**丢最旧、塞最新**：

```go
func (b *DataBus) Push(point core.DataPoint) {
    select {
    case b.ch <- point:          // 有空间，直接塞
    default:
        select { case <-b.ch: b.pushDropped.Add(1); default: }  // 丢最旧并计数
        select {                                                 // 非阻塞重试塞最新
        case b.ch <- point:
        default: b.pushDropped.Add(1)                            // 仍满则丢弃新点并计数
        }
    }
}
```

高优先级通道的 `PushHighPriority` 采用完全相同的弃旧语义，丢弃数单独记入 `highPriPushDropped`，两条通道的丢弃量分别经 `corec_databus_dropped_total{channel=main|priority}` 暴露。

::: warning 设计意图
工业数据中**最新值比历史完整性更重要**。温度传感器 1s 采一次，如果处理慢了 0.5s，丢掉上一秒的值比阻塞采集导致下一秒也超时要好。
:::

## 第三层：并行调度

调度器为每个「驱动 + 采集周期」组合启动独立 goroutine：

```go
func (s *scheduler) runTask(ctx context.Context, runner *taskRunner) {
    ticker := time.NewTicker(interval)
    for {
        select {
        case <-ctx.Done(): return
        case <-ticker.C:
            values, err := s.readFunc(readCtx, task.Driver, task.Tags)
        }
    }
}
```

3 台 PLC 各 10 个测点 → 3 个 goroutine 并行发起 Modbus 读取，总耗时 = max(单台延迟) 而非 sum(三台延迟)。

## 第四层：非阻塞 fan-out + RWMutex

**实时流订阅**用 `select + default` 非阻塞投递（无订阅者时经原子 `subCount` 快路径跳过加锁；带 filter 的订阅者只收匹配 driver 的点）：

```go
func (b *DataBus) Broadcast(point core.DataPoint) {
    if b.subCount.Load() == 0 { return }   // 无订阅者：连读锁都不拿
    b.mu.RLock()
    defer b.mu.RUnlock()
    for _, sub := range b.subscribers {
        if sub.filter != "" && point.Driver != sub.filter { continue }
        select {
        case sub.ch <- point:    // 没满
        default:
            b.dropped.Add(1)     // 满了，丢弃 + 计数
        }
    }
}
```

**读写锁策略**：LatestCache 采用 64 分片设计（按 driver 名 FNV 哈希分片，每片独立 `sync.RWMutex`，降低多 worker 并发更新时的锁竞争），RuleEngine 用 `sync.RWMutex`，N 个 API 请求并发读不互斥。

**规则排序与驱动索引**：规则按 `priority` 升序排列，使用 `sort.SliceStable`（**稳定排序**）——相同优先级的规则保持配置文件中的原始顺序，且通用规则不会在同优先级下隐式遮蔽 driver 专属规则（构造时记录的序号 seq 作为平级决胜）。匹配带 `driver == 'x'` 前缀的规则进入按驱动分桶的索引，只对来自该驱动的数据点求值；被索引跳过的规则经 `RecordMiss` 记 miss，不破坏统计语义。无索引可用时回退线性扫描。

## 第五层：原子计数器

引擎的吞吐/错误统计用 `sync/atomic`，无锁、无分配；读取速率用无锁固定窗口计数器（60 个原子桶，每桶打包「unix 秒 << 24 | 计数」）：

```go
totalRead    atomic.Uint64
totalPublish atomic.Uint64
totalErrors  atomic.Uint64
readRate     *metrics.RateWindow   // 60 个 atomic.Uint64 桶，CAS 无锁
```

每次数据点经过管道时 `totalRead.Add(1)` 是一条 CPU 原子指令，不触发 mutex 调度。超时/降级等调度器健康计数按任务本地累加、在 scheduler 锁下聚合暴露，不占热路径。

## 第六层：传输层 I/O 复用

**HTTP 连接池**预配置，避免每次发布都建 TCP 连接：

```go
t.client = &http.Client{
    Transport: &http.Transport{
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 20,
        IdleConnTimeout:     90 * time.Second,
    },
}
```

**MQTT 发布**：paho 客户端 `Publish` 后以 `token.WaitTimeout(publish-timeout)`（默认 5s）等待确认，发布失败即返回错误交给上层重试/落盘；重连由 paho 内置 auto-reconnect 处理（默认开启，重试间隔加 ±20% 抖动防惊群）。配置了 batcher 时，真正的 `PublishBatch` 在 batcher 自己的 flushLoop goroutine 上执行，处理 worker 只做非阻塞入队，不会被网络 I/O 阻塞；批量发布内部并发发出全部点再统一等待，把整批时延从 N×RTT 降到约一个 RTT。

## 附加：错误抑制

调度器在设备断网时 **10 秒窗口限频**错误日志：

```go
if !runner.inError || errMsg != runner.lastErrMsg || now.Sub(runner.lastErrLog) >= 10*time.Second {
    slog.Error("read failed", ...)
    runner.lastErrLog = now
}
```

防止高频采集任务（如 200ms 间隔）每秒打 5 条错误日志拖垮性能。

## 性能全景

```
                    ┌─ goroutine per task ─┐
                    │  ticker → Read(tags) │  ← 并行 I/O
  PLC/OPC UA ──────►│  3台 × 1s = 3 goroutine│
                    └──────────┬───────────┘
                               │ values
                    ┌──────────▼───────────┐
                    │  dataBus.Push(point)  │  ← drop-oldest
                    │ （快周期走高优先级通道）│
                    └──────────┬───────────┘
                               │ chan（主 cap=8192 / 高优先级 cap≤1024）
                    ┌──────────▼───────────┐
                    │  processingLoop()    │  ← N worker + 2 高优先级 worker
                    │  cache → broadcast   │
                    │  → rule → publish    │
                    └──────────┬───────────┘
                               │
               ┌────────────────┼────────────────┐
               ▼                ▼                ▼
          MQTT (token 确认)  HTTP (conn pool)  WebSocket (fan-out)
```
