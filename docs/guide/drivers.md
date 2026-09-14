---
title: 驱动
description: 南向驱动概念 —— Driver 接口、Read/Write/Subscribe 操作、能力声明与点位配置
---

# 驱动（Drivers）

驱动是 CoreC 的**南向抽象面**，负责与工业设备通信。所有驱动实现统一的 `core.Driver` 接口，通过工厂模式自动注册，由调度器按周期触发读取。本章详解接口契约、能力声明与三类内置驱动协议（Modbus、Siemens S7、OPC UA）的配置。

## Driver 接口

`core.Driver` 是整个南向面的唯一抽象：

```go
type Driver interface {
    // 生命周期
    Init(ctx context.Context, config DriverConfig) error
    Start(ctx context.Context) error
    Stop() error
    Restart(ctx context.Context, config DriverConfig) error

    // 数据操作
    Read(ctx context.Context, tags []string) ([]TagValue, error)
    Write(ctx context.Context, commands []WriteCommand) ([]WriteResult, error)
    Subscribe(ctx context.Context, tags []string) (<-chan DataPoint, error)

    // 元信息
    Name() string
    Type() string
    Status() DriverStatus
    Capabilities() DriverCapabilities
}
```

### 生命周期方法

| 方法 | 调用时机 | 职责 |
|:---|:---|:---|
| `Init` | 创建实例后 | 解析配置、建立内部映射、**不发起网络连接** |
| `Start` | 引擎启动时 | 发起连接；初始连接失败不返回错误，转入后台重连 |
| `Stop` | 引擎停止 / 驱动移除时 | 关闭连接、释放资源 |
| `Restart` | 配置热更新时 | 等价于 `Stop → Init → Start` |

::: tip 连接失败的容错策略
`Start` 阶段如果设备不可达，CoreC **不会**返回错误导致启动失败，而是将状态置为 `connecting` 并启动后台 `reconnectLoop` 按指数退避重试（上限 30s）。这使得 CoreC 可以在设备离线时先启动，设备恢复后自动接入。连续重连失败达到 `max-reconnect-failures`（默认 20）次后触发**断路器**，停止重连进入 5 分钟冷却期，避免设备长期不可达时无效重连消耗资源。
:::

### 数据操作方法

#### Read —— 批量读取

```go
Read(ctx context.Context, tags []string) ([]TagValue, error)
```

接收一组测点名，返回对应的 `TagValue` 列表。关键行为：

- **批量语义**：同周期的点位合并为一次调用，减少协议交互
- **部分失败容忍**：单个点位读取失败时，该点位返回 `Quality: bad` + `Error`，**不中断整批**
- **连接丢失感知**：检测到连接类错误时，自动触发后台重连

#### Write —— 批量写入

```go
Write(ctx context.Context, commands []WriteCommand) ([]WriteResult, error)
```

接收一组 `WriteCommand`，将值写入设备寄存器/线圈。每个命令独立返回 `WriteResult{Success, Error}`。写入由两种路径触发：

- API `POST /write` —— 即时控制指令
- 传输 `OnCommand()` 通道 —— 北向反向下发（如 MQTT Command Topic）

#### Subscribe —— 事件订阅

```go
Subscribe(ctx context.Context, tags []string) (<-chan DataPoint, error)
```

对于支持事件驱动的协议（如 OPC UA Subscription 模式），返回一个数据点 channel，由驱动主动推送。不支持的驱动返回 `ErrSubscribeNotSupported`。

## 能力声明（Capabilities）

每个驱动通过 `Capabilities()` 声明自身支持的操作，引擎与 API 层据此做能力协商：

```go
type DriverCapabilities struct {
    CanRead      bool  // 是否支持读取
    CanWrite     bool  // 是否支持写入
    CanSubscribe bool  // 是否支持事件订阅
    BatchRead    bool  // 是否支持批量读取
    MaxBatchSize int   // 单次批量读取上限
}
```

### 内置驱动能力矩阵

