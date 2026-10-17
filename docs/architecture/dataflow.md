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

## 交付语义（Delivery Semantics）

正向数据链路（Batcher → Transport）是 **at-least-once**，不是 exactly-once。重复是设计取舍，而非缺陷——下游必须幂等：

- **部分成功 → 整批重试 → 可能重复**。`PublishBatch` 以整批为单位重试，不做单点状态跟踪：一批 100 点中若 1 点发布失败，重试会整批重发，已成功的 99 点会再次投递。MQTT 默认 QoS 1（broker 侧同样 at-least-once），重复消息会被投给订阅者。运行时重试日志已提示该语义。
- **离线回放天然重复**。批次落盘后由 drain 循环回放：发布成功后删除文件；若进程在「发布成功」与「删除文件」之间崩溃，重启后该批会再次回放。`os.Remove` 失败（日志记录 Warn）时该批同样会于下一轮重复发布。
- **下游幂等建议**：消费侧按 **(driver, tag, timestamp)** 去重（或利用 QoS 1 + 幂等存储），将重复投递合并为一次写入。
- **关停窗口**：优雅关停只保证**已进入 Batcher** 的数据被 flush（发布，失败则落盘）。仍滞留在 DataBus 通道、未被处理管线消费的点位在关停时被丢弃——丢弃有 Warn 日志并计入 error 计数，但不会落盘。第二个 SIGINT/SIGTERM 会强制退出，正在进行的 flush 不再保证完成。

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
