---
title: 驱动配置
description: CoreC 南向驱动配置参考，涵盖 Modbus 全系列、Siemens S7、OPC UA 协议及标签定义。
---

# 驱动配置

`drivers` 段定义南向（southbound）采集驱动。每个驱动实例连接一台工业设备，按标签列表周期性采集数据点。

```yaml
drivers:
  - name: plc-modbus
    type: modbus-tcp
    settings:
      host: 192.168.1.100
      port: 502
      slave-id: 1
      timeout: 3s
      retry: 3
    tags:
      - name: temperature
        address: "40001"
        type: float32
        group: sensors
        interval: 1s
        deadband: 0.2
```

## 驱动通用字段

每个驱动实例共享以下顶层字段，`settings` 的具体内容随 `type` 不同而变化。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `name` | string | **是** | — | 驱动实例名称，全局唯一，用于规则匹配与 API 引用 |
| `type` | string | **是** | — | 驱动协议类型，见下表 |
| `settings` | object | **是** | — | 协议专属连接参数 |
| `tags` | array | **是** | — | 标签（数据点）采集定义列表 |

支持的驱动类型：

| `type` | 协议 | 典型设备 |
| --- | --- | --- |
| `modbus-tcp` | Modbus TCP | PLC、传感器网关、变频器 |
| `modbus-rtu` | Modbus RTU（串口） | RS-485 传感器、串口设备 |
| `modbus-rtuovertcp` | Modbus RTU over TCP | 串口转以太网网关 |
| `modbus-udp` | Modbus TCP over UDP | UDP 设备 |
| `modbus-rtuoverudp` | Modbus RTU over UDP | UDP 上的 RTU 帧 |
| `modbus-tls` | Modbus TCP over TLS（mTLS） | 加密通信 PLC |
| `s7` | Siemens S7 协议 | S7-300 / S7-1200 / S7-1500 PLC |
| `opcua` | OPC UA Client | SCADA、MES、OPC 服务器 |

::: info
驱动实例在核心启动时按列表顺序初始化。单个驱动初始化失败不会阻止其他驱动启动，但会在日志中记录 `error` 并将该驱动标记为 `error` 状态。
:::

---

## Modbus TCP (`modbus-tcp`)

Modbus TCP 是工业现场最常见的以太网协议，CoreC 通过 TCP 502 端口与从站通信。

```yaml
- name: plc-modbus
  type: modbus-tcp
  settings:
    host: 192.168.1.100
    port: 502
    slave-id: 1
    timeout: 3s
    retry: 3
```

### settings 字段

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `host` | string | **是** | — | 设备 IP 地址或主机名 |
| `port` | int | 否 | `502` | TCP 端口，Modbus 标准端口为 502 |
| `slave-id` | int | 否 | `1` | 从站地址，取值范围 `1–247` |
| `timeout` | duration | 否 | `3s` | 单次读写超时时间 |
| `retry` | int | 否 | `3` | 通信失败后的重试次数 |
| `reconnect-interval` | duration | 否 | `2s` | 重连初始退避间隔 |
| `reconnect-max-interval` | duration | 否 | `30s` | 重连退避上限 |
| `max-reconnect-failures` | int | 否 | `20` | 断路器阈值，连续失败达到此值后停止重连并进入冷却期 |

### 地址格式

Modbus 地址采用数字字符串，前缀决定功能码：

| 地址前缀 | 区域 | 功能码 | 示例 |
| --- | --- | --- | --- |
| `0xxxx` | 线圈（Coil） | FC01 读 / FC05 写单 / FC15 写多 | `"00001"` |
| `1xxxx` | 离散输入（Discrete Input） | FC02 读 | `"10001"` |
| `3xxxx` | 输入寄存器（Input Register） | FC04 读 | `"30001"` |
| `4xxxx` | 保持寄存器（Holding Register） | FC03 读 / FC06 写单 / FC16 写多 | `"40001"` |

