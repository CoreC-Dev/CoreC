# 数据流图

下方为 archify 生成的交互式数据流图，展示 CoreC 上行采集管道的完整数据流转。点击图片可全屏查看：

<DiagramFrame src="/diagrams/corec-dataflow.html" title="CoreC 数据流图" height="720px" />

## 管道阶段

| 阶段 | 组件 | 数据类型 | 说明 |
|:---|:---|:---|:---|
| Devices | PLC / S7 / OPC UA | `TagValue` | 设备原始读数（tag, value, type, quality, timestamp） |
| Ingest | Scheduler | `TagValue` | `Driver.Read` 按 interval 分组，goroutine 并行读取 |
| Core | onDriverData → DataBus.Push → Processing Loop | `DataPoint` | TagValue 被 enriched 为 DataPoint（加 driver, group, metadata）；多 worker（`runtime.NumCPU()`）并发消费 |
| Route | Rule Engine | `DataPoint` | 优先级匹配，决定 forward/drop/alert/transform/mirror |
| Cloud | MQTT / HTTP | `DataPoint` | 发布到云端（异步/连接池） |

## 数据类型转换

```
输入：TagValue                          输出：DataPoint
┌──────────────────┐                   ┌──────────────────────┐
│ Tag       string │                   │ Driver    string     │
│ Value     any    │                   │ Device    string     │
│ Type      DataType│  ──enrich──►     │ Group     string     │
│ Quality   Quality│                   │ Tag       string     │
│ Timestamp time   │                   │ Value     any       │
│ Error     error  │                   │ Type      DataType   │
└──────────────────┘                   │ Quality   Quality   │
                                       │ Timestamp time      │
                                       │ IsStale   bool      │
                                       │ Metadata  map       │
                                       └──────────────────────┘
```

`onDriverData()` 是核心转换点：从「裸设备读数」变成「可路由的工业数据点」。

## 背压策略

当采集速度超过处理速度时：

```
DataBus.Push(point)
  ├─ 有空间 → ch <- point     // 直接塞入
  └─ 满了   → 丢最旧 → 塞最新  // drop-oldest
```

这保证了**采集 goroutine 永不阻塞**，工业数据中最新值比历史完整性更重要。

## 链式入站（Chained-core Inbound）

传输可通过 `Transport.OnData()` 接收上游数据（如另一个 CoreC 实例经 MQTT `data-topic` 或 HTTP webhook 推送），由 `startDataListener` goroutine 送入 DataBus，复用同一条处理管道：

```
上游 CoreC → Transport.OnData() → startDataListener → DataBus.Push → ProcessingLoop(多worker) → Rule → Publish
```

这条路径与上行采集汇合于 DataBus，使多级 CoreC 可链式级联而无需额外接线。

## 下行控制

下行控制（Downlink）不经过 DataBus，由每个传输在 `AddTransport` 时启动的独立 `startCommandListener` goroutine 按传输逐个处理命令分发：

```
MQTT command-topic → Transport.OnCommand() → startCommandListener → Driver.Write() → Device
```

详见 [架构总览](/architecture/overview) 中的完整架构图。
