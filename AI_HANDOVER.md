# CoreC 项目 AI 深度交接与架构文档 (AI Handover Guide)

> **致接手的 AI 助手 / 开发者**：  
> 本文档是 **CoreC** 工业数据采集核心的技术全景与设计精髓交接说明。它记录了项目的架构逻辑、代码拓扑、已实现协议、核心关键设计以及未来演进路线，帮助你在接手本项目时能够零延迟理解并继续迭代。

---

## 1. 项目定位与核心哲学

### 1.1 什么是 CoreC？
**CoreC (Connect · Collect · Control)** 是一个面向工业自动化、工业物联网 (IIoT) 与边缘计算的**高性能、轻量级、配置驱动的数据采集与控制核心**。

### 1.2 为什么做 CoreC？（现有方案痛点）
- **EMQX Neuron**：虽然定位好，但高级协议驱动闭源收费（商业版 NeuronEX），且自带 Web UI，更接近完整应用而非纯嵌入式核心。
- **EdgeX Foundry**：微服务架构过于沉重，单机部署需要启动多个容器与服务总线，学习曲线陡峭。
- **Apache PLC4X**：本质上只是协议底层编解码库，不提供定时采集调度、最新值缓存、数据总线与北向转发管线。
- **Shifu**：强绑定 Kubernetes CRD，无法直接以单二进制运行在轻量级边缘网关或嵌入式工控机上。

### 1.3 核心设计：六边形（端口与适配器）架构
CoreC 采用 **“六边形端口与适配器架构 (Ports and Adapters)”**，将其映射为工业数据流：
- **Inbound 适配器** $\rightarrow$ **南向驱动 (Driver)**：主动周期轮询或事件订阅从 PLC / 传感器获取数据。
- **Tunnel / Routing** $\rightarrow$ **核心引擎 (Engine) 与 规则匹配 (Rule)**：无锁 Channel 数据总线、最新值缓存、优先级规则分发。
- **Outbound 适配器** $\rightarrow$ **北向传输 (Transport)**：向 MQTT Broker、HTTP Webhook / 平台推送（Kafka、gRPC 等为规划项）；同时提供反向通道接收下发指令写回 PLC。
- **Config-driven** $\rightarrow$ **声明式 YAML**：统一声明 `node`（可选，拓扑自动发现）、`global`、`drivers`、`transports`、`rules`。
- **Factory Registration** $\rightarrow$ **`init()` 自动工厂注册**：新协议只需实现接口并通过 `init()` 注册，零侵入扩展。

---

## 2. 项目代码结构拓扑

