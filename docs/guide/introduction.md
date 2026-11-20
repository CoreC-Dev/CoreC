---
title: 介绍
description: CoreC 是什么 —— 面向工业物联网的高性能、配置驱动、插件化数据采集与控制核心
---

# 介绍

## CoreC 是什么

**CoreC** 是一个面向工业物联网（IIoT）与边缘计算的**高性能、配置驱动、插件化数据采集与控制核心**。它使用 Go 1.27.3 编写（见 `go.mod`），以单二进制形式运行，既可作为独立守护进程部署在边缘网关，也能作为 Go 库嵌入上层 SCADA、MES 或边缘计算平台。

> **CoreC = Connect · Collect · Control** —— 控制指令下发、设备连接管理、工业数据采集，三位一体。

项目采用六边形可插拔架构，将工业数据采集建模为：

```
南向驱动 (Driver) → 核心引擎与规则路由 (Engine & Rules) → 北向传输 (Transport)
```

并支持反向控制指令下发，实现真正的**双向数据通道**。

> 版本号由构建时 ldflags 注入（`-X main.version=x.y.z`）：发布版显示对应 git tag 的版本，未注入时默认显示 `dev`，可用 `-version` 参数直接打印。

## 核心特性

### 纯核心定位（Headless & Embeddable）

无任何 UI 强绑定，轻量级单二进制。所有运行时行为由一份 YAML 配置文件驱动，通过 RESTful API 与 WebSocket 实现完全的可观测性与控制面操作。

### 六边形插件化架构

统一抽象的 `core.Driver`（南向）与 `core.Transport`（北向）接口，采用 Go `init()` 自动工厂注册模式。新增协议支持只需实现接口并在 `init()` 中注册，无需修改核心代码：

```go
func init() {
    core.RegisterDriver("my-protocol", NewMyDriver)
}
```

### 配置即逻辑（YAML-Driven）

三段式 YAML 配置，统一管理驱动、点位、传输与路由规则。一份配置文件即可描述完整的采集拓扑：

```yaml
drivers:        # 南向 —— 连接哪些设备
  - name: plc
    type: modbus-tcp
    # ...

transports:     # 北向 —— 数据发往哪里
  - name: cloud
    type: mqtt
    # ...

rules:          # 路由 —— 哪些数据走哪条路
  - match: "tag == 'temperature' && value > 95"
    action: alert
    target: cloud
```

### 双向数据通道（Bidirectional）