| 驱动 | `CanRead` | `CanWrite` | `CanSubscribe` | `BatchRead` | `MaxBatchSize` |
|:---|:---:|:---:|:---:|:---:|:---:|
| `modbus-tcp` | ✅ | ✅ | ❌ | ✅ | 125 |
| `s7` | ✅ | ✅ | ❌ | ✅ | 220 |
| `opcua` | ✅ | ✅ | ✅ | ✅ | 1000 |

> 上表中 `modbus-tcp` 一行代表全部六种 Modbus 变体（`modbus-tcp`/`modbus-rtu`/`modbus-rtuovertcp`/`modbus-udp`/`modbus-rtuoverudp`/`modbus-tls`）——它们共享同一 `modbusBase` 实现，能力完全一致。

::: info MaxBatchSize 的来源
- Modbus 125：单次 Modbus TCP 请求的寄存器上限（PDU 限制）
- S7 220：标准 S7 请求的 PDU 字节上限
- OPC UA 1000：客户端单次读取的 NodeID 推荐上限
:::

## 点位配置（TagConfig）

每个驱动下定义一组点位（tag），描述"读什么、怎么读、多久读一次"：

```yaml
tags:
  - name: temperature        # 测点名（全局唯一标识，用于规则匹配与 API 查询）
    address: "40001"         # 协议地址（格式因驱动而异）
    type: float32            # 数据类型
    group: sensors           # 分组（用于规则匹配与主题渲染）
    interval: 1s             # 采集周期
    scale: 1.0               # 缩放系数（可选，value * scale + offset）
    offset: 0.0              # 偏移量（可选）
    deadband: 0.2            # 死区（可选，变化小于此值时不更新）
    read-timeout: 500ms      # 读取超时（可选，默认同 interval）
```

| 字段 | 必填 | 说明 |
|:---|:---:|:---|
| `name` | ✅ | 测点名，驱动内唯一 |
| `address` | ✅ | 协议地址，格式见各驱动说明 |
| `type` | ✅ | 数据类型，见下表 |
| `group` | ❌ | 分组名，用于规则匹配与传输主题模板（可选） |
| `interval` | ✅ | 采集周期，Go duration 格式（`1s`、`500ms`、`200ms`） |
| `scale` | ❌ | 缩放系数，读取后执行 `value * scale + offset`（可选） |
| `offset` | ❌ | 偏移量（可选） |
| `deadband` | ❌ | 死区，抑制无意义的小幅波动上报（可选） |
| `read-timeout` | ❌ | 单次读取超时，独立于 `interval`（可选，默认同 `interval`） |

### 外部标签文件

当一个驱动的采集点较多时，可将标签列表拆分到独立文件，主配置更简洁：

```yaml
drivers:
  - name: plc-modbus
    type: modbus-tcp
    settings: { host: 192.168.1.100, port: 502 }
    tags-file: ./tags/plc-modbus-tags.yaml   # 外部标签文件
    tags-interval: 30s                        # 热重载间隔（可选）
```