```
corec/
├── cmd/corec/
│   └── main.go                  # CLI 主入口（Banner打印、配置加载、日志初始化、生命周期管理与优雅停机）
├── config/                      # 配置模块
│   ├── config.go                # YAML 配置解析与业务语义校验（唯一性、目标存在性、tags-file 外部标签加载）
│   └── config_test.go           # 配置文件解析与校验单元测试
├── core/                        # 核心契约层（领域模型与标准接口，无外部依赖）
│   ├── types.go                 # 数据模型: DataPoint, TagValue, WriteCommand, DataType, Quality, Action 枚举, ConnState, WriteResult, DeadLetterEntry, TagConfig
│   ├── types_test.go            # 数据类型解析单元测试
│   ├── driver.go                # 南向 Driver 接口定义、DriverConfig、DriverStatus、Capabilities
│   ├── transport.go             # 北向 Transport 接口定义、TransportConfig、TransportStatus
│   ├── rule.go                  # 规则 Rule 接口定义、RuleConfig、RuleProviderConfig、TransformConfig、RuleStat
│   ├── scheduler.go             # 采集调度器 Scheduler 接口定义、ScheduleTask
│   ├── engine.go                # 核心中枢 Engine 接口定义、EngineStats、Config 顶层结构
│   ├── logger.go                # Logger 端口接口 + NoopLogger（端口已定义，尚未注入驱动；驱动当前仍直接用 log/slog）
│   ├── metrics.go               # Metrics 端口接口 + NoopMetrics（端口已定义，尚未注入驱动；未来重构接入 Prometheus 等后端）
│   ├── registry.go              # 全局工厂注册表 (DriverFactory / TransportFactory)
│   └── registry_test.go         # 注册表单元测试
├── driver/                      # 南向驱动实现层
│   ├── modbus/
│   │   ├── modbus_base.go      # 共享基类（连接管理、重连、地址解析、全区段读写、handleConnectionLost）
│   │   ├── tcp.go              # Modbus TCP 驱动
│   │   ├── rtu.go              # Modbus RTU 驱动（串口 RS-232/RS-485）
│   │   ├── net.go              # UDP / RTU-over-TCP / RTU-over-UDP 驱动
│   │   ├── tls.go              # Modbus TCP over TLS（mTLS）驱动
│   │   ├── register.go         # init() 注册 6 种 Modbus 类型（tcp/rtu/udp/rtuovertcp/rtuoverudp/tls）
│   │   ├── tcp_test.go         # 内置 Mock TCP Server 回环集成测试
│   │   ├── rtu_test.go         # RTU 驱动测试
│   │   ├── net_test.go         # UDP / RTU-over-* 驱动测试
│   │   ├── tls_test.go         # TLS 驱动测试
│   │   └── tls_test_helper.go  # TLS 测试辅助
│   ├── s7/
│   │   ├── s7.go                # 西门子 S7 驱动（集成 robinson/gos7，支持 S7-200~1500）
│   │   ├── register.go          # init() 注册 "s7"
│   │   └── s7_test.go           # S7 地址语法解析与大小端转换测试
│   ├── opcua/
│   │   ├── client.go            # OPC UA 客户端驱动（集成 gopcua/opcua，支持 Read/Write/Sub）
│   │   ├── register.go          # init() 注册 "opcua"
│   │   └── client_test.go       # NodeID 解析与参数初始化测试
│   └── all/all.go               # 空导入聚合引入所有南向驱动
├── transport/                   # 北向传输实现层
│   ├── mqtt/
│   │   ├── publisher.go         # MQTT 传输（集成 paho.mqtt，动态 Topic，支持 Command 反向指令）
│   │   ├── register.go          # init() 注册 "mqtt"
│   │   └── publisher_test.go    # Topic 模板与配置解析测试
│   ├── httppush/
│   │   ├── push.go              # HTTP Push 传输（原生连接池复用，批量 JSON，自定义 Header）
│   │   ├── register.go          # init() 注册 "http"
│   │   ├── push_test.go         # 基于 httptest.Server 的推送测试
│   │   └── webhook_test.go      # Webhook（chained-core 入站）测试
│   ├── parser/
│   │   ├── parser.go            # 可插拔载荷解析器（将 MQTT/HTTP 载荷解析为 DataPoint，支持 default/jsonpath/raw）
│   │   └── parser_test.go       # 解析器单元测试
│   └── all/all.go               # 空导入聚合引入所有北向传输
├── rule/                        # 规则路由模块
│   ├── arith.go         # 算术表达式求值（expr-lang/expr，含编译缓存）
│   ├── engine.go        # 规则引擎（优先级切片排序、字段等值与数值区间比较、命中/未命中统计、运行时禁用）
│   ├── expr.go          # DSL→expr-lang/expr 翻译层（~176 行，依赖 expr-lang/expr）
│   ├── provider.go      # RULE-SET 外部规则集 provider
│   ├── wrapper.go       # RuleWrapper（命中/未命中原子计数、运行时禁用）
│   ├── arith_test.go    # 算术辅助单元测试
│   ├── engine_test.go   # 规则匹配、统计与禁用单元测试
│   └── p1p2_test.go     # 表达式解析 P1/P2 回归测试
├── engine/                      # 引擎核心实现
│   ├── engine.go                # Engine 编排器（流水线、驱动/传输生命周期、Suspend/Resume、autoFillNodeConfig、startDiscovery、tagFileWatchers）
│   ├── tagfile.go               # 标签文件热重载 watcher（SHA-256 变更检测 + ticker 轮询）
│   ├── discovery.go             # 拓扑自动发现（MQTT 心跳、节点注册表、auto-subscribe）
│   ├── scheduler.go             # Ticker 并发采集调度器（含错误抑制机制、Pause/Resume）
│   ├── databus.go               # 高并发无锁 Go Channel 内部数据总线
│   ├── cache.go                 # 最新测点值并发安全实时缓存 (LatestCache)
│   ├── batcher.go               # 传输批量聚合与重试
│   ├── offlinebuffer.go         # 离线持久化缓冲（传输中断时数据写磁盘，恢复后自动补传）
│   ├── engine_test.go           # 引擎生命周期集成测试
│   ├── chained_core_test.go       # 链式核心（relay）集成测试
│   ├── chained_core_scenarios_test.go # 链式核心多场景测试
│   ├── chained_core_bug_test.go  # 链式核心回归/边界测试
│   ├── chained_core_bench_test.go # 链式核心性能基准
│   ├── bench_test.go            # 引擎性能基准
│   └── statistic/
│       ├── manager.go           # 吞吐量统计管理器（原子计数 + Snapshot）
│       └── manager_test.go      # 统计管理器单元测试
├── common/
│   ├── observable/
│   │   ├── observable.go        # 泛型 Observable 事件总线（非阻塞扇出）
│   │   └── observable_test.go   # Observable 单元测试
│   ├── trace/
│   │   ├── trace.go             # 轻量级追踪：context 传播 trace ID + Span 计时（End() 以 slog.Debug 记录）。无 OpenTelemetry/OTLP 导出；Span/Start API 被 engine.readFromDriver 调用
│   │   ├── middleware.go        # HTTP 中间件：解析入站 W3C traceparent 头提取 trace ID（无则取 chi RequestID 或生成），用于 hub/route；InjectTraceparent() 向出站 HTTP 请求注入 traceparent
│   │   └── trace_test.go        # 追踪单元测试
│   └── util/
│       └── util.go              # 共享辅助函数（设置提取、时长解析、ReconnectLoopWithBreaker 含 ±20% jitter 指数退避与断路器）
├── log/                         # 可观测日志系统
│   ├── level.go                 # LogLevel 枚举与映射
│   ├── log.go                   # ObservableHandler 包装 slog，自动捕获所有日志调用 + 便捷函数
│   └── log_test.go             # 日志订阅、级别过滤、多订阅者测试
├── hub/                         # 控制面 Hub（RESTful API 与配置热重载）
│   ├── hub.go                   # Hub 启动/停止入口
│   ├── executor/
│   │   ├── executor.go          # 配置热重载执行器（Suspend→Diff→Apply→Resume）
│   │   └── executor_test.go     # 执行器单元测试
│   └── route/
│       ├── server.go            # HTTP 服务器、路由注册、认证（hmac.Equal 常量时间）、CORS、限流、TLS 中间件
│       ├── common.go            # JSON 渲染辅助
│       ├── health.go            # /healthz/live + /healthz/ready（Kubernetes 探针，免认证）
│       ├── configs.go           # /configs 端点
│       ├── drivers.go           # /drivers 端点（含 staleness 标注）
│       ├── transports.go        # /transports 端点
│       ├── tags.go              # /tags + /write 端点（annotateStaleness 标注过期缓存测点）
│       ├── rules.go             # /rules（含统计）+ /rules/disable 端点
│       ├── stats.go             # /stats 端点
│       ├── metrics.go           # /metrics 端点（Prometheus 文本格式，无外部 client 库，取自 engine.Stats()）
│       ├── logs.go              # /logs WebSocket（实时日志流）
│       ├── traffic.go           # /traffic WebSocket（吞吐量流）
│       ├── memory.go            # /memory WebSocket（内存使用流）
│       ├── stream.go            # /tags/stream WebSocket（实时数据点流）
│       ├── route_test.go        # 路由端点集成测试
│       └── handlers_test.go     # 端点处理器单元测试
├── config.example.yaml          # 生产级示例配置文件
├── go.mod                       # 模块依赖 (module github.com/CoreC-Dev/CoreC, Go 1.27.1)
├── go.sum                       # 校验和
├── README.md                    # 项目快速指引与介绍
└── .gitignore                   # Git 忽略配置（忽略二进制、本地日志等）
```

