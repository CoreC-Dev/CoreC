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
| `tags` | array | **是** ¹ | — | 标签（数据点）采集定义列表 |
| `tags-file` | string | 否 | — | 从外部 YAML 文件加载标签列表，适合数百以上采集点场景 ¹ |
| `tags-interval` | duration | 否 | — | `tags-file` 的热重载间隔（如 `30s`），为空则仅启动时加载一次 |

¹ `tags` 与 `tags-file` 至少配置其一；两者同时配置时，文件标签在前，内联标签在后追加，标签名在两者间必须唯一。

支持的驱动类型：

| `type` | 协议 | 典型设备 |
| --- | --- | --- |
| `modbus-tcp` | Modbus TCP | PLC、传感器网关、变频器 |
| `modbus-rtu` | Modbus RTU（串口） | RS-485 传感器、串口设备 |
| `modbus-rtuovertcp` | Modbus RTU over TCP | 串口转以太网网关 |
| `modbus-udp` | Modbus TCP over UDP | UDP 设备 |
| `modbus-rtuoverudp` | Modbus RTU over UDP | UDP 上的 RTU 帧 |
| `modbus-tls` | Modbus TCP over TLS（mTLS） | 加密通信 PLC |
| `s7` | Siemens S7 协议 | S7-200 / S7-300 / S7-400 / S7-1200 / S7-1500 PLC |
| `opcua` | OPC UA Client | SCADA、MES、OPC 服务器 |

::: info
驱动实例在核心启动时按列表顺序初始化。初始化失败（如必填的 `host` 缺失、地址无法解析、证书文件不可读）会导致整个引擎启动失败并回滚已启动的组件——核心以 fail-fast 拒绝带病运行。设备**连接**失败不阻塞启动：驱动转入 `connecting` 状态并在后台按重连参数重试。
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
| `slave-id` | int | 否 | `1` | 从站地址（Modbus unit id） |
| `timeout` | duration | 否 | `3s` | 单次读写超时时间 |
| `retry` | int | 否 | `3` | 通信失败后的重试次数 |
| `reconnect-interval` | duration | 否 | `2s` | 重连初始退避间隔 |
| `reconnect-max-interval` | duration | 否 | `30s` | 重连退避上限 |
| `max-reconnect-failures` | int | 否 | `20` | 断路器阈值，连续失败达到此值后将重连间隔提升至 5 分钟低频重试 |

### 地址格式

Modbus 地址采用数字字符串，前缀决定功能码：

| 地址前缀 | 区域 | 功能码 | 示例 |
| --- | --- | --- | --- |
| `0xxxx` | 线圈（Coil） | FC01 读 / FC05 写 | `"00001"` |
| `1xxxx` | 离散输入（Discrete Input） | FC02 读（只读） | `"10001"` |
| `3xxxx` | 输入寄存器（Input Register） | FC04 读（只读） | `"30001"` |
| `4xxxx` | 保持寄存器（Holding Register） | FC03 读 / FC06 写 16 位 / FC16 写 32 位（跨寄存器） | `"40001"` |

写入只使用单点功能码：线圈用 FC05，保持寄存器 16 位用 FC06，32 位类型（uint32/int32）跨两个寄存器用 FC16；离散输入与输入寄存器为只读区，写入直接报错。

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
| `parity` | string | 否 | `none` | 校验位：`none`、`even`、`odd`；无法识别的取值导致驱动初始化失败 |
| `stop-bits` | int | 否 | `0`（自动） | 停止位，`1` 或 `2`，`0` 表示由库按校验位自动选择 |
| `slave-id` | int | 否 | `1` | 从站地址（Modbus unit id） |
| `timeout` | duration | 否 | `3s` | 单次读写超时时间 |
| `retry` | int | 否 | `3` | 通信失败后的重试次数 |
| `reconnect-interval` | duration | 否 | `2s` | 重连初始退避间隔 |
| `reconnect-max-interval` | duration | 否 | `30s` | 重连退避上限 |
| `max-reconnect-failures` | int | 否 | `20` | 断路器阈值，连续失败达到此值后将重连间隔提升至 5 分钟低频重试 |

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
| `max-reconnect-failures` | int | 否 | `20` | 断路器阈值，连续失败达到此值后将重连间隔提升至 5 分钟低频重试 |

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
| `max-reconnect-failures` | int | 否 | `20` | 断路器阈值，连续失败达到此值后将重连间隔提升至 5 分钟低频重试 |

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
| `max-reconnect-failures` | int | 否 | `20` | 断路器阈值，连续失败达到此值后将重连间隔提升至 5 分钟低频重试 |

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
| `max-reconnect-failures` | int | 否 | `20` | 断路器阈值，连续失败达到此值后将重连间隔提升至 5 分钟低频重试 |

