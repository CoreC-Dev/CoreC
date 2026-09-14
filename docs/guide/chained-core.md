---
title: 链式核心
description: 拓扑组网方式 —— 纯订阅转发、CoreC 级联、多级中继、协议转换、多对一汇聚、一对多分发、双向级联
---

# 链式核心（Chained Core）

链式核心让 CoreC 实例既能**接收**上游数据，又能**转发**到下游。一个 Transport 配了 `data-topic`（MQTT）或 `webhook-addr`（HTTP）就开启接收，收到的数据经 Parser 解析后喂回 DataBus，走完规则匹配再发布到下游——实现 edge → gateway → cloud 的多跳中继。

其机制是：入站 Transport 把收到的数据回流到 DataBus，用 channel 代替管道，让结构化的 DataPoint 重新进入 processingLoop 走完规则匹配与下游发布。

## 数据流

```
Driver.Read()  ──▶ onDriverData() ──▶ DataBus.Push() ─┐
                                                        ├──▶ processingLoop ──▶ 规则 ──▶ Publish
Transport.OnData() ──▶ startDataListener() ──▶ DataBus.Push() ─┘
```

两个数据来源（驱动轮询、Transport 接收）汇入同一条 DataBus，对 processingLoop 完全透明。

## 判断一个节点是"采集"还是"中继"

| | 采集节点 | 中继节点 |
|:---|:---|:---|
| `drivers` | 有驱动配置 | `drivers: []` |
| Transport | 只有 `topic-template` / `url` | 额外配了 `data-topic` / `webhook-addr` |
| 数据来源 | `Driver.Read()` | `Transport.OnData()` |

## Parser 三种模式

| 模式 | 适用场景 | 原理 |
|:---|:---|:---|
| `default` | CoreC → CoreC | `json.Unmarshal(DataPoint)`，格式天然匹配，零成本 |
| `jsonpath` | 第三方 JSON | `text/template` 字段映射，双花括号模板语法 |
| `raw` | 裸数值 | payload 即 value，tag 从 topic 路径提取 |

---