---

## 3. 核心接口与数据流模型

### 3.1 核心数据结构 (`core/types.go`)
- **`TagValue`**：驱动层直接读取出的原始值，携带测点名、Go `any` 值、数据类型枚举、数据质量（`QualityGood`, `QualityBad`, `QualityUncertain`）、物理时间戳及底层错误。
- **`DataPoint`**：流经数据总线与规则引擎的标准事件实体：
  ```go
  type DataPoint struct {
      Driver    string            `json:"driver"`
      Device    string            `json:"device"`
      Group     string            `json:"group"`
      Tag       string            `json:"tag"`
      Value     any               `json:"value"`
      Type      DataType          `json:"type"`
      Quality   Quality           `json:"quality"`
      Timestamp time.Time         `json:"timestamp"`
      Metadata  map[string]string `json:"metadata,omitempty"`
      IsStale   bool              `json:"is_stale,omitempty"` // API 层标注缓存值过期，正常流水线不设置
  }
  ```
- **`WriteCommand`**：反向控制下发结构：包含目标 `Driver`、`Device`、`Tag`、写入 `Value` 及期望 `DataType`。

### 3.2 南向驱动契约 (`core/driver.go`)
所有驱动必须实现 `core.Driver` 接口：
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

### 3.3 北向传输契约 (`core/transport.go`)
所有传输必须实现 `core.Transport` 接口：
```go
type Transport interface {
    Init(ctx context.Context, config TransportConfig) error
    Start(ctx context.Context) error
    Stop() error

    Publish(ctx context.Context, point DataPoint) error
    PublishBatch(ctx context.Context, points []DataPoint) error
    OnCommand() <-chan WriteCommand // 反向指令下发通道
    OnData() <-chan DataPoint // 链式核心上行数据通道

    Name() string
    Type() string
    Status() TransportStatus
}
```