---

## Siemens S7 (`s7`)

通过 ISO-on-TCP（102 端口）连接西门子 S7 系列 PLC，支持 S7-200、S7-300、S7-400、S7-1200、S7-1500。

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
| `max-reconnect-failures` | int | 否 | `20` | 断路器阈值，连续失败达到此值后将重连间隔提升至 5 分钟低频重试 |

### slot 取值

| PLC 型号 | `slot` | 说明 |
| --- | --- | --- |
| S7-200 | `2` | 需 CP 243-1 以太网模块 |
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
| `I<x>.<bit>` / `IB` `IW` `ID` | 输入区位/字节/字/双字 | `bool` / `int8` / `int16` / `int32` |
| `Q<x>.<bit>` / `QB` `QW` `QD` | 输出区位/字节/字/双字 | `bool` / `int8` / `int16` / `int32` |
| `M<x>.<bit>` / `MB` `MW` `MD` | 位存储区位/字节/字/双字 | `bool` / `int8` / `int16` / `int32` |

位偏移取值 0–7，超出该范围的位地址视为无效。`E`/`A` 可分别替代 `I`/`Q`（德式记法）。

```yaml
tags:
  - name: reactor_temp
    address: "DB1.DBD0"   # DB1 双字 0，Float32
    type: float32
  - name: emergency_stop
    address: "I0.0"       # 输入位 0.0
    type: bool
```

### 批量读合并

一次轮询内，同内存区（DB 场景下同 DB 号）且地址相邻或间隙 ≤32 字节的多个非位标签会合并为一次 PLC 读请求（单段上限 480 字节，超长连续区间自动分段）；间隙超限的稀疏标签各自独立读，位标签（`DBX`/`I`/`Q`/`M` 位）始终逐个读。合并段内任一读失败时，该段全部标签标 `QualityBad`。

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
| `max-reconnect-failures` | int | 否 | `20` | 断路器阈值，连续失败达到此值后将重连间隔提升至 5 分钟低频重试 |

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
| `enforce-interval` | duration | 否 | — | 死区心跳周期：值在死区内被抑制时，距上次发布超过该时长强制发布一次（需 `deadband` > 0，且 ≥ `interval`） |
| `protect` | string | 否 | `read-write` | 写保护：`read-only` 拒绝一切写入（API 单写、批量写、MQTT 命令）；`write-only` 只写不采集（不轮询、不进缓存与规则） |
| `trigger-group` | string | 否 | — | 声明本点是一个触发组的**条件点**（值为组名）。条件点每轮必读；其值判定为真时才读取同组的详情点 |
| `triggers` | string | 否 | — | 指定本点被哪个条件点**门控**（值为条件点名字）。条件为假时本点不读取，缓存保留最后好值并自然变陈旧 |

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
调度器按 `interval` 把标签分组成采集任务：`interval` 相同（解析后等值，如 `1s` 与 `1000ms`）的标签合并为一次批量读请求，显著降低通信开销。`group` 仅用于规则匹配与主题渲染，**不参与**读取合并——需要合并的标签请使用相同的 `interval`。
:::

::: tip settings 键名校验
每个驱动的 `settings` 支持的键在配置校验时即被检查：**拼错的键（如 `slve-id`）会在 `PUT /configs`/`POST /configs/validate` 时直接报错并列出全部合法键**，而不是静默忽略后用默认值连到错误的设备。各驱动支持的键以下文的配置表为准。
:::

### scale 与 offset

对原始读值做线性变换，常用于传感器量程转换：

