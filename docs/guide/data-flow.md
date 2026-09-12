---
title: 数据流
description: 从设备到云端的全链路数据流 —— TagValue 到 DataPoint 的富化、管道各阶段与丢弃策略
---

# 数据流

理解数据在 CoreC 内部的流转路径，是掌握整个系统行为的关键。本章详细拆解从设备寄存器到北向端点的每一个阶段。

## 全链路总览

```
┌─────────┐  Read    ┌───────────┐  Push    ┌─────────┐  Channel  ┌───────────────┐
│ Device  │ ───────▶ │  Driver   │ ───────▶ │ DataBus │ ────────▶ │ processingLoop│
│ (PLC)   │          │ (南向)    │          │  chan   │           │   (规则匹配)  │
└─────────┘          └───────────┘          │  cap=   │           └───────┬───────┘
                     onDriverData           │  8192   │                   │
                     TagValue→DataPoint     └────┬────┘                   │
                                                │ Broadcast            Match
                                                ▼                          │
                                         ┌────────────┐                   ▼
                                         │ Subscriber │           ┌──────────────┐
                                         │  (WS/API)  │           │  Rule Engine │
                                         └────────────┘           └──────┬───────┘
                                                                       │ Action
                                                                       ▼
                                                                  ┌──────────┐
                                                                  │Transport │
                                                                  │ (北向)   │
                                                                  └──────────┘
```

## 两个核心数据结构

### TagValue —— 驱动层的原始读值

`TagValue` 是驱动从设备读取后返回的原始结构，只包含与设备直接相关的信息：

```go
type TagValue struct {
    Tag       string    // 测点名
    Value     any       // 原始值
    Type      DataType  // 数据类型
    Quality   Quality   // 质量戳 (good/bad/uncertain)
    Timestamp time.Time // 读取时刻
    Error     error     // 读取错误（不序列化）
}
```

此时数据**还没有**路由元信息——不知道来自哪个驱动、属于哪个分组。这是设备协议层的纯粹表达。

### DataPoint —— 引擎层的标准数据单元

`DataPoint` 是在 `DataBus` 中流转、经规则匹配、最终推送到北向传输的标准单元：

```go
type DataPoint struct {
    Driver    string            // 来源驱动名（富化填充）
    Device    string            // 设备标识
    Group     string            // 点位分组（来自 TagConfig.Group）
    Tag       string            // 测点名
    Value     any               // 值
    Type      DataType          // 数据类型
    Quality   Quality           // 质量戳
    Timestamp time.Time         // 时间戳
    Metadata  map[string]string // 扩展元数据
}
```

### 富化过程（TagValue → DataPoint）

富化发生在引擎的 `onDriverData` 回调中。调度器每次读取完成后，将 `[]TagValue` 交给引擎，引擎逐个补充路由元信息（驱动名与分组）：

```go
func (e *CoreCEngine) onDriverData(driver string, values []core.TagValue) {
    // 按 driver 取出 tag → group 映射，一次读取避免逐点加锁
    groups := e.tagGroups[driver]

    for _, v := range values {
        point := core.DataPoint{
            Driver:    driver,     // ← 富化：补充驱动名
            Tag:       v.Tag,
            Value:     v.Value,
            Type:      v.Type,
            Quality:   v.Quality,
            Timestamp: v.Timestamp,
        }
        if groups != nil {
            point.Group = groups[v.Tag]  // ← 富化：按测点名查表补充分组
        }
        e.dataBus.Push(point)     // 推入数据总线
    }
}
```

::: info 为什么需要富化？
规则引擎需要依据 `driver`、`group`、`tag` 等字段做路由决策，北向传输的主题模板（如 `factory/&lbrace;&lbrace;.Driver&rbrace;&rbrace;/&lbrace;&lbrace;.Group&rbrace;&rbrace;/&lbrace;&lbrace;.Tag&rbrace;&rbrace;`）也需要这些字段。富化将设备层的原始读值升级为携带完整路由上下文的标准数据点。
:::

## 管道各阶段详解

### 阶段 1：调度触发（Scheduler）

调度器为每个「驱动 + 采集周期」组合创建一个独立 goroutine，使用 `time.Ticker` 周期触发：

```
驱动 demo-plc 有三个点位：
  temperature  interval=1s
  pressure     interval=1s
  pump_status  interval=2s

调度器按 interval 分组，创建两个任务：
  任务 demo-plc_1s  → [temperature, pressure]  每 1s 触发
  任务 demo-plc_2s  → [pump_status]            每 2s 触发
```

每个任务一个 goroutine，互不阻塞。触发时调用 `driver.Read(ctx, tags)` 批量读取该周期内所有点位。