### 3.4 双向数据流向拓扑
```
[定时周期 / Ticker]
        │
        ▼
   Scheduler ──> Driver.Read()
                    │
                    ▼ []TagValue
               DataBus.Push() (无锁 Channel)
                    │
       ┌────────────┴────────────┐
       ▼                         ▼
  LatestCache (最新值更新)   Pipeline (规则匹配)
                                 │
                   ┌─────────┼─────────┼─────────┼─────────┐
                   ▼         ▼         ▼         ▼         ▼
                   Forward   Drop      Alert     Mirror    Transform
                   │         │         │         │         │
                   └─────────┴─────────┬─────────┴─────────┘
                                       │
                                       ▼
                              Transport.Publish()
                                 (MQTT / HTTP)

[反向控制流下发]:
MQTT Command Topic ──> Transport.OnCommand() ──> Engine.startCommandListener() (per-transport goroutine) ──> Driver.Write() ──> PLC 寄存器
```

---

## 4. 关键设计细节与避坑经验 (Crucial Gotchas)

### 4.1 高频采样断网日志抑制 (Error Throttling)
- **痛点**：在工控现场，测点采样频率往往达到 200ms 或 100ms。当 PLC 掉电或网络闪断时，调度器每秒会狂刷 5~10 次相同的 `read failed: driver not connected` 日志，极快塞满磁盘并淹没重要日志。
- **解决方案 (`engine/scheduler.go`)**：
  - 任务运行器 `taskRunner` 内部维护 `inError`, `lastErrMsg`, `lastErrLog`, `errCount`。
  - **首次故障**立即打印日志；**错误内容变更**立即打印日志。
  - **持续相同错误**启动 10 秒时间窗口聚合抑制，10 秒内只打 1 条日志，并在日志中输出 `repeated_count=50`。
  - **故障恢复**时，自动打印一条 INFO 级别的 `msg="read recovered"`。