```text
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

**写入语义（反演）**：配置了 `scale`/`offset` 的标签，写入时默认接受**工程值**，驱动在编码前自动反演回原始值：

```text
设备原始值 = (写入值 - offset) / scale
```

以 `pressure`（scale=0.01）为例：读取显示 1.5，下发 1.5，PLC 寄存器收到 150——读写往返一致。调试或原始报文下发场景可在写指令中传 `"raw": true` 跳过反演（见 [数据 API](../api/data.md)）。`bool`/`string`/`bytes` 类型不参与变换。

变换覆盖全部数值类型（int8~int64、uint8~uint64、float32/float64）；带变换的整型读取结果为 `float64` 工程值。

### deadband

死区过滤：仅当本次读值与上次输出值的差值绝对值**不小于** `deadband` 时才产生新数据点（即 `|Δ| ≥ deadband` 通过），用于抑制传感器抖动、减少上行流量。

**质量穿透**：`bad` 或 `uncertain` 质量的点**始终通过**死区过滤（不更新基线）——质量劣化必须对下游可见，且坏值不参与基线比较，避免污染基线导致后续正常值被误吞。仅 `good` 质量的点参与死区比较与基线更新。

```yaml
- name: temperature
  address: "40001"
  type: float32
  deadband: 0.2    # 温度变化小于 0.2 时不输出
  interval: 1s
```

::: info
`deadband` 仅对数值类型（int*/uint*/float*）生效，`bool`、`string`、`bytes` 类型忽略该设置——非数值类型的每次采集值都会照常输出，且不参与基线比较。设为 `0` 表示禁用死区，每次采集都输出。
:::

### enforce-interval

死区心跳：死区会**无限期**抑制没有变化的点，下游平台因此无法区分"值没变"与"链路已断"。`enforce-interval` 在死区之上叠加一个心跳周期——被抑制的点距上次实际发布超过该周期时，强制发布一次（值取自本轮采集，时间戳照常刷新），发布后心跳时钟重置上线。

```yaml
- name: temperature
  address: "40001"
  type: float32
  interval: 1s
  deadband: 0.2
  enforce-interval: 30s   # 值不变也至少每 30s 上报一次
```

语义要点：

- 心跳只影响**被死区抑制**的点；值真正变化（越过死区阈值）时照常立即发布，不额外插入中间样本。
- 心跳标签的内部"上次发布时间"只在该点上维护，未配置心跳的点的行为与此前逐字节一致。
- 首次采集即发布（不存在历史的点不会等待一个心跳周期才开始上报）。
- `bad` / `uncertain` 质量穿透死区，本身就会发布，同样计入"上次发布"时间。
- `enforce-interval` 必须 **≥ `interval`**（否则心跳永远来不及触发，加载期拒绝）；且必须与**非零 `deadband`** 搭配（没有死区时每次采集本来就会输出，配置心跳无意义，加载期拒绝）。
- 心跳发布计入 `/stats` 的 `total_publish`，并单独计数为 Prometheus 指标 `corec_deadband_heartbeats_total{driver}`——该计数器增长表示"系统在按心跳重复上报未变化的值"，不是数据变化。

::: info
`deadband` 与 `enforce-interval` 都是**稳健性**设置而非业务逻辑：采集侧不受影响（每次都读），只在数据点发往下游前决定是否放行。因此心跳不会让慢设备读得更快，设备读取预算不变。
:::

### read-timeout

为标签单独设置读取超时时间，独立于采集周期 `interval`。默认不设置时读取超时等于 `interval`。在高频采集场景（如 `interval: 200ms`）下，网络抖动可能导致读取耗时接近 `interval` 而频繁超时；通过设置较大的 `read-timeout`（如 `500ms`）可以容忍更多抖动。反之，低频场景下可以设置较小的 `read-timeout` 避免一个卡住的连接阻塞调度过久。

注意：`interval` 相同的标签合并为同一个采集任务，任务内所有标签共用该组中**最大**的 `read-timeout`。

### protect

点位级写保护，在配置边界声明该点是否允许写入：

| 值 | 语义 |
| --- | --- |
| `read-write`（默认，或省略） | 可读可写，与历史行为一致 |
| `read-only` | 可正常采集；**所有**写入路径（`POST /write`、`POST /write/batch`、MQTT 命令通道）在到达驱动前被拒绝，API 返回 400 并带明确原因，命令通道直接进死信队列（不重试） |
| `write-only` | 只接受写入、**永不采集**：不加入调度任务，因此不出现在 `/tags`、`/tags/stream`、缓存与规则引擎输入中，也不消耗设备读取预算 |

```yaml
- name: setpoint           # 操作员设定值：允许写入
  address: "40010"
  type: float32
  protect: read-only

