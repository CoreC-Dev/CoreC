# 架构总览

CoreC 采用六边形插件架构，将工业数据采集抽象为**南向驱动 → 核心管道 → 北向传输**三层。

## 交互式架构图

下方为 archify 生成的交互式架构图，支持明暗主题切换、pan/zoom、搜索聚焦。点击图片可全屏查看：

<DiagramFrame src="/diagrams/corec-architecture.html" title="CoreC 架构图" height="720px" />

## 三层架构

### 南向层 — Drivers

| 驱动 | 协议 | 接口 |
|:---|:---|:---|
| `modbus-tcp` | Modbus TCP | PLC / 传感器网关 |
| `modbus-rtu` | Modbus RTU | 串口设备 |
| `modbus-rtuovertcp` | Modbus RTU over TCP | 串口网关 |
| `modbus-udp` | Modbus UDP | UDP 设备 |
| `modbus-rtuoverudp` | Modbus RTU over UDP | UDP 串口网关 |
| `modbus-tls` | Modbus TLS | 加密 Modbus TCP |
| `s7` | Siemens S7 | S7-200 / S7-300 / S7-400 / S7-1200 / S7-1500 |
| `opcua` | OPC UA | SCADA / MES / OPC 服务器 |

所有驱动实现统一的 `Driver` 接口：

```go
type Driver interface {
    Init(ctx context.Context, config DriverConfig) error
    Start(ctx context.Context) error
    Stop() error
    Restart(ctx context.Context, config DriverConfig) error
    Read(ctx context.Context, tags []string) ([]TagValue, error)
    Write(ctx context.Context, commands []WriteCommand) ([]WriteResult, error)
    Subscribe(ctx context.Context, tags []string) (<-chan DataPoint, error)
    Name() string
    Type() string
    Status() DriverStatus
    Capabilities() DriverCapabilities
}
```

### 核心层 — Engine

核心是整个系统的核心，包含以下组件：

- **Scheduler** — 为每个「驱动 + 采集周期」组合启动独立 goroutine，用 `time.Ticker` 驱动定时读取；连续失败时自动降级（放慢采集频率），恢复后自动还原
- **DataBus** — 带缓冲的 Go channel（cap=8192），采用 drop-oldest 背压策略
- **Processing Loop** — 多 worker goroutine（默认 `runtime.NumCPU()` 个）并发消费 DataBus channel，依次执行：坏质量策略 → 缓存更新 → 广播 → 规则匹配 → 发布
- **LatestCache** — 64 分片（shard）设计，按 driver 名做 FNV 哈希分片，每片独立 `sync.RWMutex`，降低多 worker 并发更新时的锁竞争；供 API 读取最新值
- **Batcher** — 每个传输可选的批量缓冲器，按 batch-size 或 flush-interval 触发刷新，带指数退避重试；启用全局 buffer 时，重试耗尽后数据落盘缓存，传输恢复后自动回放
- **Parser** — 传输入站数据解析器，支持 `default`/`jsonpath`/`raw` 三种模式，将 MQTT/HTTP 载荷解析为 `DataPoint`
- **Rule Engine** — 优先级排序的规则链（用 `sort.Slice`，**非稳定排序**），`RWMutex` 保护，first-match-wins，支持 5 种动作：forward / drop / alert / transform / mirror
- **Dead Letter Queue** — 写入指令重试耗尽后进入内存死信队列（上限 1000 条），供 `GET /write/failed` 查询

### 北向层 — Transports

| 传输 | 协议 | 特性 |
|:---|:---|:---|
| `mqtt` | MQTT | paho 异步发布，topic 模板渲染，command-topic 反向控制，data-topic 入站（`OnData()`） |
| `http` | HTTP POST | 连接池（MaxIdleConns=100），batch 支持，webhook 入站（`OnData()`） |

## 数据流方向

核心有**四条独立的数据流**：

```
1. 上行采集（Uplink）
   Device → Driver.Read → onDriverData → DataBus.Push → ProcessingLoop(多worker) → Rule → Transport.Publish → Cloud

2. 链式入站（Chained-core Inbound）
   上游 CoreC → Transport.OnData() → startDataListener → DataBus.Push → ProcessingLoop → ...

3. 下行控制（Downlink）
   Cloud → MQTT command-topic → Transport.OnCommand() → startCommandListener → Driver.Write → Device

4. 控制面（Control Plane）
   HTTP Request → API Handler → Engine Method → JSON Response
   WebSocket → Real-time Stream (tags/logs/traffic/memory)
```

## 设计哲学

| 原则 | 实现 |
|:---|:---|
| **最新值优先** | DataBus 满时丢最旧、塞最新，采集永不阻塞 |
| **读多写少** | LatestCache（64 分片）、RuleEngine 用 RWMutex，多读不互斥 |
| **零开销统计** | 所有计数器用 `sync/atomic`，无锁无分配 |
| **非阻塞 fan-out** | WebSocket 订阅用 `select + default`，慢订阅者不影响管道 |
| **错误抑制** | 调度器 10s 窗口限频错误日志，防止日志风暴 |
| **断路器保护** | 驱动连续重连失败达阈值后将重连间隔提升至 5 分钟低频重试，避免资源浪费 |
| **离线缓冲兜底** | 传输失败时数据落盘，传输恢复后自动回放，保证数据完整性 |
| **写入可靠投递** | 控制指令失败自动重试，耗尽后进入死信队列供查询和手动重试 |

## 相关页面

- [数据流图](/architecture/dataflow) — 交互式数据流管道图
- [高性能设计](/architecture/performance) — 六层性能设计详解
- [核心输入输出](/architecture/io) — 输入输出数据类型梳理