### 4.2 调度超限限频 (Overrun Throttling)
- **痛点**：当采集间隔配置为 1s，但 TCP 握手或读取超时设定为 3s 时，`latency > interval` 会发生调度超限告警。
- **解决方案**：超限告警与错误抑制统一，加入 10 秒窗口与 `occurrences` 计数器。

### 4.3 并发重连防抖
- 在 `driver/modbus/modbus_base.go` 中的 `handleConnectionLost()`，当并发的多个采集任务同时失败时，先以互斥锁检查 `d.state == StateConnecting || d.state == StateError`，防止并发启动多个重连协程和输出重复重连警告。

### 4.4 优雅停机保证 (Graceful Teardown)
- 捕获 `os.Interrupt`, `syscall.SIGTERM` 信号。
- 退出顺序：**取消全局 Context → 停止拓扑发现 (Discovery) → 停止调度器 (Scheduler) → 停止南向驱动连接 → 停止传输批量器 (Batcher, flush 残留数据) → 停止北向传输发布 → 关闭内部数据总线 (DataBus) → 关闭规则 Provider → 停止标签文件 watcher → 等待所有工作协程退出**。每个驱动/传输的 Stop 均有超时保护，防止永久阻塞。耗时毫秒级，零资源泄露。

---

## 5. 如何扩展新驱动 / 新传输（开发者指南）

### 5.1 增加一个新驱动（以 EtherNet/IP 为例）
1. 在 `driver/ethernetip/` 下创建驱动文件：
   ```go
   package ethernetip

   import "github.com/CoreC-Dev/CoreC/core"

   type Driver struct { ... }

   func NewDriver(cfg core.DriverConfig) (core.Driver, error) {
       return &Driver{ ... }, nil
   }
   // 实现 core.Driver 接口全部方法: Init, Start, Stop, Restart, Read, Write, Subscribe...
   ```
2. 创建 `driver/ethernetip/register.go`：
   ```go
   package ethernetip

   import "github.com/CoreC-Dev/CoreC/core"

   func init() {
       core.RegisterDriver("ethernet-ip", NewDriver)
   }
   ```
3. 在 `driver/all/all.go` 中添加一行引入：
   ```go
   import (
       _ "github.com/CoreC-Dev/CoreC/driver/ethernetip"
   )
   ```
4. 编写 `driver/ethernetip/driver_test.go` 并执行测试。

### 5.2 增加一个新北向传输（以 Kafka 为例）
1. 在 `transport/kafka/` 下创建实现文件并实现 `core.Transport`。
2. 在 `transport/kafka/register.go` 中调用 `core.RegisterTransport("kafka", NewKafkaTransport)`。
3. 在 `transport/all/all.go` 中添加 `_ "github.com/CoreC-Dev/CoreC/transport/kafka"`。

---

## 6. 项目演进现状与未来规划 (Roadmap)