- name: reset_command      # 只下发的命令寄存器：从不读取
  address: "40011"
  type: bool
  protect: write-only
```

写保护由引擎在写入路径统一强制（`WriteTag` 是所有写入的唯一汇聚点），**对任何驱动生效，包括第三方驱动**——驱动无需（也无法绕过）自行实现。非法拼写（如 `readonly`）在加载期直接报错，不会被静默忽略为"可写"。

::: warning
`write-only` 点位若被规则或派生标签引用，将永远拿不到读值。需要"读回校验"的场景不要把它设为 `write-only`。
:::

```yaml
- name: temperature
  address: "40001"
  type: float32
  interval: 200ms
  read-timeout: 500ms   # 允许单次读取最多 500ms，而非默认的 200ms
```

### Timestamp 语义

所有驱动的数据点 `timestamp` 统一取 **CoreC 主机本地时钟**在采集/接收时刻的值（`time.Now()`），而非设备或服务器侧时间源：

- Modbus 与 S7 协议本身不携带时间戳，始终以本地时钟打点；
- OPC UA 轮询与订阅模式均以**本机接收时刻**打点，不采用服务器的 `SourceTimestamp`。

下游所有依赖 `timestamp` 的语义——derived tag 的 `stale-guard` 陈旧保护、API 的 `is_stale` 标注、`dataAge` 指标——都与本地时钟比较。统一时间源意味着：只要 CoreC 主机时钟正常（NTP 同步），即使 OPC UA 服务器时钟漂移，陈旧判定也不会误标点位为陈旧或误判新鲜。

**质量戳（OPC UA）**：轮询与订阅两种模式都会把服务器的 StatusCode 映射为质量戳——`OK` → `good`；其余（含 `Uncertain`）保守映射为 `bad`（订阅模式下 bad 点不带值）。质量戳随后进入管线的 bad-quality 策略、死区质量穿透与告警规则，两条采集路径语义一致。

---

## 外部标签文件（tags-file）

当一个驱动的采集点数量较多（数十至数百以上），将所有标签内联在主配置文件中会使文件臃肿且难以维护。`tags-file` 允许将标签列表拆分到独立的 YAML 文件中。

### 基本用法

主配置文件：

```yaml
drivers:
  - name: plc-modbus
    type: modbus-tcp
    settings:
      host: 192.168.1.100
      port: 502
    tags-file: ./tags/plc-modbus-tags.yaml
```

外部标签文件（`./tags/plc-modbus-tags.yaml`）格式为 YAML 列表，与内联 `tags` 字段结构完全相同：

```yaml
- name: temperature
  address: "40001"
  type: float32
  group: sensors
  interval: 1s
- name: pressure
  address: "40003"
  type: float32
  group: sensors
  interval: 1s
- name: pump_status
  address: "00001"
  type: bool
  group: actuators
  interval: 2s
```

### 内联与文件混用

`tags` 与 `tags-file` 可同时配置。文件标签排列在前，内联标签追加在后。标签名在两者间必须唯一，否则配置校验会报错。

```yaml
drivers:
  - name: plc-modbus
    type: modbus-tcp
    settings: { host: 192.168.1.100, port: 502 }
    tags-file: ./tags/plc-modbus-tags.yaml   # 文件中的标签
    tags:                                      # 内联标签（追加在后）
      - name: custom_tag
        address: "40010"
        type: float32
        interval: 5s
