---
title: 传输
description: 北向传输概念 —— Transport 接口、Publish/PublishBatch 发布、OnCommand 反向通道与批量聚合
---

# 传输（Transports）

传输是 CoreC 的**北向抽象面**，负责将采集到的数据点发往外部系统（云平台、MES、数据湖等）。所有传输实现统一的 `core.Transport` 接口，并可通过反向通道接收控制指令。本章详解接口契约、批量策略与两种内置传输的配置。

## Transport 接口

`core.Transport` 是整个北向面的唯一抽象：

```go
type Transport interface {
    // 生命周期
    Init(ctx context.Context, config TransportConfig) error
    Start(ctx context.Context) error
    Stop() error

    // 数据发布
    Publish(ctx context.Context, point DataPoint) error
    PublishBatch(ctx context.Context, points []DataPoint) error

    // 反向通道：接收写指令
    OnCommand() <-chan WriteCommand

    // 链式核心入站：接收上游 DataPoint（如 MQTT data-topic / HTTP webhook）
    OnData() <-chan DataPoint

    // 元信息
    Name() string
    Type() string
    Status() TransportStatus
}
```

### 生命周期方法

| 方法 | 调用时机 | 职责 |
|:---|:---|:---|
| `Init` | 创建实例后 | 解析配置、建立客户端对象、**不发起连接** |
| `Start` | 引擎启动时 | 发起连接 / 准备发送通道 |
| `Stop` | 引擎停止时 | 断开连接、清理缓冲池 |

### 数据发布方法

#### Publish —— 单点发布

```go
Publish(ctx context.Context, point DataPoint) error
```

将单个 `DataPoint` 序列化并发往外部系统。引擎的 `publishToTargets` 在规则匹配后逐个调用此方法。

#### PublishBatch —— 批量发布

```go
PublishBatch(ctx context.Context, points []DataPoint) error
```

将多个 `DataPoint` 合并为一次请求。HTTP 传输原生支持批量（一次 POST 发送 JSON 数组），MQTT 传输的 `PublishBatch` 内部逐条调用 `Publish`（MQTT 协议本身是单消息发布模型）。

### 反向通道 OnCommand

```go
OnCommand() <-chan WriteCommand
```

返回一个接收 `WriteCommand` 的只读 channel。传输在收到外部控制指令时（如 MQTT Command Topic 的消息），将指令推入此 channel，引擎的 `commandLoop` 消费后调用驱动 `Write` 写入设备。

```go
type WriteCommand struct {
    Driver string   // 目标驱动名
    Device string   // 设备标识
    Tag    string   // 目标测点名
    Value  any      // 待写入值
    Type   DataType // 值类型
}
```

::: info 双向通道的意义
`OnCommand` 让北向传输不仅是数据出口，也可以是控制入口。这使得云端可以通过同一个 MQTT 连接既接收数据又下发控制指令，形成边缘闭环。**MQTT 传输**通过 `command-topic` 实现反向指令；**HTTP Push 传输**是单向推送，`OnCommand()` 暴露的通道当前无消息来源、不会投递任何指令（如需 HTTP 反向下发，需自行扩展 webhook 监听）。
:::

## 传输配置（TransportConfig）

```yaml
transports:
  - name: cloud-mqtt          # 传输名（规则 target 引用此名）
    type: mqtt                # 类型
    settings: { ... }         # 协议特定设置
    batch-size: 50            # 批量大小（可选）
    flush-interval: 1s        # 刷新间隔（可选）
    retry-count: 3            # 重试次数（可选）
    buffer-size: 1000         # 内部缓冲（可选）
    fallback: backup-http     # 备用传输（可选）
```

| 字段 | 必填 | 说明 |
|:---|:---:|:---|
| `name` | ✅ | 传输名，全局唯一，规则 `target` / `targets` 引用 |
| `type` | ✅ | 类型：`mqtt` 或 `http` |
| `settings` | ✅ | 协议特定配置，见各传输说明 |
| `batch-size` | ❌ | 批量聚合的消息数 |
| `flush-interval` | ❌ | 批量刷新周期，到时无论是否凑满都发送 |
| `retry-count` | ❌ | 发布失败重试次数 |
| `buffer-size` | ❌ | 内部发送缓冲容量 |
| `fallback` | ❌ | 备用传输名，本传输发布失败时自动切换 |

## 内置传输详解

### MQTT（`mqtt`）

基于 Eclipse Paho MQTT 客户端（异步模式），是工业物联网上云的主流选择。

#### 配置

