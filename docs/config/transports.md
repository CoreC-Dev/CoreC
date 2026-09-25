---
title: 传输配置
description: CoreC 北向传输配置参考，涵盖 MQTT、HTTP 两种传输、批量/刷新/重试通用参数、链式核心入站及解析器配置。
---

# 传输配置

`transports` 段定义北向（northbound）传输通道。每个传输实例将采集到的数据点发布到云端或上游系统，并可选地接收反向写命令。

::: warning 字段位置
`batch-size`、`flush-interval`、`retry-count`、`buffer-size`、`fallback` 是传输的**顶层字段**（与 `settings` 平级），**不能**写进 `settings` 内部。写进 `settings` 会被静默忽略，传输退化为同步逐点发布且不报错。配置校验会对此快速失败。
:::

```yaml
transports:
  - name: cloud-mqtt
    type: mqtt
    settings:
      broker: tcp://broker.emqx.io:1883
      client-id: factory-edge-01
      qos: 1
      topic-template: "factory/{{.Driver}}/{{.Group}}/{{.Tag}}"
      command-topic: "factory/commands/#"
    batch-size: 50            # ← 顶层字段，与 settings 平级
    flush-interval: 1s        # ← 顶层字段
```

## 传输通用字段

所有传输实例共享以下字段，`settings` 内容随 `type` 不同而变化。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `name` | string | **是** | — | 传输实例名称，全局唯一，用于规则 `target` 引用 |
| `type` | string | **是** | — | 传输类型：`mqtt` 或 `http` |
| `settings` | object | **是** | — | 协议专属连接参数 |
| `batch-size` | int | 否 | `100` | 单次批量发送的最大数据点数。**顶层字段**（与 `settings` 平级） |
| `flush-interval` | duration | 否 | — | 触发刷新的时间间隔，未设置则不启用定时刷新。**顶层字段** |
| `retry-count` | int | 否 | `0`（不重试） | 发送失败后的重试次数。**顶层字段** |
| `buffer-size` | int | 否 | `100` | 命令/数据通道容量，超出后丢弃。**顶层字段** |
| `fallback` | string | 否 | — | 备用传输名称，本传输发布失败时自动切换到该传输。**顶层字段** |

支持的传输类型：

| `type` | 协议 | 典型目标 |
| --- | --- | --- |
| `mqtt` | MQTT 3.1.1 | IoT 平台、边缘网关、命令回写 |
| `http` | HTTP/REST | MES、数据湖 API、Webhook |

::: info
`batch-size` 与 `flush-interval` 共同决定发送时机：**任一条件先满足即触发发送**。例如 `batch-size: 50` 且 `flush-interval: 1s`，则 1 秒内攒满 50 点立即发送，否则满 1 秒发送当前已攒的点。
:::

---

## 通用参数详解

### batch-size

单次批量发送的数据点上限。较大的值提升吞吐但增加单次延迟，较小的值降低延迟但增加请求次数。

| 场景 | 推荐值 |
| --- | --- |
| 高频传感器、云端聚合 | `100`–`500` |
| 低频关键数据、需低延迟 | `1`–`10` |
| MQTT 受限于单包大小 | `50`–`100` |

### flush-interval

定时刷新间隔，保证数据点不会因 `batch-size` 未凑满而无限滞留。采用 Go duration 字符串（`1s`、`500ms` 等）。**未设置时不启用定时刷新**，仅按 `batch-size` 触发发送。

::: tip
若数据稀疏且未设置 `flush-interval`，点会长期滞留队列，**不建议**用于实时场景。建议显式设置一个合理的刷新间隔。
:::

### retry-count

发送失败后的重试次数。每次重试间隔采用指数退避（base × 2^n）。**默认 `0` 表示不重试**。重试耗尽后，若启用了全局 `buffer`，数据批次写入本地磁盘缓冲待后续回放；否则数据点丢弃并累加 `failed` 计数。