```

### 热重载

配置 `tags-interval` 后，引擎会按指定间隔周期性检查标签文件。文件内容变化时（基于 SHA-256 校验），引擎自动移除旧标签的采集任务并按新标签列表重建驱动，无需重启或手动触发配置重载。

```yaml
drivers:
  - name: plc-modbus
    type: modbus-tcp
    settings: { host: 192.168.1.100, port: 502 }
    tags-file: ./tags/plc-modbus-tags.yaml
    tags-interval: 30s   # 每 30 秒检查一次文件变化
```

::: info 热重载机制
- 变更检测基于文件内容的 SHA-256 哈希，仅内容真正变化时才触发重载。
- 重载过程为移除旧驱动实例并按新标签列表重新创建，期间该驱动短暂中断采集。
- 文件是热重载的唯一事实来源：重载后标签列表被文件内容**整体替换**，主配置中的内联 `tags` 不再保留（仅启动时的首次加载会做"文件 + 内联"合并）。内联与文件混用的驱动请勿依赖 `tags-interval` 热重载。
- 从文件中删除的标签会同时清空其缓存值。
- `tags-interval` 为空时不启动 watcher，标签仅在启动时加载一次。
- 通过 API `PUT /configs` 触发的全量配置重载会重新读取并合并标签文件。
:::

### trigger-group / triggers

触发式条件采集：**平时只poll一个"有没有事"的布尔/数值点，为真时才去读一大票详情寄存器**。

典型场景是报警详情组——`alarm` 标志位 + `d1..d200` 二十个报警码/时间戳/参数寄存器。不门控时 200 个点每秒都要在总线上跑一遍 200 次；门控后平时只读 1 个点，报警发生的那一轮才读全。

```yaml
tags:
  - name: alarm_flag
    address: "40000"
    type: bool
    interval: 1s
    trigger-group: alarm-group    # 条件点：声明它是哪个组的门禁

  - name: AlarmAdvance
    address: "40100"
    type: int16
    interval: 1s
    triggers: alarm_flag          # 详情点：被 alarm_flag 门控
#  - name: d1 / d2 ... 同理 triggers: alarm_flag
```

**判定规则（fail-closed）**：条件值必须"质量 good 且为真"。`bool` 为 `true`、数值非零算真；其余一律算假——包括坏质量、uncertain、字符串（`"true"` 这类不当作真）、空值，以及条件点**读取失败**。flag 不可信时宁可不读详情，也不喷数据。

**几条语义**：

- 条件点**每轮都读并照常上报**——下游要能看见它从 true 翻回 false。
- 详情点被抑制期间**不向设备发读请求**，但引擎仍会用缓存里的最后一次好值**补发一个 `quality: uncertain` 的数据点**（值不变、时间戳刷新）。这样下游看到的是"值还是那个值，但已不是本轮实采"，而不是沉默或把旧值当成新值——沉默无法与"链路断了"区分，冒充新值则是撒谎。
  - 从未采到过值的详情点（例如上电后第一次就是条件为假）没有可补发的值，保持缺席，不伪造 `0`。
  - `uncertain` 点的处理由既有策略决定：`on-bad-quality` 管的是 `bad`，`uncertain` 会照常进入规则/北向，因此告警规则若不能容忍陈旧值，应在表达式里显式排除（例如判断 `quality`），或调短 `stale-threshold` 让 `is_stale` 一起出现。
- 被抑制的轮次计入 Prometheus 计数 `corec_trigger_group_skipped_total{driver}`。计数是**按轮**而非按点：一个设备三组同时被抑制，该轮只加一。

**约束（加载期校验，不满足拒绝启动）**：

- 同一个点上 `trigger-group` 与 `triggers` **互斥**——既是门禁又是被门控的点，读取次序无定义。
- `triggers` 指向的条件点必须**同驱动**存在且带 `trigger-group`。悬空引用永远为假，等于静默停止采集，必须拦在加载期。
- 组名不得含空格或分隔符。

::: warning
触发式采集省的是**读取预算**，代价是**被抑制点的实时性**。触发指标本身劣化（读数抖动、迟迟不恢复 true）时，详情点会长期陈旧——监控 `corec_trigger_group_skipped_total` 的增长速率，停滞的增长往往比增长更快更有信息量。
:::