::: tip
地址从 `1` 开始计数。`"40001"` 对应保持寄存器第 0 号（PLC 内部偏移 0），`"40003"` 对应偏移 2。多字节类型（如 `float32`）会自动跨寄存器读取。
:::

---

## Modbus RTU (`modbus-rtu`)

Modbus RTU 通过串口（如 RS-485）与从站通信，适用于串口传感器与现场设备。

```yaml
- name: sensor-rtu
  type: modbus-rtu
  settings:
    serial-device: /dev/ttyUSB0
    baud-rate: 9600
    data-bits: 8
    parity: none
    stop-bits: 1
    slave-id: 1
    timeout: 3s
    retry: 3
```

### settings 字段

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `serial-device` | string | **是** | — | 串口设备路径，如 `/dev/ttyUSB0`（Linux）或 `COM3`（Windows） |
| `baud-rate` | int | 否 | `9600` | 波特率 |
| `data-bits` | int | 否 | `8` | 数据位，`7` 或 `8` |
| `parity` | string | 否 | `none` | 校验位：`none`、`even`、`odd` |
| `stop-bits` | int | 否 | `0`（自动） | 停止位，`1` 或 `2`，`0` 表示由库按校验位自动选择 |
| `slave-id` | int | 否 | `1` | 从站地址，取值范围 `1–247` |
| `timeout` | duration | 否 | `3s` | 单次读写超时时间 |
| `retry` | int | 否 | `3` | 通信失败后的重试次数 |
| `reconnect-interval` | duration | 否 | `2s` | 重连初始退避间隔 |
| `reconnect-max-interval` | duration | 否 | `30s` | 重连退避上限 |
| `max-reconnect-failures` | int | 否 | `20` | 断路器阈值，连续失败达到此值后停止重连并进入冷却期 |

