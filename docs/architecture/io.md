# 核心输入输出

核心是一个**双向数据管道 + 控制面**，有三条独立的数据流方向。

## 输入（5 类）

### 1. 配置文件

```yaml
# config.yaml — 启动输入
global:     # 日志、API、缓冲
drivers:    # 南向驱动定义
transports: # 北向传输定义
rules:      # 规则链
```

| 入口 | 时机 | 路径 |
|:---|:---|:---|
| `config.Load(path)` | 进程启动 | `cmd/corec/main.go` |
| `PUT /configs` | API 热重载 | `hub/route/configs.go` |
| `PATCH /configs` | 运行时调参 | `hub/route/configs.go` |

### 2. 南向设备数据

**轮询模式** — 调度器主动拉取：

```go
Read(ctx context.Context, tags []string) ([]TagValue, error)
```

| 驱动 | 物理输入 |
|:---|:---|
| Modbus TCP | PLC 保持寄存器、线圈、离散输入 |
| Siemens S7 | DB 块、Merker、输入(I)、输出(Q) |
| OPC UA | NodeID 节点变量 |

**订阅模式** — 设备主动推送（仅 OPC UA）：

```go
Subscribe(ctx context.Context, tags []string) (<-chan DataPoint, error)
```

输入数据类型：

```go
type TagValue struct {
    Tag       string    // "temperature"
    Value     any       // 42.5
    Type      DataType  // TypeFloat32
    Quality   Quality   // QualityGood
    Timestamp time.Time // 采集时间戳
    Error     error     // 读取错误
}
```

### 3. 北向控制指令

云端通过 MQTT command topic 下发写指令：

```go
type WriteCommand struct {
    Driver string   // "plc-modbus"
    Device string   // "192.168.1.10"
    Tag    string   // "temperature"
    Value  any      // 50
    Type   DataType // TypeFloat32
}
```

流向：MQTT 订阅 command-topic → `Transport.OnCommand()` → `startCommandListener` → `Driver.Write()` → PLC

> 注：每个传输在 `AddTransport` 注册时启动各自的 `startCommandListener` goroutine 完成命令分发并参与优雅停机。

### 4. 控制面 API 请求

| 请求 | 输入 |
|:---|:---|
| `POST /write` | 直接写设备 |
| `PUT /configs` | 热重载配置 |
| `PATCH /configs` | 运行时调参 |
| `PATCH /rules/disable` | 禁用规则 |
| WebSocket | 订阅实时流 |

### 5. 链式入站数据（Transport.OnData）

传输可通过 `Transport.OnData()` 接收上游数据点（如另一个 CoreC 实例经 MQTT `data-topic` 或 HTTP webhook 推送），由 `startDataListener` goroutine 送入 DataBus 复用处理管道。入站载荷经 **Parser** 解析为 `DataPoint`：

```go
// core/transport.go — Transport 接口包含 OnData() 入站通道
OnData() <-chan DataPoint
```

Parser 支持三种模式（`transport/parser`）：

| 模式 | 说明 | 适用场景 |
|:---|:---|:---|
| `default` | 直接 `json.Unmarshal` 为 `DataPoint` | CoreC→CoreC 链式级联（双方共享同一 JSON schema） |
| `jsonpath` | 通过可配置模板把任意 JSON 字段映射到 `DataPoint` | 接入第三方 JSON 数据源 |
| `raw` | 把整个载荷当作标量值 | 简单数值上报 |

流向：上游 → MQTT `data-topic` / HTTP `webhook-addr` → `Parser.Parse` → `Transport.OnData()` → `startDataListener` → `DataBus.Push` → ProcessingLoop

## 输出（5 类）

### 1. 北向数据发布

主输出流，采集数据经规则匹配后发布到云端：

```go
type DataPoint struct {
    Driver    string            // "plc-modbus"
    Device    string            // ""（始终为空）
    Group     string            // "sensors"
    Tag       string            // "temperature"
    Value     any               // 42.5
    Type      DataType          // TypeFloat32
    Quality   Quality           // QualityGood
    Timestamp time.Time         // 采集时间
    Metadata  map[string]string // 扩展元数据
}
```

| 传输 | 输出形式 | 目标 |
|:---|:---|:---|
| MQTT | JSON → topic `factory/plc-modbus/sensors/temperature` | EMQX / Mosquitto |
| HTTP | JSON batch → POST | MES / Data Lake |

规则动作决定路由：

| Action | 行为 |
|:---|:---|
| `forward` | 发到单个 target |
| `mirror` | 发到多个 targets |
| `drop` | 丢弃 |
| `alert` | 发 + 触发告警 |
| `transform` | 经表达式变换值（可重命名 tag）后发到 target |

### 2. 设备写入结果

```go
type WriteResult struct {
    Success bool
    Error   string
}
```

### 3. 实时流推送

| 端点 | 输出 |
|:---|:---|
| `/tags/stream` | `DataPoint` JSON 流 |
| `/logs` | `log.Event` JSON 流 |
| `/traffic` | `{"read":N,"publish":N,"dropped":N}` |
| `/memory` | `{"alloc":N,"total_alloc":N,"sys":N,"num_gc":N,"goroutines":N}` |

### 4. API 响应

| 端点 | 输出 |
|:---|:---|
| `GET /drivers` | 驱动列表 + 状态 |
| `GET /tags` | 最新缓存值 |
| `GET /stats` | `EngineStats`（含 `total_dropped`） |
| `GET /rules` | 规则列表 + 命中统计 |

### 5. 告警 + 日志

规则匹配 `alert` 动作时触发告警 handler，所有 `slog` 调用经 Observable 双路输出。

## 数据类型流转

```
输入                    核心处理                    输出
─────────────────────────────────────────────────────────────
TagValue ─Read()──►  onDriverData  ──►  DataPoint  ──Publish()──► MQTT/HTTP
  (设备原始值)         │                 (enriched)      │
                       │                 ├──► cache ──► API JSON
                       │                 ├──► alert ──► slog.Warn
                       │                 └──► (drop)
                       │
DataPoint ─OnData()──► startDataListener ─► DataBus.Push ─► ProcessingLoop
  (链式入站)            (复用同一处理管道)

WriteCommand ─MQTT──►  startCommandListener  ──►  Driver.Write()  ──► PLC
  (云端指令)                                    │
                                                └──► WriteResult ──► API JSON
```

**核心转换**：`TagValue`（输入）→ `DataPoint`（输出）发生在 `onDriverData()`，加上 `Driver`、`Group` 等路由元数据，使规则引擎能按字段匹配路由。