标签文件格式为 YAML 列表，与内联 `tags` 结构相同。`tags` 与 `tags-file` 可同时使用，文件标签在前、内联标签在后。配置 `tags-interval` 后引擎定期检查文件变化并自动热重载，详见[驱动配置参考](../config/drivers.md#外部标签文件-tags-file)。

### 支持的数据类型

| 类型 | 说明 | 典型用途 |
|:---|:---|:---|
| `bool` | 布尔 | 开关、状态、线圈 |
| `int8` / `int16` / `int32` / `int64` | 有符号整数 | 计数器、字 |
| `uint8` / `uint16` / `uint32` / `uint64` | 无符号整数 | 寄存器原始值 |
| `float32` / `float64` | 浮点数 | 温度、压力、流量 |
| `string` | 字符串 | OPC UA 字符串变量 |
| `bytes` | 原始字节 | 原始寄存器数据 |

## 内置驱动详解

### Modbus TCP（`modbus-tcp`）

Modbus 是工业领域最通用的协议，CoreC 内置六种 Modbus 变体（见下文「Modbus 变体」），本节以 `modbus-tcp` 为例。

#### 连接配置

```yaml
- name: plc-modbus
  type: modbus-tcp
  settings:
    host: 192.168.1.100    # 设备 IP（必填）
    port: 502              # Modbus TCP 端口（默认 502）
    slave-id: 1            # 从站 ID（默认 1）
    timeout: 3s            # 读写超时（默认 3s）
    retry: 3               # 重试次数（默认 3）
```

#### 地址格式

Modbus 地址通过前缀数字区分四个数据区：

| 地址范围 | 数据区 | Modbus 功能码 | 可读 | 可写 |
|:---|:---|:---|:---:|:---:|
| `00001` ~ `09999` | 线圈（Coil） | FC 01 / 05 / 15 | ✅ | ✅ |
| `10001` ~ `19999` | 离散输入（Discrete Input） | FC 02 | ✅ | ❌ |
| `30001` ~ `39999` | 输入寄存器（Input Register） | FC 04 | ✅ | ❌ |
| `40001` ~ `49999` | 保持寄存器（Holding Register） | FC 03 / 06 / 16 | ✅ | ✅ |

::: tip 地址是 1-based
`40001` 对应保持寄存器 0（内部自动减 1 转为 0-based）。也支持直接写 0-based 裸地址（如 `"0"`），此时默认按保持寄存器处理。
:::

#### 配置示例

```yaml
tags:
  - name: temperature
    address: "40001"      # 保持寄存器 0，float32
    type: float32
    group: sensors
    interval: 1s
    deadband: 0.2
  - name: pressure
    address: "40003"      # 保持寄存器 2，float32
    type: float32
    group: sensors
    interval: 1s
  - name: pump_status
    address: "00001"      # 线圈 0，bool
    type: bool
    group: actuators
    interval: 2s
```

#### Modbus 变体

除 `modbus-tcp` 外，CoreC 还注册了五种 Modbus 变体，地址格式与能力矩阵完全相同，仅连接设置不同：

| 类型名 | 传输方式 | 区别于 `modbus-tcp` 的设置 |
|:---|:---|:---|
| `modbus-rtu` | 串口（RS-485/RS-232） | `serial-device`（必填，如 `/dev/ttyUSB0` 或 `COM3`）、`baud-rate`（默认 9600）、`data-bits`（默认 8）、`parity`（`none`/`even`/`odd`，默认 `none`）、`stop-bits`（默认 0 = 库自动选择） |
| `modbus-rtuovertcp` | RTU 帧封装在 TCP（串口转网关） | `host` + `port`（默认 502），同 `modbus-tcp` |
| `modbus-udp` | Modbus TCP over UDP | `host` + `port`（默认 502），同 `modbus-tcp` |
| `modbus-rtuoverudp` | RTU 帧封装在 UDP | `host` + `port`（默认 502），同 `modbus-tcp` |
| `modbus-tls` | Modbus TCP over TLS（mTLS） | `host` + `port`（默认 502），并要求 `cert-file`、`key-file`、`ca-file`（均为必填的 PEM 路径） |

所有变体共享 `modbusBase` 实现，通用设置包括 `slave-id`（默认 1）、`timeout`（默认 3s）、`retry`（默认 3）、`reconnect-interval`（默认 2s）、`reconnect-max-interval`（默认 30s）、`max-reconnect-failures`（默认 20，断路器阈值）。

`modbus-rtu` 示例：

```yaml
- name: sensor-rtu
  type: modbus-rtu
  settings:
    serial-device: /dev/ttyUSB0   # 串口设备（必填）
    baud-rate: 9600               # 默认 9600
    data-bits: 8                  # 默认 8
    parity: none                  # none | even | odd（默认 none）
    stop-bits: 1                  # 1 | 2，0 = 自动（默认 0）
    slave-id: 1
    timeout: 3s
```

`modbus-tls` 示例：

```yaml
- name: plc-mtls
  type: modbus-tls
  settings:
    host: 192.168.1.100
    port: 502
    cert-file: /etc/corec/client.pem   # 客户端证书（必填）
    key-file: /etc/corec/client.key    # 客户端私钥（必填）
    ca-file: /etc/corec/ca.pem         # CA 证书（必填）
    slave-id: 1
```

### Siemens S7（`s7`）

支持 S7-200/300/400/1200/1500 全系列，基于 ISO-on-TCP（端口 102）。

#### 连接配置

```yaml
- name: siemens-s7-300
  type: s7
  settings:
    host: 192.168.1.200   # PLC IP（必填）
    port: 102             # ISO-on-TCP 端口（默认 102）
    rack: 0               # 机架号（默认 0）
    slot: 2               # 槽号：S7-300 用 2，S7-1200/1500 用 1
    timeout: 3s           # 超时（默认 5s）
```

::: warning slot 取值
不同 S7 系列的 CPU 槽号不同：
- **S7-300**：`slot: 2`（默认）
- **S7-1200 / S7-1500**：`slot: 1`
配置错误会导致连接失败。
:::

#### 地址格式

S7 地址支持四种数据区，格式丰富。每个区都支持位（X）、字节（B）、字（W）、双字（D）四种宽度：

| 格式 | 数据区 | 示例 | 说明 |
|:---|:---|:---|:---|
| `DB{n}.DB{X/B/W/D}{byte}` | 数据块 | `DB1.DBD0` | DB1 双字（float32）起始字节 0 |
| | | `DB1.DBW4` | DB1 字（uint16）起始字节 4 |
| | | `DB1.DBB2` | DB1 字节（uint8）起始字节 2 |
| | | `DB1.DBX0.0` | DB1 位，字节 0 位 0 |
| `M{B/W/D}{byte}` 或 `M{byte}.{bit}` | Merker（M） | `M10.0` | 标志位（位寻址） |
| | | `MB10` | M 字节（uint8）起始字节 10 |
| | | `MW20` | M 字（uint16）起始字节 20 |
| | | `MD30` | M 双字（float32）起始字节 30 |
| `[IE]{B/W/D}{byte}` 或 `[IE]{byte}.{bit}` | 输入（I/E） | `I0.0` | 输入位（位寻址） |
| | | `IB1` / `EB1` | 输入字节（uint8） |
| | | `IW2` / `EW2` | 输入字（uint16） |
| | | `ID4` / `ED4` | 输入双字（uint32） |
| `[QA]{B/W/D}{byte}` 或 `[QA]{byte}.{bit}` | 输出（Q/A） | `Q0.0` | 输出位（位寻址） |
| | | `QB1` / `AB1` | 输出字节（uint8） |
| | | `QW2` / `AW2` | 输出字（uint16） |
| | | `QD4` / `AD4` | 输出双字（uint32） |

#### 配置示例

```yaml
tags:
  - name: reactor_temp
    address: "DB1.DBD0"   # DB1 双字 0，float32
    type: float32
    group: reactor
    interval: 500ms
  - name: motor_speed
    address: "DB1.DBW4"   # DB1 字 4，uint16
    type: uint16
    group: motors
    interval: 1s
  - name: emergency_stop
    address: "I0.0"       # 输入位 0.0，bool
    type: bool
    group: safety
    interval: 200ms
```

### OPC UA（`opcua`）

支持标准 NodeID，提供轮询与事件订阅两种模式。

#### 连接配置

```yaml
- name: opc-server
  type: opcua
  settings:
    endpoint: "opc.tcp://192.168.1.50:4840"   # 端点（必填）
    mode: polling                              # polling 或 subscription
    timeout: 5s                                # 超时（默认 5s）
    subscription-interval: 500ms               # 订阅模式采样间隔
    security-policy: "None"                    # 安全策略（可选）
    security-mode: "None"                      # 安全模式（可选）
    username: ""                               # 用户名认证（可选）
    password: ""                               # 密码认证（可选）
    cert-file: ""                              # 客户端证书（可选）
    key-file: ""                               # 客户端私钥（可选）
```

#### 两种采集模式

| 模式 | 说明 | 适用场景 |
|:---|:---|:---|
| `polling` | 调度器按 `interval` 周期主动读取 | 通用场景，行为可预测 |
| `subscription` | OPC UA 服务端主动推送变更 | 高实时性、低流量场景 |

::: tip subscription 模式
当 `mode: subscription` 时，驱动调用 `Subscribe` 建立服务端订阅，由 OPC UA Server 在数据变化时主动推送，CoreC 无需周期轮询。此时点位的 `interval` 字段不再用于调度触发，而由 `subscription-interval` 控制服务端采样间隔。
:::

#### 地址格式（NodeID）

OPC UA 使用 NodeID 寻址，支持两种形式：

| 格式 | 示例 | 说明 |
|:---|:---|:---|
| 命名空间 + 字符串 | `ns=2;s=Conveyor.Speed` | 命名空间 2，字符串标识 |
| 命名空间 + 数字 | `ns=1;i=1001` | 命名空间 1，数字标识 |

#### 配置示例

```yaml
tags:
  - name: conveyor_speed
    address: "ns=2;s=Conveyor.Speed"
    type: float64
    group: conveyor
    interval: 1s
  - name: batch_count
    address: "ns=1;i=1001"
    type: uint32
    group: production
    interval: 2s
```

## 连接状态机

所有驱动遵循统一的状态机：

```
                 Init
                  │
                  ▼
          ┌───────────────┐  connect OK   ┌────────────┐
          │ disconnected  │ ────────────▶ │ connected  │
          └───────────────┘               └─────┬──────┘
                  │                             │
            Start │                             │ 连接断开
                  ▼                             ▼
          ┌───────────────┐  reconnect    ┌────────────┐
          │  connecting   │ ◀─────────── │   error    │
          └───────────────┘              └────────────┘
                  │
          connect │ OK
                  ▼
            ┌────────────┐
            │ connected  │
            └────────────┘
```

状态可通过 `GET /drivers/{name}` API 的 `state` 字段查询，取值为 `disconnected` / `connecting` / `connected` / `error`。

## 驱动状态查询

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  http://localhost:9090/drivers/plc-modbus | jq
```

```json
{
  "name": "plc-modbus",
  "type": "modbus-tcp",
  "state": "connected",
  "last_read": "2024-01-15T10:30:15Z",
  "last_error": "",
  "tag_count": 3,
  "read_count": 15234,
  "error_count": 2
}
```

| 字段 | 说明 |
|:---|:---|
| `state` | 连接状态 |
| `last_read` | 最后一次成功读取时刻 |
| `last_error` | 最后一条错误信息 |
| `tag_count` | 配置的点位数 |
| `read_count` | 累计读取次数 |
| `error_count` | 累计错误次数 |

## 编写自定义驱动

实现一个新协议只需三步：

```go
package myprotocol

import "github.com/CoreC-Dev/CoreC/core"

// 1. 实现 core.Driver 接口
type MyDriver struct { /* ... */ }

func NewMyDriver(config core.DriverConfig) (core.Driver, error) {
    return &MyDriver{}, nil
}

// 实现 Init/Start/Stop/Restart/Read/Write/Subscribe/Name/Type/Status/Capabilities ...

// 2. 在 init() 中注册工厂
func init() {
    core.RegisterDriver("my-protocol", NewMyDriver)
}
```

```go
// 3. 在 main.go 中导入你的驱动包
import _ "github.com/yourorg/corec-driver-myprotocol"
```

::: tip 工厂注册模式
CoreC 使用全局 `Registry`（`core/registry.go`）管理驱动与传输工厂。`RegisterDriver` 在 `init()` 中调用，`CreateDriver` 在引擎启动时按 `type` 字段查找工厂并实例化。这与全局 Registry 注册机制一致。
:::

## 下一步

- [传输](./transports.md) —— 数据采集后的北向目的地
- [规则引擎](./rules.md) —— 控制哪些数据发往哪个传输
- [数据流](./data-flow.md) —— 回顾驱动在整个管道中的位置