- **上行采集**：调度器按点位采集周期轮询（Scheduler）；OPC UA 驱动额外实现 `Subscribe()` 服务端订阅（供把 CoreC 作为 Go 库嵌入的宿主消费，见[驱动](./drivers.md#opc-ua-opcua)）。
- **下行控制**：北向传输（如 MQTT Command Topic）接收控制指令，反向调度驱动写入 PLC 寄存器/线圈，形成闭环控制。

### 生产级容错与调度

- **断线重连与指数退避**：设备离线自动后台重连，重连成功后自动恢复正常采集，退避上限 30 秒。
- **高频错误抑制**：针对 200ms 等高频采集任务在断网时的刷屏问题，提供 10 秒窗口限频与发生计数聚合。
- **调度超时保护**：每次读取以采集周期为超时上限，避免慢设备拖垮整体调度；测点可单独设置 `read-timeout`（如 `500ms`）覆盖该默认值。
- **断路器**：连续失败达到阈值后熔断，进入 5 分钟低频重试，避免持续冲击故障设备。
- **离线缓冲**：发布失败时将数据落盘持久化，连接恢复后自动重发，减少数据丢失。
- **死信队列（DLQ）**：多次重试仍不可恢复的数据进入死信队列，可通过 `/write/failed` 查询。
- **优雅降级（坏质量策略）**：对坏质量数据点支持 `publish`（默认）/ 丢弃 `drop` / 标记 `mark-and-publish` / 告警 `alert` 四种策略（`global.engine.on-bad-quality`），避免污染下游。
- **优雅退出**：shutdown 信号即时捕获，先停 API 服务再按依赖顺序停止引擎组件，每个组件的停止受 `shutdown-timeout`（默认 30s）约束；退出过程中再次收到信号会强制退出；`goleak` 测试保证停机后无协程泄露。

### 控制面 RESTful API

提供完整的控制面 API（chi 路由器，需在配置中显式设置 `api.listen`，不设置则不启动 API 服务；Bearer Token 鉴权），支持：

- 配置热重载（`PUT /configs`）、局部补丁（`PATCH /configs`）、服务端配置备份与一键恢复（`/configs/backups`——每次覆盖前自动快照，详见 [API 文档](../api/config.md)）
- 驱动 / 传输 / 规则运行时查询与规则禁用
- 实时日志、数据流、流量与内存监控的 WebSocket 推送（`/tags/stream`、`/logs`、`/traffic`、`/memory`）
- 即时读写点位（`POST /write`）、最新值缓存查询（`/tags`）、写入审计历史（`GET /write/history`）、驱动连通性预检（`POST /drivers/test`）、规则表达式试算（`POST /rules/preview`）

## 核心组件

CoreC 由六个核心组件构成，各司其职：

| 组件 | 职责 |
|:---|:---|
| `Driver`（南向驱动） | 连接工业设备并读写数据，支持 Modbus、Siemens S7、OPC UA 等协议 |
| `Transport`（北向传输） | 将数据点发往外部系统，支持 MQTT、HTTP Push/Webhook、以及 Slack/Teams/Email/Webhook 告警通知（notify）|
| `Engine`（核心引擎） | 数据总线消费、生命周期管理、双向指令调度 |
| `Scheduler`（主动调度器） | 按点位采集周期主动触发读取 |
| `Rule`（规则引擎） | 优先级规则链匹配，`forward` / `drop` / `alert` / `transform` / `mirror` |
| `DataPoint`（统一数据点） | 含驱动、分组、测点名、类型、时间戳、质量戳、值 |

## 支持协议

### 南向驱动（Southbound Drivers）

| 驱动 | 类型名 | 读取 | 写入 | 订阅 | 批量读 | 最大批量 |
|:---|:---|:---:|:---:|:---:|:---:|:---:|
| Modbus TCP | `modbus-tcp` | ✅ | ✅ | ❌ | ✅ | 125 |
| Modbus RTU | `modbus-rtu` | ✅ | ✅ | ❌ | ✅ | 125 |
| Modbus RTU over TCP | `modbus-rtuovertcp` | ✅ | ✅ | ❌ | ✅ | 125 |
| Modbus UDP | `modbus-udp` | ✅ | ✅ | ❌ | ✅ | 125 |
| Modbus RTU over UDP | `modbus-rtuoverudp` | ✅ | ✅ | ❌ | ✅ | 125 |
| Modbus TLS | `modbus-tls` | ✅ | ✅ | ❌ | ✅ | 125 |
| Siemens S7 | `s7` | ✅ | ✅ | ❌ | ✅ | 220 |
| OPC UA | `opcua` | ✅ | ✅ | ✅ | ✅ | 1000 |

### 北向传输（Northbound Transports）

| 传输 | 类型名 | 特性 |
|:---|:---|:---|
| MQTT | `mqtt` | paho 异步客户端、QoS 0/1/2、Retain、动态主题模板、Command Topic 反向下发（HMAC-SHA256 认证） |
| HTTP Push | `http` | 原生连接池复用、批量 JSON Push、自定义 Headers / Auth Token、Webhook 入站 |
| Notify | `notify` | 告警通知通道（Slack / Teams / Email / 通用 Webhook），按规则触发、冷却去重，单向通知 |

## 下一步

- [安装](./installation.md) —— 从源码构建或获取预编译二进制
- [快速上手](./quickstart.md) —— 5 分钟跑通第一个数据点
- [数据流](./data-flow.md) —— 理解从设备到云端的数据全链路
