# 高性能设计

核心的高性能不是单一技巧，而是从数据流到 I/O 的**六层设计**，每层解决一个瓶颈。

## 第一层：Channel 驱动的无锁数据流

```
Driver goroutine → DataBus.ch (cap=8192) → processingLoop (多worker) → Transport
```

整个采集→处理→发布管道用一条带 8192 缓冲的 Go channel 串联，生产端跑在调度 goroutine 里，消费端由多个 worker goroutine（默认 `runtime.NumCPU()`）并发消费，**零锁传递数据**。

```go
// engine/engine.go — 创建 8192 缓冲的 DataBus
e.dataBus = NewDataBus(e.dataBusSize) // 默认 8192 (core.DefaultDataBusSize)

// 多 worker goroutine 并发消费（默认 runtime.NumCPU() 个）
numWorkers := e.numWorkers
for i := 0; i < numWorkers; i++ {
    go e.processingLoop()
}
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
        select { case <-b.ch: default: }  // 丢最旧
        select {                            // 非阻塞重试塞最新
        case b.ch <- point:
        default:                            // 仍满则丢弃新点
        }
    }
}
```

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

**实时流订阅**用 `select + default` 非阻塞投递：

```go
func (b *DataBus) Broadcast(point core.DataPoint) {
    b.mu.RLock()
    defer b.mu.RUnlock()
    for _, sub := range b.subscribers {
        select {
        case sub.ch <- point:    // 没满
        default:
            b.dropped.Add(1)     // 满了，丢弃 + 计数
        }
    }
}
```

**读写锁策略**：LatestCache 采用 64 分片设计（按 driver 名 FNV 哈希分片，每片独立 `sync.RWMutex`，降低多 worker 并发更新时的锁竞争），RuleEngine 用 `sync.RWMutex`，N 个 API 请求并发读不互斥。

**规则排序**：规则按 `priority` 升序排列，使用 `sort.Slice`（**非稳定排序**）。相同优先级的规则不保证保留配置文件中的原始顺序；如需稳定顺序应改用 `sort.SliceStable`。

## 第五层：原子计数器

所有运行时统计用 `sync/atomic`，无锁、无分配：

```go
totalRead    atomic.Uint64
totalPublish atomic.Uint64
totalErrors  atomic.Uint64
overruns     atomic.Uint64
```

每次数据点经过管道时 `totalRead.Add(1)` 是一条 CPU 原子指令，不触发 mutex 调度。

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

**MQTT 异步发布**：paho 客户端内置异步 publish + 自动重连，不阻塞 processingLoop。

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
                    └──────────┬───────────┘
                               │ chan (cap=8192)
                    ┌──────────▼───────────┐
                    │  processingLoop()     │  ← 多 worker (NumCPU)
                    │  cache → broadcast    │
                    │  → rule → publish     │
                    └──────────┬───────────┘
                               │
              ┌────────────────┼────────────────┐
              ▼                ▼                ▼
         MQTT (async)    HTTP (conn pool)   WebSocket (fan-out)
```