### 6.1 Phase 2 已完成内容
1. **控制面 REST API (`hub/route/`, `hub/hub.go`)**：
   - 基于 `go-chi/chi/v5` 路由器，严格隔离公共路由与认证路由组。
   - `crypto/hmac.Equal`（对 sha256 哈希做常量时间比较）防时序攻击的 Bearer Token / URL query token 认证；`api.secret` 为空时**拒绝启动**（fail-closed，避免裸奔暴露 `/write`、`/configs` 等写端点）。
   - CORS 中间件支持浏览器 Dashboard 跨域访问；可选 TLS（`api.tls-cert`/`api.tls-key`）与按客户端真实 RemoteAddr（不信任 `X-Forwarded-For`）的令牌桶限流。
   - 公共（免认证）端点：`/` (hello), `/version`, `/healthz/live` (K8s liveness，仅进程存活), `/healthz/ready` (K8s readiness，检查 engine 运行态 + 至少一条数据通路连通)。
   - 认证端点：`/configs` (GET/PUT/PATCH), `/drivers`, `/drivers/{name}`, `/drivers/{name}/tags`, `/transports`, `/transports/{name}`, `/tags`, `/write` (POST), `/write/failed` (GET), `/rules` (含命中统计), `/rules/disable` (PATCH), `/stats`, `/metrics` (Prometheus 文本格式 + Go 运行时/进程指标，无外部 client 库), `/debug/pprof/*` (pprof 性能剖析，可配置开关 `pprof-disabled` 与独立端口 `pprof-addr`), `/logs` (WS), `/traffic` (WS), `/tags/stream` (WS), `/memory` (WS)。
   - `/tags` 与 `/drivers/{name}/tags` 在响应中对超过 `stale-threshold` 的缓存测点标注 `is_stale`（仅 API 层设置，不进入发布载荷）。
2. **配置热重载流水线 (`hub/executor/executor.go`)**：
   - 采用 `Suspend` -> `Diff` -> `Apply` -> `Resume` 状态机。
   - `reflect.DeepEqual` 细粒度 diff 驱动、传输通道及规则，变更的驱动/传输自动重启。
3. **Observable 日志总线 (`common/observable/`, `log/`)**：
   - 泛型 Observable 事件总线，非阻塞扇出至 WebSocket 订阅端。
   - **`ObservableHandler` 包装 `slog.Handler`**：自动捕获全代码库所有 `slog.Info`/`slog.Error` 调用，无需改动现有代码即可实现日志流。
4. **吞吐量与性能统计 (`engine/statistic/`)**：
   - 原子计数器模式 (`PushRead`, `PushPublish`, `PushError`, `Snapshot`)，无后台 goroutine，调用方按需读取快照。
5. **规则命中统计与运行时禁用 (`rule/engine.go`)**：
   - `RuleWrapper` 的 `HitCount`/`MissCount`/`HitAt`/`MissAt` 原子计数。
   - `PATCH /rules/disable` 支持运行时启用/禁用规则，无需重载配置。
6. **规则表达式引擎 (`rule/expr.go`, `rule/engine.go`)**：
   - 基于 expr-lang/expr v1.17.8（MIT）的 DSL 翻译层（~176 行），将 CoreC DSL 转译为 expr-lang/expr 内置算子后编译求值。支持 `==`/`!=`/`=~`/`!~`/`contains`/`suffix`/`prefix`/`> < >= <=`/`in lo..hi`/`&&`/`||`/`!`/`( )` 表达式语法。
   - 可用字段：`driver`、`device`、`group`、`tag`、`type`、`quality`、`value`。
   - `ALL` 特殊匹配全部；`RULE-SET:`/`SUB-RULE:` 委托外部 provider 与子规则组。
7. **死区过滤 (Deadband Filter, `engine/scheduler.go`)**：
   - 调度器在读取数据后按 `TagConfig.DeadBand` 阈值过滤，变化量小于死区的数据点不上报。
   - 每个 task runner 维护 `lastValues` 映射记录上次上报值。
8. **传输批量聚合与重试 (`engine/batcher.go`)**：
   - `transportBatcher` 包装层实现 `batch-size`/`flush-interval`/`retry-count` 配置。
   - 数据点先进入内存缓冲，达到 batch-size 或 flush-interval 触发时一次性调用 `PublishBatch`。
   - 发送失败按指数退避重试。
9. **驱动运行期断线重连与断路器 (所有驱动)**：
   - 所有驱动在读取失败时检测连接错误，自动触发 `handleConnectionLost` → `ReconnectLoopWithBreaker`（指数退避，上限 `reconnect-max-interval`）。
   - 连续失败达到 `max-reconnect-failures`（默认 20）后触发断路器，将重连间隔提升至 5 分钟低频重试（重连不会停止，设备恢复后仍可自动接入）。
   - Modbus、S7、OPC UA 驱动共享同一重连与断路器机制。