```yaml
- name: cloud-mqtt
  type: mqtt
  settings:
    broker: tcp://broker.emqx.io:1883   # Broker 地址（必填，含 scheme）
    client-id: factory-edge-01           # 客户端 ID（不填则自动生成）
    username: ""                         # 用户名（可选）
    password: ""                         # 密码（可选）
    qos: 1                               # QoS 级别 0/1/2（默认 1）
    retained: false                      # 是否保留消息（默认 false）
    topic-template: "factory/{{.Driver}}/{{.Group}}/{{.Tag}}"  # 主题模板
    command-topic: "factory/commands/#"  # 反向指令订阅主题（可选）
    keep-alive: 60s                      # 心跳间隔（默认 60s）
    connect-timeout: 10s                 # 连接超时（默认 10s）
    auto-reconnect: true                 # 自动重连（默认 true）
    clean-session: true                  # 清洁会话（默认 true）
    connect-retry: true                  # 连接重试（默认 true）
  batch-size: 50
  flush-interval: 1s
```

#### 主题模板

`topic-template` 使用 Go `text/template` 语法，可用的变量来自 `DataPoint`。未配置时默认为 <code v-pre>corec/{{.Driver}}/{{.Tag}}</code>：

| 变量 | 来源 | 示例值 |
|:---|:---|:---|
| `&lbrace;&lbrace;.Driver&rbrace;&rbrace;` | `DataPoint.Driver` | `plc-modbus` |
| `&lbrace;&lbrace;.Device&rbrace;&rbrace;` | `DataPoint.Device` | `` |
| `&lbrace;&lbrace;.Group&rbrace;&rbrace;` | `DataPoint.Group` | `sensors` |
| `&lbrace;&lbrace;.Tag&rbrace;&rbrace;` | `DataPoint.Tag` | `temperature` |

渲染示例：

```
模板: "factory/{{.Driver}}/{{.Group}}/{{.Tag}}"
DataPoint{Driver:"plc-modbus", Group:"sensors", Tag:"temperature"}
  → "factory/plc-modbus/sensors/temperature"
```

::: tip 空段清理
如果某字段为空（如 `Device` 未设置），模板渲染会产生 `//`，CoreC 会自动清理为单斜杠，避免主题格式异常。
:::

#### 反向指令（Command Topic）

配置 `command-topic` 后，传输会在连接成功时订阅该主题。收到的消息 JSON 反序列化为 `WriteCommand` 并推入 `OnCommand()` 通道：

```bash
# 云端下发：将 plc-modbus 的 pump_status 置为 true
mosquitto_pub -h broker.emqx.io -t "factory/commands/pump" \
  -m '{"driver":"plc-modbus","tag":"pump_status","value":true,"type":"bool"}'
```

CoreC 日志：

```text
DEBUG mqtt command received topic=factory/commands/pump payload_size=72
INFO command write success driver=plc-modbus tag=pump_status
```

#### 连接容错

MQTT 传输充分利用 Paho 客户端的内置容错能力：

- `auto-reconnect: true` —— 连接断开后自动重连
- `connect-retry: true` —— 初始连接失败也持续重试
- 连接断开时状态置为 `connecting`，发布请求返回错误并计入 `failed`
- 重连成功后自动重新订阅 `command-topic`

### HTTP Push（`http`）

将数据点以 JSON POST 推送到 RESTful 端点，适合对接 MES、数据湖 API 或自建接收服务。

::: warning 单向推送
HTTP Push 是**单向推送传输**，不支持反向控制指令。`OnCommand()` 通道存在但永不投递消息。若需云端反向下发控制指令，请使用 **MQTT Command Topic**。
:::

#### 配置

```yaml
- name: mes-http-push
  type: http
  settings:
    url: "http://localhost:8080/api/v1/telemetry"   # 目标 URL（必填）
    method: POST                                     # HTTP 方法（默认 POST）
    headers:                                         # 自定义头（可选）
      Authorization: "Bearer mes-secret-key"
      X-Tenant: "factory-01"
    timeout: 3s                                      # 请求超时（默认 5s）
  batch-size: 100
  flush-interval: 5s
```

#### 连接池

HTTP 传输使用原生 `net/http` 客户端，内置连接池复用，针对高频推送优化：

```go
t.client = &http.Client{
    Timeout: t.timeout,
    Transport: &http.Transport{
        MaxIdleConns:        100,   // 全局最大空闲连接
        MaxIdleConnsPerHost: 20,    // 每主机最大空闲连接
        IdleConnTimeout:     90 * time.Second,
    },
}
```

::: tip 连接池的意义
没有连接池时，每次 POST 都要建立 TCP 连接 + TLS 握手，延迟显著。连接池复用使后续请求直接走已建立的连接，吞吐量可提升数倍。`MaxIdleConnsPerHost: 20` 意味着对同一目标服务最多保持 20 条空闲连接。
:::