::: tip 按周期分组的优势
同一周期的点位合并为一次批量读取请求，减少协议交互次数。例如 100 个 1s 点位只需每秒一次 Modbus 请求，而非 100 次。
:::

### 阶段 2：驱动读取（Driver.Read）

驱动将测点名翻译为协议地址，发起实际读取。以 Modbus 为例：

```
Read(ctx, ["temperature", "pressure"])
  → 解析 address "40001" → 保持寄存器 0
  → client.ReadFloat32(0)
  → 返回 TagValue{Tag:"temperature", Value:42.5, Quality:good, ...}
```

读取失败时，对应点位的 `Quality` 设为 `bad` 并填充 `Error`，但**不会中断整批读取**——其他点位仍正常返回。

### 阶段 3：富化与入队（onDriverData → DataBus.Push）

如上文所述，`TagValue` 被富化为 `DataPoint` 后推入 `DataBus`。

### 阶段 4：数据总线（DataBus）

`DataBus` 是连接采集协程与处理协程的 Go channel，容量 **8192**：

```go
e.dataBus = NewDataBus(8192)  // engine.go
```

#### 丢弃策略（Drop-Oldest）

当总线满时，`Push` 采用 **drop-oldest** 策略而非阻塞：

```go
func (b *DataBus) Push(point core.DataPoint) {
    select {
    case b.ch <- point:        // 正常入队
    default:                   // 队列满
        select {
        case <-b.ch:            // 丢弃最旧的一个
        default:
        }
        b.ch <- point           // 入队新数据
    }
}
```

::: warning 为什么丢弃而非阻塞？
工业场景中过时数据的价值快速衰减。如果选择阻塞采集协程，会导致：
1. 调度周期被拖长，引发 overrun 雪崩
2. 设备连接因超时断开
3. 所有点位采集集体停滞

丢弃最旧数据保证采集协程始终以设备节奏运行，过载通过 `PushDropped()` 计数器暴露给运维。
:::

总线满时丢弃的最旧数据通过 `DataBus.PushDropped()` 计数；下游慢订阅者被跳过的数据通过 `DataBus.Dropped()` 计数。两者（再加上日志环缓冲丢弃）聚合到引擎统计的 `TotalDropped` 字段，可通过 `/stats` API 查询。

### 阶段 5：处理循环（processingLoop）

引擎启动多个 worker goroutine（默认 `runtime.NumCPU()` 个）并行消费 `DataBus` 的 channel，每个 worker 对每个 `DataPoint` 依次执行：

```go
func (e *CoreCEngine) processingLoop() {
    for {
        select {
        case <-e.ctx.Done():
            return
        case point, ok := <-e.dataBus.Channel():
            // ① 更新最新值缓存
            e.cache.Update(point)

            // ② 广播给订阅者（WebSocket / API 流）
            e.dataBus.Broadcast(point)

            // ③ 规则匹配
            result := e.ruleEngine.Match(point)
            if result == nil {
                continue  // 无规则匹配，数据止于此
            }

            // ④ 执行动作
            switch result.Rule.Action() {
            case core.ActionDrop:
                continue
            case core.ActionAlert:
                e.fireAlert(point, result.Rule)
                e.publishToTargets(point, result.Targets)
            case core.ActionTransform:
                e.publishToTargets(e.applyTransform(point, result.Transform), result.Targets)
            case core.ActionForward, core.ActionMirror:
                e.publishToTargets(point, result.Targets)
            }
        }
    }
}
```

### 阶段 6：最新值缓存（LatestCache）

每个 `DataPoint` 都会更新 `LatestCache`。为降低多 worker 并发更新时的锁竞争，缓存采用 **64 分片（shard）设计**——按驱动名哈希取模分散到 64 个分片，每个分片各自持有 `RWMutex`：

```go
const numCacheShards = 64

type LatestCache struct {
    shards [numCacheShards]cacheShard  // 按 driver 名 FNV-1a 哈希取模定位分片
}

type cacheShard struct {
    mu       sync.RWMutex
    drivers  map[string]*driverCache   // 该分片内的 driver → tag → DataPoint
}
```

缓存支撑按需查询：

- `GET /drivers/{name}/tags` —— 返回某驱动所有点位的最新值
- `GET /tags` —— 返回全部驱动全部点位的最新值

> 这两个端点都**不接受** `driver` / `tag` 查询参数；按驱动筛选请使用 `/drivers/{name}/tags` 路径参数形式。

::: tip 缓存与调度的关系
`LatestCache` 总是保存每个点位的**最近一次成功读值**，无论该值是否被规则转发。即使某点位被规则 `drop`，缓存中仍有值可供 API 查询。这使得"采集"与"转发"两个关注点彻底解耦。
:::

### 阶段 7：广播给订阅者（Broadcast）