::: tip 离线缓冲
全局 `buffer` 配置启用后，传输重试耗尽时数据会写入本地磁盘，待传输恢复后自动回放，防止数据丢失。详见[全局配置 - buffer](./global#buffer)。
:::

### fallback

配置备用传输名称。当本传输发布失败（重试耗尽后）时，引擎自动将数据点转发到 `fallback` 指定的传输。适用于双链路冗余场景（如 MQTT 主链路 + HTTP 备用链路）。fallback 仅尝试一次，不递归（即备用传输的 fallback 不会被跟随）。

```yaml
transports:
  - name: primary-mqtt
    type: mqtt
    settings: { ... }
    fallback: backup-http    # 主链路失败时切换到 HTTP 备用
  - name: backup-http
    type: http
    settings: { ... }
```

### buffer-size

命令/数据通道容量（内部 command/data channel）。当入站数据或命令的到达速率超过引擎消费速率、通道已满时，**丢弃新到达的数据点**（非阻塞写入）并记录 `warn` 日志。默认 `100`。

---

## MQTT (`mqtt`)

通过 MQTT 协议发布数据点，并可选订阅命令主题接收反向写命令（下行控制）。

```yaml
- name: cloud-mqtt
  type: mqtt
  settings:
    broker: tcp://broker.emqx.io:1883
    client-id: factory-edge-01
    qos: 1
    topic-template: "factory/{{.Driver}}/{{.Group}}/{{.Tag}}"
    command-topic: "factory/commands/#"
  batch-size: 50
  flush-interval: 1s
```

### settings 字段

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `broker` | string | **是** | — | Broker 地址，格式 `scheme://host:port` |
| `client-id` | string | 否 | 自动生成 | 客户端 ID，需在 Broker 范围内唯一 |
| `username` | string | 否 | — | 用户名认证 |
| `password` | string | 否 | — | 密码认证 |
| `qos` | int | 否 | `1` | 服务质量等级，取值 `0`、`1`、`2` |
| `retained` | bool | 否 | `false` | 是否发布保留消息 |
| `topic-template` | string | 否 | <code v-pre>corec/{{.Driver}}/{{.Tag}}</code> ¹ | 发布主题模板，支持 Go 模板变量 |
| `command-topic` | string | 否 | — ¹ | 订阅的命令主题，支持通配符 `+` / `#` |
| `data-topic` | string | 否 | — ¹ | 订阅的数据主题（链式核心入站），需配合 `parser` |
| `keep-alive` | duration | 否 | `60s` | 心跳保活间隔 |
| `connect-timeout` | duration | 否 | `10s` | 连接超时时间 |
| `auto-reconnect` | bool | 否 | `true` | 是否自动重连 |
| `clean-session` | bool | 否 | `true` | 是否使用干净会话 |
| `connect-retry` | bool | 否 | `true` | 是否在连接失败后自动重试 |
| `connect-retry-interval` | duration | 否 | `5s` | 连接重试间隔 |
| `subscribe-timeout` | duration | 否 | `5s` | 订阅操作超时 |
| `publish-timeout` | duration | 否 | `5s` | 发布操作超时 |
| `disconnect-quiesce` | duration | 否 | `1s` | 断开连接时的静默等待时长 |
| `parser` | object | 否 | — | 入站数据解析器配置，见[解析器配置](#解析器配置-parser) |

::: info ¹ 自动发现
当配置了 `node.id`（拓扑自动发现）时，`topic-template`、`command-topic`、`data-topic`、`parser` 在省略时会自动生成或发现。显式配置的字段始终优先，不会被覆盖。详见 [链式核心 → 拓扑自动发现](/guide/chained-core#拓扑自动发现-auto-discovery)。
:::

### broker

| scheme | 含义 | 示例 |
| --- | --- | --- |
| `tcp://` | 明文 MQTT | `tcp://broker.emqx.io:1883` |
| `ssl://` `tls://` `mqtts://` `mqtt+ssl://` `tcps://` | TLS 加密 MQTT | `mqtts://broker.emqx.io:8883` |
| `ws://` | WebSocket | `ws://broker.emqx.io:8083/mqtt` |
| `wss://` | WebSocket over TLS | `wss://broker.emqx.io:8084/mqtt` |

### qos

| 取值 | 含义 | 适用场景 |
| --- | --- | --- |
| `0` | 至多一次，不保证送达 | 高频低价值遥测 |
| `1` | 至少一次，保证送达但可能重复 | **推荐**，工业数据常用 |
| `2` | 恰好一次，开销最大 | 命令回写、计费数据 |

::: warning
`client-id` 必须唯一。重复 ID 会导致后连接的客户端踢掉先前的会话，造成数据中断。多实例部署时建议附加主机名或 Pod 名后缀。
:::

### topic-template

发布主题使用 Go template 语法，可引用数据点字段：

| 变量 | 来源 | 示例值 |
| --- | --- | --- |
| `&lbrace;&lbrace;.Driver&rbrace;&rbrace;` | 驱动实例名 | `plc-modbus` |
| `&lbrace;&lbrace;.Group&rbrace;&rbrace;` | 标签分组 | `sensors` |
| `&lbrace;&lbrace;.Tag&rbrace;&rbrace;` | 标签名 | `temperature` |
| `&lbrace;&lbrace;.Device&rbrace;&rbrace;` | 设备名 | `plc-01` |

```yaml
topic-template: "factory/{{.Driver}}/{{.Group}}/{{.Tag}}"
# 渲染结果：factory/plc-modbus/sensors/temperature
```

::: tip
主题层级设计建议遵循 `组织/产线/设备/测点` 的从宽到窄结构，便于下游按层级订阅与权限控制。
:::

### command-topic

订阅该主题以接收反向写命令。消息体为 JSON 格式的 `WriteCommand`：

```json
{
  "driver": "plc-modbus",
  "device": "plc-01",
  "tag": "pump_status",
  "value": true,
  "type": "bool"
}
```

支持 MQTT 通配符：

| 通配符 | 含义 |
| --- | --- |
| `+` | 单层通配，匹配一个主题层级 |
| `#` | 多层通配，匹配剩余所有层级（只能放末尾） |

```yaml
command-topic: "factory/commands/#"   # 接收所有命令
command-topic: "factory/commands/plc-modbus/+"   # 仅该驱动的命令
```

### data-topic（链式核心入站）

订阅数据主题以接收上游 CoreC 实例或第三方 MQTT 发布者的数据（链式核心 / chained-core 入站）。设置 `data-topic` 后需配合 `parser` 配置将消息体解析为 `DataPoint`。解析后的数据点进入引擎 DataBus，流经规则并可被重新发布到下游传输，实现多跳中继拓扑（边缘 → 网关 → 云端）。

```yaml
settings:
  data-topic: "upstream/factory/#"
  parser:
    type: default          # json.Unmarshal(DataPoint)，CoreC→CoreC 零成本路径
```

---

## HTTP (`http`)

通过 HTTP/REST 将数据点批量推送到上游 API，适用于 MES、数据湖、Webhook 等场景。HTTP 传输也可通过 webhook 接收入站数据（链式核心入站）。

::: warning
HTTP Push 的反向控制指令通道（`OnCommand()`）存在但当前不投递。反向下发请使用 MQTT `command-topic`。
:::

```yaml
- name: mes-http-push
  type: http
  settings:
    url: "http://localhost:8080/api/v1/telemetry"
    method: POST
    headers:
      Authorization: "Bearer mes-secret-key"
      Content-Type: "application/json"
    timeout: 3s
  batch-size: 100
  flush-interval: 5s
```

### settings 字段

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `url` | string | 条件必填 | — | 上游 API 地址；未设置 `webhook-addr` 时必填，仅作 webhook 入站时可省略 |
| `method` | string | 否 | `POST` | HTTP 方法，通常 `POST` 或 `PUT` |
| `headers` | map[string]string | 否 | — | 自定义请求头 |
| `timeout` | duration | 否 | `5s` | HTTP 请求超时时间 |
| `max-idle-conns` | int | 否 | `100` | 连接池最大空闲连接数 |
| `max-idle-conns-per-host` | int | 否 | `20` | 每主机最大空闲连接数 |
| `idle-conn-timeout` | duration | 否 | `90s` | 空闲连接超时时间 |
| `webhook-addr` | string | 否 | — | Webhook 监听地址（链式核心入站），如 `0.0.0.0:9091` |
| `webhook-path` | string | 否 | `/data` | Webhook URL 路径 |
| `parser` | object | 否 | — | 入站数据解析器配置，见[解析器配置](#解析器配置-parser) |

### url

完整 URL，包含 scheme 与路径。支持 `http://` 与 `https://`，使用 `https://` 时核心自动校验服务端证书。当传输仅用于 webhook 入站（已设置 `webhook-addr`）时，`url` 可省略。

```yaml
url: "https://mes.example.com/api/v1/telemetry"
```

### method

| 取值 | 含义 |
| --- | --- |
| `POST` | 创建新记录，最常用 |
| `PUT` | 整体替换资源 |
| `PATCH` | 部分更新 |

### headers

键值对形式的自定义请求头，常用于鉴权与内容类型声明：

```yaml
headers:
  Authorization: "Bearer mes-secret-key"
  Content-Type: "application/json"
  X-Tenant-Id: "factory-01"
```

::: info
核心默认以 JSON 数组形式发送批量数据点，即 `[]DataPoint`。若上游要求不同结构，可在网关层做转换，或通过规则 `transform` 调整。
:::

### timeout

单次 HTTP 请求（含重试中的每次尝试）的超时时间，默认 `5s`。超时后视为本次发送失败，进入重试流程。

### 连接池

以下字段控制底层 HTTP 客户端的连接池行为，均为可选：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `max-idle-conns` | `100` | 连接池最大空闲连接数 |
| `max-idle-conns-per-host` | `20` | 每主机最大空闲连接数 |
| `idle-conn-timeout` | `90s` | 空闲连接超时时间 |

### webhook-addr / webhook-path（链式核心入站）

设置 `webhook-addr` 后，传输会启动一个 HTTP 服务接收上游 CoreC 实例或第三方 POST 请求中的数据（链式核心入站）。请求体可以是单个 `DataPoint` JSON 对象或 `[]DataPoint` 数组，经 `parser` 解析后进入引擎 DataBus。

```yaml
settings:
  webhook-addr: "0.0.0.0:9091"
  webhook-path: "/ingest"
  parser:
    type: default          # json.Unmarshal(DataPoint)
```

---

## 解析器配置（parser）

当传输配置了入站通道（MQTT `data-topic` 或 HTTP `webhook-addr`）时，需通过 `parser` 配置将消息体解析为 `DataPoint`。`parser` 是 `settings` 下的一个子对象。

### parser.type

| 取值 | 说明 |
| --- | --- |
| `default` | 直接 `json.Unmarshal` 为 `DataPoint`，CoreC→CoreC 零成本路径（默认） |
| `jsonpath` | 通过 Go template 将任意 JSON 字段映射到 `DataPoint` |
| `raw` | 将整个 payload 视为标量值，tag 可从 MQTT topic 提取 |

### default 解析器

无需额外配置，消息体须为 `DataPoint` 的 JSON 表示。

### jsonpath 解析器

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `parser.driver` | string | 否 | — | 静态 driver 字段值 |
| `parser.tag` | string | 否 | — | tag 字段的模板或静态字符串 |
| `parser.value` | string | 否 | — | value 字段的模板路径 |
| `parser.data-type` | string | 否 | — | 数据类型名，如 `float32` |
| `parser.group` | string | 否 | — | 静态 group 字段值 |
| `parser.device` | string | 否 | — | 静态 device 字段值 |
| `parser.timestamp` | string | 否 | — | timestamp 字段的模板路径 |
| `parser.timestamp-format` | string | 否 | `rfc3339` | 时间戳格式：`rfc3339`、`unix`、`unixmilli` |

```yaml
parser:
  type: jsonpath
  driver: "lora-gateway"
  tag: "{{ .payload.dev_id }}"
  value: "{{ .payload.temp }}"
  data-type: "float32"
  group: "sensors"
  timestamp: "{{ .payload.ts }}"
  timestamp-format: "unix"
```

### raw 解析器

将整个 payload 视为标量值，tag 可从 MQTT topic 的指定层级提取。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `parser.driver` | string | 否 | — | 静态 driver 字段值 |
| `parser.tag` | string | 否 | — | 静态 tag 字段值 |
| `parser.tag-from-topic` | int | 否 | `0` | 取 MQTT topic 第 N 段作为 tag，`0` 表示使用静态 `tag` |
| `parser.data-type` | string | 否 | `float64` | 数据类型名 |
| `parser.group` | string | 否 | — | 静态 group 字段值 |
| `parser.device` | string | 否 | — | 静态 device 字段值 |
| `parser.timestamp-format` | string | 否 | `rfc3339` | 时间戳格式 |

```yaml
parser:
  type: raw
  driver: "factory"
  tag-from-topic: 2      # 使用 MQTT topic 第 2 段作为 tag
  data-type: "float32"
```

---

## 完整示例

```yaml
transports:
  # MQTT 云端传输：发布 + 命令回写 + 链式入站
  - name: cloud-mqtt
    type: mqtt
    settings:
      broker: ssl://broker.emqx.io:8883
      client-id: factory-edge-01
      qos: 1
      topic-template: "factory/{{.Driver}}/{{.Group}}/{{.Tag}}"
      command-topic: "factory/commands/#"
      data-topic: "upstream/factory/#"
      parser:
        type: default
    batch-size: 50
    flush-interval: 1s
    retry-count: 5
    buffer-size: 100

  # HTTP 推送：MES 数据接入 + Webhook 入站
  - name: mes-http-push
    type: http
    settings:
      url: "https://mes.example.com/api/v1/telemetry"
      method: POST
      headers:
        Authorization: "Bearer mes-secret-key"
      timeout: 5s
      webhook-addr: "0.0.0.0:9091"
      webhook-path: "/ingest"
      parser:
        type: default
    batch-size: 100
    flush-interval: 5s
    retry-count: 3
    buffer-size: 100
```

::: tip
对关键数据建议同时配置 MQTT 与 HTTP 两个传输，并通过规则 `mirror` 动作双发，实现传输层冗余。
:::