#### 批量发布

HTTP 传输的 `PublishBatch` 将多个 `DataPoint` 序列化为 JSON 数组，一次 POST 发送：

```json
// 单次 POST body（batch-size=3）
[
  {"driver":"plc","tag":"temp","value":42.5,"type":"float32","quality":"good","timestamp":"..."},
  {"driver":"plc","tag":"press","value":1.2,"type":"float32","quality":"good","timestamp":"..."},
  {"driver":"plc","tag":"flow","value":88.0,"type":"float32","quality":"good","timestamp":"..."}
]
```

目标服务端只需返回 2xx 状态码即视为成功，非 2xx 计入 `failed`。

#### 请求头

`Content-Type: application/json` 自动设置。`headers` 段的自定义头会逐个追加，常用于携带认证 Token 或租户标识。

## 批量与刷新策略

`batch-size` 与 `flush-interval` 共同控制发布节奏：

| 策略 | 触发条件 | 适用场景 |
|:---|:---|:---|
| 凑满即发 | 累积消息数达到 `batch-size` | 高吞吐，追求每请求最大负载 |
| 到时即发 | 距上次发送超过 `flush-interval` | 低延迟，保证数据及时上报 |
| 两者取早 | 任意条件先满足 | 推荐，兼顾吞吐与延迟 |

::: info 批量聚合实现
引擎在 `publishToTargets` 中通过 `transportBatcher` 包装层实现批量聚合：数据点先进入内存缓冲，当缓冲达到 `batch-size` 或 `flush-interval` 定时器触发时，一次性调用 `PublishBatch` 发送。发送失败时按 `retry-count` 进行指数退避重试。重试耗尽后，若启用了全局 `buffer`，数据批次写入本地磁盘缓冲待后续回放；否则数据丢弃。未配置 `batch-size`/`flush-interval`/`retry-count` 的传输仍逐点调用 `Publish`。MQTT 传输逐条发布符合 MQTT 单消息模型。
:::

## 传输状态查询

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  http://localhost:9090/transports/cloud-mqtt | jq
```

```json
{
  "name": "cloud-mqtt",
  "type": "mqtt",
  "state": "connected",
  "published": 15234,
  "failed": 3,
  "received": 0,
  "last_publish": "2024-01-15T10:30:15Z",
  "queue_size": 0
}
```

| 字段 | 说明 |
|:---|:---|
| `state` | 连接状态 |
| `published` | 累计成功发布数 |
| `failed` | 累计失败数 |
| `received` | 经 `OnData()` 入站接收的数据点数（链式核心 inbound；纯发布节点为 0） |
| `last_publish` | 最后一次成功发布时刻 |
| `queue_size` | 待发送队列长度（MQTT 为 command channel 长度） |

## 内置传输对比

| 特性 | MQTT | HTTP |
|:---|:---|:---|
| 客户端模型 | Paho 异步 | net/http 同步连接池 |
| 发布方式 | 逐条发布（QoS 保证） | 批量 JSON 数组 POST |
| 反向指令 | ✅ Command Topic 订阅 | ❌ 单向推送（`OnCommand()` 通道存在但无消息来源） |
| 重连 | Paho 内置自动重连 | 无状态，每次请求独立 |
| 主题/路由 | 动态主题模板 | 固定 URL |
| 典型用途 | IoT 上云、双向控制 | MES 推送、数据湖入库 |

::: tip 如何选择
- 需要双向通信、低延迟、弱网容忍 → **MQTT**
- 对接已有 RESTful API、批量入库、无需反向控制 → **HTTP Push**
- 两者可以同时配置，通过 `mirror` 规则将同一份数据同时推送到两端
:::

## 编写自定义传输

与驱动类似，实现 `core.Transport` 接口并注册工厂：

```go
package mytransport

import "github.com/CoreC-Dev/CoreC/core"

// 1. 实现 core.Transport 接口
type MyTransport struct { /* ... */ }

func NewMyTransport(config core.TransportConfig) (core.Transport, error) {
    return &MyTransport{}, nil
}

// 实现 Init/Start/Stop/Publish/PublishBatch/OnCommand/OnData/Name/Type/Status ...

// 2. 注册工厂
func init() {
    core.RegisterTransport("my-transport", NewMyTransport)
}
```

```go
// 3. 在 main.go 中导入
import _ "github.com/yourorg/corec-transport-mytransport"
```

## 下一步

- [规则引擎](./rules.md) —— 决定数据发往哪个传输
- [数据流](./data-flow.md) —— 传输在管道末端的角色
- [驱动](./drivers.md) —— 管道起点的南向抽象