`DataBus.Broadcast` 将数据点推送给所有通过 `Subscribe(filter)` 注册的订阅者（主要是 WebSocket `/tags/stream` 端点）：

```go
func (b *DataBus) Broadcast(point core.DataPoint) {
    for _, sub := range b.subscribers {
        select {
        case sub.ch <- point:   // 正常推送
        default:                // 订阅者缓冲满
            b.dropped.Add(1)     // 跳过并计数
        }
    }
}
```

每个订阅者有独立的 256 容量缓冲 channel。慢订阅者（如 WebSocket 客户端处理不过来）会被跳过而非阻塞主流水线，跳过次数计入 `DataBus.Dropped()`。

### 阶段 8：规则匹配与动作执行

详见 [规则引擎](./rules.md) 章节。处理循环将匹配结果中的目标传输列表交给 `publishToTargets`：

```go
func (e *CoreCEngine) publishToTargets(point core.DataPoint, targets []string) {
    for _, targetName := range targets {
        transport, ok := e.transports[targetName]
        if !ok {
            continue  // 目标传输不存在，告警并跳过
        }
        transport.Publish(e.ctx, point)
    }
}
```

### 阶段 9：北向发布（Transport.Publish）

传输将 `DataPoint` 序列化后发往外部系统。以 MQTT 为例，使用主题模板渲染目标主题，JSON 序列化后异步发布：

```
DataPoint{Driver:"demo-plc", Group:"sensors", Tag:"temperature", Value:42.5}
  → 主题模板 "factory/{{.Driver}}/{{.Group}}/{{.Tag}}"
  → 渲染为 "factory/demo-plc/sensors/temperature"
  → JSON 序列化
  → paho MQTT 异步发布 (QoS 1)
```

## 反向数据流：控制指令下发

除了上行采集，CoreC 还支持下行控制。北向传输可以通过 `OnCommand()` 暴露一个 `<-chan WriteCommand` 反向通道：

```
MQTT Command Topic ──订阅──▶ Transport.OnCommand() ──chan──▶ startCommandListener ──▶ Driver.Write ──▶ 设备
```

引擎为**每个传输**启动一个独立的 `startCommandListener` goroutine 监听该传输的 `OnCommand()` 通道，收到 `WriteCommand` 后调用对应驱动的 `Write` 方法写入设备。`commandLoop` goroutine 本身只等待 `ctx.Done()`，仅用于被 `WaitGroup` 跟踪以保证优雅退出：

```go
type WriteCommand struct {
    Driver string   // 目标驱动
    Device string   // 设备标识
    Tag    string   // 目标测点
    Value  any      // 待写入值
    Type   DataType // 值类型
}
```

以 MQTT 为例，配置 `command-topic: "factory/commands/#"` 后，向该主题发布 JSON 格式的 `WriteCommand` 即可触发设备写入，形成闭环控制。

## 数据质量戳

CoreC 借鉴 OPC UA 的质量戳概念，每个数据点都携带 `Quality` 字段：

| 质量 | 值 | 含义 |
|:---|:---:|:---|
| `good` | 0 | 读取成功，值可信 |
| `bad` | 1 | 读取失败（连接断开、地址错误等） |
| `uncertain` | 2 | 不确定（预留扩展） |

质量为 `bad` 的数据点仍会进入总线与缓存（便于感知设备状态），规则可以据此过滤：

```yaml
rules:
  - name: drop-bad-quality
    match: "quality == 'bad'"
    action: drop
    priority: 1
```

## 性能特征

| 环节 | 并发模型 | 阻塞风险 |
|:---|:---|:---|
| 调度读取 | 每任务一 goroutine，互不阻塞 | 单任务超时不影响其他任务 |
| 数据总线 | Go channel，无锁 | 满时 drop-oldest，从不阻塞采集 |
| 处理循环 | 多 goroutine 并行消费（默认 `runtime.NumCPU()` 个 worker） | 规则匹配为纯内存操作，极快 |
| 北向发布 | 同步 Publish，逐目标串行 | 发布失败计数，不阻塞主流水线 |
| 订阅广播 | 非阻塞 select，慢消费者跳过 | 永不阻塞主流水线 |

::: tip 吞吐量参考
在单核容器环境下，CoreC 可稳定支撑数千点位的秒级采集与转发。瓶颈通常在北向传输的远端系统（MQTT Broker、HTTP 服务端）而非核心本身。通过 `mirror` 动作分摊到多传输、或调整 `batch-size` / `flush-interval` 可进一步优化。
:::

## 下一步

- [驱动](./drivers.md) —— 深入 `Driver` 接口与三种协议的能力差异
- [传输](./transports.md) —— 北向传输的发布、批量与反向通道
- [规则引擎](./rules.md) —— 控制数据如何路由与变换
