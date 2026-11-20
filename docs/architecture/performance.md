# 高性能设计

核心的高性能不是单一技巧，而是从数据流到 I/O 的**六层设计**，每层解决一个瓶颈。

## 第一层：Channel 驱动的无锁数据流

```
Driver goroutine → DataBus 主通道 (cap=8192) → processingLoop (N worker) ──┬→ Transport
        └────────→ DataBus 高优先级通道 (cap≤1024) → 专属 worker (默认 2 个) ──┘
```

采集→处理→发布管道用带缓冲的 Go channel 串联：主通道默认 `core.DefaultDataBusSize`，快周期任务（interval ≤ 1s）路由到高优先级通道（容量为主通道 1/4、上限 1024、下限 64）。生产端跑在调度 goroutine 里，消费端由 N 个 worker（默认 `runtime.NumCPU()`）并发消费主通道、`core.DefaultHighPriorityWorkers` 个专属 worker 消费高优先级通道，**零锁传递数据**——低频批量读取的突发挤占不了高频采集。

## 第二层：Drop-oldest 背压策略

当采集速度超过处理速度时，DataBus 不阻塞生产端，而是**丢最旧、塞最新**；腾位后仍满则丢弃新点，两种丢弃均计数。

高优先级通道的 `PushHighPriority` 采用完全相同的弃旧语义，丢弃数单独记入 `highPriPushDropped`，两条通道的丢弃量分别经 `corec_databus_dropped_total{channel=main|priority}` 暴露。

## 第三层：并行调度

调度器为每个「驱动 + 采集周期」组合启动独立 goroutine，用 `time.Ticker` 驱动定时读取：多台设备的读取并行发起，总耗时取决于最慢的一台而非各台之和。

## 第四层：非阻塞 fan-out + RWMutex

**实时流订阅**用 `select + default` 非阻塞投递：无订阅者时经原子 `subCount` 快路径跳过加锁；带 filter 的订阅者只收匹配 driver 的点；订阅者通道满时丢弃该点并计数。

**读写锁策略**：LatestCache 采用 64 分片设计（按 driver 名 FNV 哈希分片，每片独立 `sync.RWMutex`，降低多 worker 并发更新时的锁竞争），RuleEngine 用 `sync.RWMutex`，N 个 API 请求并发读不互斥。

**规则排序与驱动索引**：规则按 `priority` 升序排列，使用 `sort.SliceStable`（**稳定排序**）——相同优先级的规则保持配置文件中的原始顺序，且通用规则不会在同优先级下隐式遮蔽 driver 专属规则（构造时记录的序号 seq 作为平级决胜）。匹配带 `driver == 'x'` 前缀的规则进入按驱动分桶的索引，只对来自该驱动的数据点求值；被索引跳过的规则经 `RecordMiss` 记 miss，不破坏统计语义。无索引可用时回退线性扫描。

## 第五层：原子计数器

引擎的吞吐/错误统计用 `sync/atomic`，无锁、无分配；读取速率用无锁滚动窗口计数器 `metrics.RateWindow`（60 个原子桶，每桶打包「unix 秒 << 24 | 计数」）。每次数据点经过管道时的计数累加是一条 CPU 原子指令，不触发 mutex 调度。超时/降级等调度器健康计数按任务本地累加、在 scheduler 锁下聚合暴露，不占热路径。

## 第六层：传输层 I/O 复用

**HTTP 连接池**预配置（`max-idle-conns`、`max-idle-conns-per-host`、`idle-conn-timeout` 可配），请求超时取传输 `timeout` 设置，复用空闲连接发布。

**MQTT 发布**：paho 客户端 `Publish` 后以 `token.WaitTimeout(publish-timeout)`（默认 5s）等待确认，发布失败即返回错误交给上层重试/落盘；重连由 paho 内置 auto-reconnect 处理（默认开启，重试间隔加 ±20% 抖动防惊群）。配置了 batcher 时，真正的 `PublishBatch` 在 batcher 自己的 flushLoop goroutine 上执行，处理 worker 只做非阻塞入队，不会被网络 I/O 阻塞；批量发布内部并发发出全部点再统一等待，把整批时延从 N×RTT 降到约一个 RTT。

## 附加：错误抑制

调度器对重复的读取错误日志按窗口限频：首次失败或错误信息变化时立即记录，否则每个窗口至多一条。窗口默认 `core.DefaultErrorThrottleWindow`，可经 `global.engine.error-throttle-window` 配置。