地址格式与 [Modbus TCP](#地址格式) 相同。

---

## Modbus RTU over TCP (`modbus-rtuovertcp`)

通过 TCP 连接传输 RTU 帧，常见于串口转以太网网关。

```yaml
- name: rtu-gateway
  type: modbus-rtuovertcp
  settings:
    host: 192.168.1.80
    port: 502
    slave-id: 1
    timeout: 3s
```

### settings 字段

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `host` | string | **是** | — | 网关 IP 地址 |
| `port` | int | 否 | `502` | TCP 端口 |
| `slave-id` | int | 否 | `1` | 从站地址 |
| `timeout` | duration | 否 | `3s` | 单次读写超时时间 |
| `retry` | int | 否 | `3` | 通信失败后的重试次数 |
| `reconnect-interval` | duration | 否 | `2s` | 重连初始退避间隔 |
| `reconnect-max-interval` | duration | 否 | `30s` | 重连退避上限 |
| `max-reconnect-failures` | int | 否 | `20` | 断路器阈值，连续失败达到此值后停止重连并进入冷却期 |

---

## Modbus UDP (`modbus-udp`)

通过 UDP 传输 Modbus TCP 帧。

```yaml
- name: modbus-udp
  type: modbus-udp
  settings:
    host: 192.168.1.90
    port: 502
    slave-id: 1
    timeout: 3s
```

### settings 字段

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `host` | string | **是** | — | 设备 IP 地址 |
| `port` | int | 否 | `502` | UDP 端口 |
| `slave-id` | int | 否 | `1` | 从站地址 |
| `timeout` | duration | 否 | `3s` | 单次读写超时时间 |
| `retry` | int | 否 | `3` | 通信失败后的重试次数 |
| `reconnect-interval` | duration | 否 | `2s` | 重连初始退避间隔 |
| `reconnect-max-interval` | duration | 否 | `30s` | 重连退避上限 |
| `max-reconnect-failures` | int | 否 | `20` | 断路器阈值，连续失败达到此值后停止重连并进入冷却期 |

---

## Modbus RTU over UDP (`modbus-rtuoverudp`)

通过 UDP 传输 RTU 帧。

```yaml
- name: rtu-udp
  type: modbus-rtuoverudp
  settings:
    host: 192.168.1.91
    port: 502
    slave-id: 1
    timeout: 3s
```

### settings 字段

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `host` | string | **是** | — | 设备 IP 地址 |
| `port` | int | 否 | `502` | UDP 端口 |
| `slave-id` | int | 否 | `1` | 从站地址 |
| `timeout` | duration | 否 | `3s` | 单次读写超时时间 |
| `retry` | int | 否 | `3` | 通信失败后的重试次数 |
| `reconnect-interval` | duration | 否 | `2s` | 重连初始退避间隔 |
| `reconnect-max-interval` | duration | 否 | `30s` | 重连退避上限 |
| `max-reconnect-failures` | int | 否 | `20` | 断路器阈值，连续失败达到此值后停止重连并进入冷却期 |

---

## Modbus TLS (`modbus-tls`)

Modbus TCP over TLS，需要双向 TLS（mTLS）证书。

```yaml
- name: modbus-tls
  type: modbus-tls
  settings:
    host: 192.168.1.100
    port: 802
    cert-file: /etc/corec/certs/client.crt
    key-file: /etc/corec/certs/client.key
    ca-file: /etc/corec/certs/ca.crt
    slave-id: 1
    timeout: 3s
```

### settings 字段

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `host` | string | **是** | — | 设备 IP 地址 |
| `port` | int | 否 | `502` | TCP 端口（Modbus TLS 常用 802） |
| `cert-file` | string | **是** | — | 客户端证书 PEM 文件路径 |
| `key-file` | string | **是** | — | 客户端私钥 PEM 文件路径 |
| `ca-file` | string | **是** | — | CA / 服务端证书 PEM 文件路径 |
| `slave-id` | int | 否 | `1` | 从站地址 |
| `timeout` | duration | 否 | `3s` | 单次读写超时时间 |
| `retry` | int | 否 | `3` | 通信失败后的重试次数 |
| `reconnect-interval` | duration | 否 | `2s` | 重连初始退避间隔 |
| `reconnect-max-interval` | duration | 否 | `30s` | 重连退避上限 |
| `max-reconnect-failures` | int | 否 | `20` | 断路器阈值，连续失败达到此值后停止重连并进入冷却期 |

---

## Siemens S7 (`s7`)

通过 ISO-on-TCP（102 端口）连接西门子 S7 系列 PLC，支持 S7-300、S7-1200、S7-1500。

```yaml
- name: siemens-s7-300
  type: s7
  settings:
    host: 192.168.1.200
    port: 102
    rack: 0
    slot: 2
    timeout: 5s
```

### settings 字段

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `host` | string | **是** | — | PLC IP 地址 |
| `port` | int | 否 | `102` | ISO-on-TCP 端口，通常无需修改 |
| `rack` | int | 否 | `0` | 机架号，标准机架为 0 |
| `slot` | int | 否 | `2` | 槽号，见下表 |
| `timeout` | duration | 否 | `5s` | 单次读写超时时间 |
| `idle-timeout` | duration | 否 | `60s` | 连接空闲超时时间 |
| `reconnect-interval` | duration | 否 | `2s` | 重连初始退避间隔 |
| `reconnect-max-interval` | duration | 否 | `30s` | 重连退避上限 |
| `max-reconnect-failures` | int | 否 | `20` | 断路器阈值，连续失败达到此值后停止重连并进入冷却期 |

### slot 取值

| PLC 型号 | `slot` | 说明 |
| --- | --- | --- |
| S7-300 | `2` | CPU 位于机架 0 槽 2 |
| S7-400 | `2`（典型） | 视实际机架配置而定 |
| S7-1200 | `1` | CPU 集成 PN 接口 |
| S7-1500 | `1` | CPU 集成 PN 接口 |

### 地址格式

S7 地址直接使用 STEP 7 / TIA Portal 中的符号：

| 地址形式 | 含义 | 推荐类型 |
| --- | --- | --- |
| `DB<n>.DBD<x>` | 数据块双字（32 位） | `float32` / `int32` / `uint32` |
| `DB<n>.DBW<x>` | 数据块字（16 位） | `int16` / `uint16` |
| `DB<n>.DBB<x>` | 数据块字节（8 位） | `int8` / `uint8` |
| `DB<n>.DBX<x>.<bit>` | 数据块位 | `bool` |
| `I<x>.<bit>` | 输入区位 | `bool` |
| `Q<x>.<bit>` | 输出区位 | `bool` |
| `M<x>.<bit>` | 位存储区位 | `bool` |

```yaml
tags:
  - name: reactor_temp
    address: "DB1.DBD0"   # DB1 双字 0，Float32
    type: float32
  - name: emergency_stop
    address: "I0.0"       # 输入位 0.0
    type: bool
```

---

## OPC UA (`opcua`)

作为 OPC UA 客户端连接 SCADA、MES 或独立 OPC 服务器，支持轮询与订阅两种采集模式。

```yaml
- name: opc-server
  type: opcua
  settings:
    endpoint: "opc.tcp://192.168.1.50:4840"
    mode: polling
    timeout: 5s
```

### settings 字段

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `endpoint` | string | **是** | — | OPC UA 端点 URL，格式 `opc.tcp://host:port` |
| `mode` | string | 否 | `polling` | 采集模式：`polling` 或 `subscription` |
| `timeout` | duration | 否 | `5s` | 连接与单次操作超时时间 |
| `security-policy` | string | 否 | — | 安全策略，如 `None`、`Basic256Sha256` |
| `security-mode` | string | 否 | — | 安全模式，如 `None`、`Sign`、`SignAndEncrypt` |
| `username` | string | 否 | — | 用户名认证（未设置时使用匿名认证） |
| `password` | string | 否 | — | 密码认证 |
| `cert-file` | string | 否 | — | 客户端证书 PEM 文件路径 |
| `key-file` | string | 否 | — | 客户端私钥 PEM 文件路径 |
| `subscription-interval` | duration | 否 | `500ms` | 订阅模式下的发布间隔 |
| `subscription-buffer` | int | 否 | `1024` | 订阅通道缓冲容量 |
| `max-batch-size` | int | 否 | `1000` | 单次读请求的最大节点数 |
| `reconnect-interval` | duration | 否 | `2s` | 重连初始退避间隔 |
| `reconnect-max-interval` | duration | 否 | `30s` | 重连退避上限 |
| `max-reconnect-failures` | int | 否 | `20` | 断路器阈值，连续失败达到此值后停止重连并进入冷却期 |

### mode 取值

| 取值 | 行为 | 适用场景 |
| --- | --- | --- |
| `polling` | 按标签 `interval` 周期主动读 | 服务端不支持订阅、或需精确控制采集频率 |
| `subscription` | 由服务端在数据变化时推送 | 实时性要求高、希望减少无效轮询 |

::: warning
并非所有 OPC UA 服务端都支持 `subscription` 模式。若服务端拒绝创建订阅，驱动会记录 `warn` 日志并自动回退到轮询采集，无需修改配置。若通过 API 调用 `Subscribe()` 而驱动处于 `polling` 模式，则返回 `subscribe not supported by this driver` 错误。
:::

### 地址格式

OPC UA 地址采用 NodeId 标准表示法：

| 形式 | 示例 | 说明 |
| --- | --- | --- | 
| 命名空间 + 字符串 | `ns=2;s=Conveyor.Speed` | 最常用，可读性强 |
| 命名空间 + 数字 | `ns=1;i=1001` | 引用数字 NodeId |
| 仅数字 | `i=2258` | Server.CurrentTime 等标准节点 |

---

## 标签配置（tags）

`tags` 列表定义驱动下每个数据点的采集规则。对应核心 `TagConfig` 结构。

```yaml
tags:
  - name: temperature
    address: "40001"
    type: float32
    group: sensors
    interval: 1s
    scale: 1.0
    offset: 0.0
    deadband: 0.2
```

### 字段说明

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `name` | string | **是** | — | 标签名称，同一驱动内唯一，作为数据点的 `tag` 字段 |
| `address` | string | **是** | — | 设备地址，格式随驱动类型而异 |
| `type` | string | **是** | — | 数据类型，见下表 |
| `group` | string | 否 | — | 分组名，用于规则按组匹配与传输主题渲染 |
| `interval` | duration | 否 | 引擎 `default-tag-interval`（默认 `1s`） | 采集周期，如 `1s`、`500ms`、`200ms` |
| `scale` | float | 否 | `1.0` | 线性缩放系数，`输出 = value * scale + offset` |
| `offset` | float | 否 | `0.0` | 线性偏移量 |
| `deadband` | float | 否 | `0` | 死区过滤阈值，变化量小于该值时不输出 |
| `read-timeout` | duration | 否 | 同 `interval` | 单次读取超时时间，独立于采集周期 |

### 支持的数据类型

| `type` | 位宽 | 说明 |
| --- | --- | --- |
| `bool` | 1 | 布尔 |
| `int8` | 8 | 有符号 8 位整数 |
| `int16` | 16 | 有符号 16 位整数 |
| `int32` | 32 | 有符号 32 位整数 |
| `int64` | 64 | 有符号 64 位整数 |
| `uint8` | 8 | 无符号 8 位整数 |
| `uint16` | 16 | 无符号 16 位整数 |
| `uint32` | 32 | 无符号 32 位整数 |
| `uint64` | 64 | 无符号 64 位整数 |
| `float32` | 32 | 单精度浮点 |
| `float64` | 64 | 双精度浮点 |
| `string` | — | 字符串 |
| `bytes` | — | 原始字节 |

### interval 写法

采用 Go duration 字符串：

| 写法 | 含义 |
| --- | --- |
| `200ms` | 200 毫秒 |
| `500ms` | 500 毫秒 |
| `1s` | 1 秒 |
| `2s` | 2 秒 |
| `1m` | 1 分钟 |

::: tip
同一 `group` 下的标签建议使用相同 `interval`，核心可将其合并为一次批量读请求，显著降低通信开销。
:::

### scale 与 offset

对原始读值做线性变换，常用于传感器量程转换：

```
输出值 = 原始值 × scale + offset
```

```yaml
- name: pressure
  address: "40003"
  type: uint16
  scale: 0.01      # 寄存器存的是 100 倍工程值
  offset: 0.0
  interval: 1s
```

### deadband

死区过滤，仅当本次读值与上次输出值的差值绝对值大于 `deadband` 时才产生新数据点，用于抑制传感器抖动、减少上行流量。

```yaml
- name: temperature
  address: "40001"
  type: float32
  deadband: 0.2    # 温度变化小于 0.2 时不输出
  interval: 1s
```

::: info
`deadband` 仅对数值类型（int*/uint*/float*）生效，`bool`、`string`、`bytes` 类型忽略该设置。设为 `0` 表示禁用死区，每次采集都输出。
:::

### read-timeout

为标签单独设置读取超时时间，独立于采集周期 `interval`。默认不设置时读取超时等于 `interval`。在高频采集场景（如 `interval: 200ms`）下，网络抖动可能导致读取耗时接近 `interval` 而频繁超时；通过设置较大的 `read-timeout`（如 `500ms`）可以容忍更多抖动。反之，低频场景下可以设置较小的 `read-timeout` 避免一个卡住的连接阻塞调度过久。

```yaml
- name: temperature
  address: "40001"
  type: float32
  interval: 200ms
  read-timeout: 500ms   # 允许单次读取最多 500ms，而非默认的 200ms
```