::: tip 可运行 Demo
本文档中所有场景均有对应的 **Docker Compose 可运行实现**，位于 `demo/chained/scenario1/` ~ `scenario8/`。
进入对应目录执行 `docker compose up` 即可看到数据在链上真实流动，详见 [demo/chained/README.md](https://github.com/CoreC-Dev/CoreC/tree/main/demo/chained/README.md)。
:::

---

## 场景一：纯订阅转发（协议网关）

CoreC 不连任何设备，只订阅第三方设备的 MQTT，解析后转发到云端。角色是**协议适配器**。

```
LoRa网关 ──MQTT──▶ CoreC ──MQTT──▶ 云端
(非CoreC格式)       (解析转换)      (标准格式)
```

```yaml {v-pre}
drivers: []

transports:
  - name: bridge
    type: mqtt
    settings:
      broker: tcp://broker:1883
      topic-template: "cloud/{{.Driver}}/{{.Tag}}"
      data-topic: "lora/+/up"              # 订阅第三方
      parser:
        type: jsonpath                      # 第三方格式 → DataPoint
        driver: "lora"
        tag: "{{ .payload.dev_id }}"
        value: "{{ .payload.temp }}"
        data-type: "float32"

rules:
  - { match: "ALL", action: forward, target: bridge }
```

**特点**：`drivers: []`，数据全从 `data-topic` 进来。CoreC 只做格式转换。

---

## 场景二：两个 CoreC 级联（边缘 → 云端）

最常见的链式拓扑。A 在边缘连 PLC 采集，B 在云端收数据做告警 / 存储。

```
PLC ──modbus──▶ CoreC-A ──MQTT──▶ CoreC-B ──MQTT──▶ 云端
                 (边缘)            (中继)
```

**CoreC-A（边缘）：**

```yaml {v-pre}
drivers:
  - name: plc
    type: modbus-tcp
    settings: { host: 192.168.1.100, port: 502 }
    tags:
      - { name: temperature, address: "40001", type: float32, interval: 1s }

transports:
  - name: mqtt-out
    type: mqtt
    settings:
      broker: tcp://broker:1883
      topic-template: "edgeA/{{.Driver}}/{{.Tag}}"
      # 没有 data-topic，A 不收数据

rules:
  - { match: "ALL", action: forward, target: mqtt-out }
```

**CoreC-B（中继）：**

```yaml {v-pre}
drivers: []

transports:
  - name: mqtt-chain
    type: mqtt
    settings:
      broker: tcp://broker:1883
      topic-template: "cloud/{{.Driver}}/{{.Tag}}"
      data-topic: "edgeA/#"               # 收 A 发的
      parser:
        type: default                      # A 发的就是 DataPoint JSON

rules:
  - { match: "tag == 'temperature' && value > 90", action: alert, target: mqtt-chain, priority: 1 }
  - { match: "ALL", action: forward, target: mqtt-chain, priority: 999 }
```

**特点**：A 的 `topic-template` 和 B 的 `data-topic` 必须匹配。A 发 `edgeA/plc/temperature`，B 订阅 `edgeA/#` 就能收到。

---

## 场景三：多级级联（边缘 → 网关 → 云端）

三跳或更多。每一跳都可以做规则处理。

```
PLC ──▶ CoreC-A ──MQTT──▶ CoreC-B ──HTTP──▶ CoreC-C ──MQTT──▶ 云端
         (车间)            (厂区网关)          (区域中心)
```

**CoreC-B（厂区网关，MQTT 收 → HTTP 发）：**

```yaml {v-pre}
drivers: []

transports:
  - name: from-edge
    type: mqtt
    settings:
      broker: tcp://factory-broker:1883
      topic-template: "unused"
      data-topic: "edgeA/#"
      parser: { type: default }

  - name: to-region
    type: http
    settings:
      url: "http://corec-c:9091/ingest"    # 发给 C 的 webhook

rules:
  - { match: "ALL", action: forward, target: to-region }
```

**CoreC-C（区域中心，HTTP 收 → MQTT 发）：**

```yaml {v-pre}
drivers: []

transports:
  - name: from-gateway
    type: http
    settings:
      url: "http://localhost:9999/unused"
      webhook-addr: "0.0.0.0:9091"
      webhook-path: "/ingest"
      parser: { type: default }

  - name: to-cloud
    type: mqtt
    settings:
      broker: tcp://cloud:1883
      topic-template: "cloud/{{.Driver}}/{{.Tag}}"

rules:
  - { match: "ALL", action: forward, target: to-cloud }
```

**特点**：中间每一跳都可以做过滤、告警、变换。比如 B 过滤掉车间不关心的数据，C 做区域级告警。

---

## 场景四：协议转换（MQTT → HTTP）

CoreC 收 MQTT 数据，通过 HTTP 推到 REST API。解决**云端只接受 HTTP，设备只发 MQTT** 的情况。

```
设备 ──MQTT──▶ CoreC ──HTTP──▶ MES/ERP系统
```

```yaml {v-pre}
drivers: []

transports:
  - name: mqtt-in
    type: mqtt
    settings:
      broker: tcp://broker:1883
      topic-template: "unused"
      data-topic: "factory/#"
      parser: { type: default }

  - name: http-out
    type: http
    settings:
      url: "http://mes.example.com/api/v1/telemetry"
      headers:
        Authorization: "Bearer key"

rules:
  - { match: "ALL", action: forward, target: http-out }
```

反过来也行（HTTP → MQTT）：配 `webhook-addr` 收，`topic-template` 发。

---

## 场景五：多对一汇聚

多个边缘节点往同一个中继汇聚，中继统一处理后转发。

```
CoreC-A ──MQTT──▶
CoreC-B ──MQTT──▶ CoreC-G ──MQTT──▶ 云端
CoreC-C ──MQTT──▶
```

```yaml {v-pre}
# CoreC-G（汇聚网关）
drivers: []

transports:
  - name: aggregator
    type: mqtt
    settings:
      broker: tcp://broker:1883
      topic-template: "cloud/{{.Driver}}/{{.Tag}}"
      data-topic: "edge/#"            # # 匹配所有子层，收 edge/A/... edge/B/... edge/C/... 所有节点
      parser: { type: default }

rules:
  - { match: "ALL", action: forward, target: aggregator }
```

A 发 `edge/A/...`，B 发 `edge/B/...`，C 发 `edge/C/...`，G 订阅 `edge/#` 全收。

> MQTT 单层通配符 `+` 必须独占一整个层级（如 `edge/+/up`），不能写成 `edge+/#`。若只需匹配一层节点名，可用 `edge/+/...` 形式并让各节点发布到 `edge/A/...`、`edge/B/...`。

---

## 场景六：一对多分发

一个边缘节点采集数据，同时推到多个云端（MQTT + HTTP，或两个不同 MQTT broker）。

```
              ┌──MQTT──▶ 云端A
PLC ──▶ CoreC ┤
              └──HTTP──▶ MES系统
```

```yaml {v-pre}
drivers:
  - name: plc
    type: modbus-tcp
    settings: { host: 192.168.1.100, port: 502 }
    tags:
      - { name: temperature, address: "40001", type: float32, interval: 1s }

transports:
  - name: cloud-mqtt
    type: mqtt
    settings:
      broker: tcp://cloud-a:1883
      topic-template: "cloud/{{.Driver}}/{{.Tag}}"

  - name: mes-http
    type: http
    settings:
      url: "http://mes.example.com/api/telemetry"

rules:
  - { match: "ALL", action: mirror, targets: [cloud-mqtt, mes-http] }
```

**特点**：用 `action: mirror` 同时推多个 target。这不是链式核心的新功能，但可以跟链式组合。

---

## 场景七：双向级联（数据上行 + 命令下行）

数据从边缘往云端走，控制命令从云端往边缘走。两个方向都经过中继。

```
         数据上行                    数据上行
PLC ◀──modbus──▶ CoreC-A ◀──MQTT──▶ CoreC-B ◀──MQTT──▶ 云端
         写命令                    写命令
         下行                       下行
```

**CoreC-A（边缘）：**

```yaml {v-pre}
transports:
  - name: mqtt
    type: mqtt
    settings:
      broker: tcp://broker:1883
      topic-template: "edgeA/{{.Driver}}/{{.Tag}}"
      command-topic: "edgeA/commands/#"    # 收 B 转发下来的命令
```

**CoreC-B（中继）：**

```yaml {v-pre}
transports:
  - name: mqtt-chain
    type: mqtt
    settings:
      broker: tcp://broker:1883
      topic-template: "cloud/{{.Driver}}/{{.Tag}}"
      data-topic: "edgeA/#"                # 收 A 的数据
      command-topic: "cloud/commands/#"    # 收云端的命令
      parser: { type: default }
```

::: warning 命令跨实例转发当前不支持
上图为**目标拓扑**，但 CoreC **当前没有命令重发布机制**：当 B 通过 `command-topic` 收到云端命令时，它只会调用**自身本地驱动**的 `Write()`（而 B 是中继节点，`drivers: []`，没有本地驱动可写），**不会**把命令重新发布到 `edgeA/commands/#`。因此命令无法从 B 透传到 A。

目前可行的替代方案：
- **方案一**：云端直接向 A 的 `command-topic`（`edgeA/commands/#`）下发命令，跳过中继 B。
- **方案二**：在中继 B 上用外部脚本 / 规则引擎监听 `cloud/commands/#` 并转发到 `edgeA/commands/#`。
- **方案三**：等待后续版本支持 command 透传 / 重发布能力。

每个 CoreC 实例的 `command-topic` 只触发**自身本地驱动**的写入，不会跨实例传递命令。
:::

---

## 场景总览

| 场景 | 拓扑 | CoreC 角色 | 链式核心 | 关键配置 |
|:---|:---|:---|:---|:---|
| ① 纯订阅转发 | 第三方 → CoreC → 云端 | 协议适配器 | ✅ | `data-topic` + `parser: jsonpath` |
| ② 两级级联 | CoreC-A → CoreC-B | A=采集，B=中继 | ✅ | B 用 `data-topic` + `parser: default` |
| ③ 多级级联 | A → B → C | 每跳都可处理 | ✅ | 中间节点 `data-topic` / `webhook-addr` |
| ④ 协议转换 | MQTT → CoreC → HTTP | 协议桥 | ✅ | `data-topic` 收，`url` 发 |
| ⑤ 多对一汇聚 | A+B+C → G → 云端 | 汇聚网关 | ✅ | G 用 `data-topic: "edge/#"` 收所有 `edge/A/...` |
| ⑥ 一对多分发 | CoreC → 多个云端 | 边缘采集 | ❌ | `action: mirror` + 多 target |
| ⑦ 双向级联 | 数据上行 + 命令下行 | 中继双向 | ✅ | `data-topic` + `command-topic` |

## 配置速查

### MQTT Transport

| 参数 | 作用 | 必填 |
|:---|:---|:---|
| `broker` | MQTT broker 地址 | ✅ |
| `topic-template` | 发数据的 topic 模板 | 有默认值 ² |
| `command-topic` | 收写命令的 topic | 可选 ² |
| `data-topic` | 收数据的 topic（开启链式核心） | 可选 ² |
| `parser` | 数据解析器（配了 `data-topic` 就必须配） | 条件必填 ² |

² 配置了 `node.id` 时，这些字段省略则自动生成/发现，显式配置优先。详见上方[拓扑自动发现](#拓扑自动发现-auto-discovery)。

### HTTP Transport

| 参数 | 作用 | 必填 |
|:---|:---|:---|
| `url` | 发数据的目标 URL | ✅ |
| `webhook-addr` | 收数据的监听地址（开启链式核心） | 可选 |
| `webhook-path` | 收数据的 URL 路径（默认 `/data`） | 可选 |
| `parser` | 数据解析器（配了 `webhook-addr` 就必须配） | 条件必填 |

### Parser 配置

```yaml {v-pre}
# CoreC → CoreC（零成本，格式天然匹配）
parser:
  type: default

# 第三方 JSON（字段名不同）
parser:
  type: jsonpath
  driver: "lora-gateway"
  tag: "{{ .payload.dev_id }}"
  value: "{{ .payload.temp }}"
  data-type: "float32"
  group: "sensors"
  timestamp: "{{ .payload.ts }}"
  timestamp-format: "unix"          # rfc3339 | unix | unixmilli

# 裸数值（payload 即 value）
parser:
  type: raw
  driver: "factory"
  tag: "temperature"                # 静态 tag
  # 或 tag-from-topic: 2            # 从 topic 第 2 段取 tag
  data-type: "float64"
```

## 拓扑自动发现（Auto-Discovery）

当配置了 `node` 段时，引擎启用拓扑自动发现：节点通过 MQTT 心跳广播自己的身份和端点信息，自动建立 CoreC 实例间的连接，无需手动配置 `topic-template`、`data-topic`、`parser` 等字段。

### 核心原则：写了的用你写的，没写的才自动填

```
配置里写了 topic-template → 用你写的（原有功能不变）
配置里没写 topic-template → 自动生成 "topo/{node-id}/data/{{.Driver}}/{{.Tag}}"

data-topic、parser、command-topic 同理
```

**没有 `node` 段的配置完全不受影响**——向后兼容。

### 配置

```yaml {v-pre}
node:
  id: edge-A                    # 节点唯一标识
  role: collector               # collector | relay | aggregator | sink
  subscribe: [edge-B]           # 声明要接收哪些上游节点的数据
  # topic-prefix: topo          # 可选，默认 "topo"
```

### 自动生成的字段

| 字段 | 自动生成值 | 条件 |
|:---|:---|:---|
| `topic-template` | <code v-pre>topo/{node-id}/data/{{.Driver}}/{{.Tag}}</code> | MQTT transport 未显式配置时 |
| `command-topic` | `topo/{node-id}/cmd/#` | MQTT transport 未显式配置时 |
| `parser` | `{ type: default }` | 有 `data-topic` 但未配 parser 时 |
| forward rule | `match: ALL → first transport` | 无 rules 时 |

### 心跳机制

每个节点在连接的每个 MQTT broker 上广播心跳到 `corec/_discovery/{node-id}`：

```json
{
  "id": "edge-A",
  "role": "collector",
  "publish": { "type": "mqtt", "topic": "topo/edge-A/data/#" },
  "ts": 1234567890
}
```

- 心跳间隔：5 秒
- 消息 retained：后启动的节点也能发现先启动的节点
- 每个 broker 独立发现：多 broker 场景下，节点在自己连接的每个 broker 上做发现

### 多 broker 场景

发现是 per-broker 的：同一个 broker 上的节点自动发现，跨 broker 的节点通过桥接节点（连多个 broker）连接。`subscribe` 不需要指定 broker——节点在自己所有连接的 broker 上搜索上游。

```
工厂 (broker-1)              云端 (broker-2)
  CoreC-A ──→ CoreC-B(连两个broker) ──→ CoreC-C
```

B 在 broker-1 发现 A，在 broker-2 被 C 发现，数据自动跨 broker 流转。

### 与第三方数据共存

一个节点可以同时有自动发现 transport 和手动配置 transport：

```yaml {v-pre}
transports:
  - name: mqtt-auto             # CoreC 间通道（自动发现）
    type: mqtt
    settings:
      broker: tcp://broker:1883
      # data-topic 不写 → 自动发现上游

  - name: mqtt-3rdparty         # 第三方数据通道（手动配置）
    type: mqtt
    settings:
      broker: tcp://broker:1883
      data-topic: "lora/+/up"           # 保留：第三方订阅
      parser:                            # 保留：第三方解析
        type: jsonpath
        driver: "lora"
        tag: "{{ .payload.dev_id }}"
        value: "{{ .payload.temp }}"
        data-type: "float32"
```

### 示例

参见 `demo/chained/scenario8/`：与 scenario2 相同的拓扑，但使用自动发现替代手动 topic 配置。