10. **Modbus 离散输入功能码修复 (`driver/modbus/modbus_base.go`)**：
    - 离散输入读取改用 FC02 (`ReadDiscreteInput`) 而非 FC01 (`ReadCoil`)。
    - 离散输入和输入寄存器添加写保护。
11. **S7 类型安全修复 (`driver/s7/s7.go`)**：
    - `int64`/`uint64`/`bytes`/未知类型读取返回明确错误而非静默截断为 Uint16。
    - `encodeS7Value` 签名改为 `([]byte, error)`，不支持类型报错而非静默写零值。
12. **OPC UA 订阅模式完整实现 (`driver/opcua/client.go`)**：
    - `subscription` 模式已对接 `gopcua/opcua` 的 `Subscription` API：`connect()` 成功后自动创建订阅、为所有配置标签创建 MonitoredItem，通知 goroutine 将值变更写入 `subChannel`。断线时自动取消订阅，重连后重建。
13. **DataPoint Group 富化 (`engine/engine.go`)**：
    - 在 `onDriverData` 中从 `TagConfig.Group`（`AddDriver` 时缓存的 `tagGroups` 映射）补全 `DataPoint.Group` 字段，支撑 `group == 'reactor'` 规则匹配与 `{{.Group}}` 主题模板。
14. **拓扑自动发现 (`engine/discovery.go`, `core/engine.go` NodeConfig)**：
    - 配置 `node.id` 后，节点通过 MQTT 心跳（`corec/_discovery/{node-id}`，retained，5s 间隔）广播身份与端点信息。
    - `subscribe` 声明上游节点，发现模块自动创建 inbound transport（auto-subscribe）。
    - `autoFillNodeConfig` 自动填充省略的 `topic-template`（`topo/{node-id}/data/...`）、`command-topic`、`parser`、forward rule。
    - 原则：显式配置优先，省略才自动填。无 `node` 段时完全向后兼容。
    - 多 broker：每个 transport 在自己的 broker 上独立发现，桥接节点连多个 broker 自动跨网。
 15. **外部标签文件与热重载 (`engine/tagfile.go`, `config/config.go` loadTagsFiles)**：
     - `DriverConfig.TagsFile` 从外部 YAML 文件加载标签列表，适合数百以上采集点场景。
     - `DriverConfig.TagsInterval` 配置热重载间隔，watcher 基于 SHA-256 检测文件变更，变化时移除旧驱动并按新标签列表重建。
     - `tags` 与 `tags-file` 可同时使用，文件标签在前、内联标签追加在后。
     - 配置解析在 `config.Parse` 中统一处理，全量配置重载（`PUT /configs`）也会重新读取标签文件。
 16. **已定义但尚未接入的契约（实现状态诚实说明）**：
     - **`core.Logger` / `core.Metrics` 端口**：接口与 `NoopLogger`/`NoopMetrics` 默认实现已定义，但**尚未注入驱动**；驱动当前仍直接调用 `log/slog`，且无驱动级指标后端（Prometheus `/metrics` 端点取自 `engine.Stats()` 聚合计数，非逐驱动埋点）。接入需经 `Driver.Init`/构造器改造，列为后续重构。
     - **`common/trace` 分布式追踪**：W3C Trace Context 实现——HTTP 中间件解析入站 `traceparent` 头（`version-trace_id-parent_id-trace_flags` 格式），提取 trace ID 并注入 context；无 traceparent 时回退至 chi RequestID 或生成新 W3C trace ID。`InjectTraceparent()` 向出站 HTTP push 请求注入 `traceparent` 头实现跨服务传播。`engine.readFromDriver` 调用 `trace.Start()` 创建管线 span（含属性与计时，`End()` 以 `slog.Debug` 记录）。**无 OpenTelemetry/OTLP 导出后端、无采样、无父子 span 树**——如需完整可观测追踪需后续接入 OTel。
     - **MQTT 反向指令 "replay protection"**：HMAC-SHA256 签名 + 时间戳偏移（anti-stale）校验（`command-max-skew`，默认 5m）+ **有界重放缓存**（10,000 条目，TTL = 2× skew 窗口，自动过期）记录已认证指令的 SHA256 哈希，实现真正防重放——同一指令在窗口内不可二次接受。`command-strict-replay=false`（默认）时无时间戳的认证指令仅告警放行；`=true` 时拒绝无时间戳指令。
     - **重连 jitter 覆盖范围**：`ReconnectLoopWithBreaker` 的 ±20% 随机抖动覆盖使用该工具的驱动（Modbus/S7/OPC UA）；MQTT（paho）在 Init 时对 `connect-retry-interval` 施加 ±20% 抖动（实例级，随机一次）以防止多传输同时重连的 thundering-herd。
     - **`LatestCache` 并发模型**：按驱动名 FNV 哈希分 64 分片，每分片 `sync.RWMutex` + `atomic.Pointer` 快照（非 seqlock）；读快照在数据未变时免 map 拷贝。
 17. **命令跨实例透传 (`core/transport.go`, `transport/mqtt/publisher.go`, `engine/engine.go`)**：
     - 新增 `core.CommandForwarder` 可选接口（`ForwardCommand(ctx, cmd) error`），不修改 `core.Transport` 契约。
     - MQTT transport 实现 `CommandForwarder`：通过 `command-forward-topic` 发布转发命令，配置 `command-forward-secret` 时使用 HMAC-SHA256 签名并附加时间戳。
     - engine 的 `executeWriteWithRetry` 在本地无匹配驱动时，遍历所有实现 `CommandForwarder` 的 transport 转发命令；所有 forwarder 失败则进入死信队列。
     - 支撑链式核心场景七（双向级联）：中继节点（`drivers: []`）收到云端命令后自动转发到边缘节点，实现多跳命令透传（cloud → gateway → edge）。
 18. **环境变量替换 (`config/config.go`)**：
     - 配置文件及 tags-file 中的 `${ENV_VAR}` 占位符在 YAML 解析前被替换为环境变量值。
     - 字节级替换（pre-YAML-parse），保留类型推断：`port: ${PORT}`（PORT=502）解析为 int 而非 string。
     - 未设置的变量保持原样（`${...}` 字面量），使配置错误在启动时可见。
     - 变量名匹配 `[A-Za-z_][A-Za-z0-9_]*`；支持引号内外替换。
     - 适用于敏感字段（API secret、MQTT password、forward secret 等），实现 12-Factor 配置分离。

### 6.2 Phase 3 规划 (后续演进方向)
1. **DataPoint Device 富化**：
   - 在 `onDriverData` 中从 `DriverConfig` 补全 `DataPoint.Device` 字段（`Group` 已在 Phase 2 实现）。
2. **扩展更多协议**：
   - **南向**：EtherNet/IP (CIP), IEC 60870-5-104 (电力), BACnet (楼宇), Omron FINS, Mitsubishi MC Protocol。
   - **北向**：Kafka, Webhook Stream, InfluxDB / TDengine 时序库直连。

> 注：断线持久化缓存（Offline Buffer）已重新启用并实现——`global.buffer` 配置段支持 `enabled`/`path`/`max-size` 字段，`engine/offlinebuffer.go` 提供基于文件的离线缓冲，传输中断时数据写入磁盘待恢复后自动补传。

---

## 7. 常用命令清单

```powershell
# 切换到项目根目录
cd CoreC

# 运行所有单元测试与集成测试
go test -v ./...

# 编译生成可执行文件
go build -o corec.exe ./cmd/corec

# 运行核心
.\corec.exe -c config.example.yaml

# 覆写日志级别运行
.\corec.exe -c config.example.yaml -log-level debug
```
