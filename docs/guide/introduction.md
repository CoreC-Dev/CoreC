---
title: 介绍
description: CoreC 是什么 —— 面向工业物联网的高性能、配置驱动、插件化数据采集与控制核心
---

# 介绍

## CoreC 是什么

**CoreC** 是一个面向工业物联网（IIoT）与边缘计算的**高性能、配置驱动、插件化数据采集与控制核心**。它使用 Go 1.27.1 编写，以单二进制形式运行，既可作为独立守护进程部署在边缘网关，也能作为 Go 库嵌入上层 SCADA、MES 或边缘计算平台。

> **CoreC = Connect · Collect · Control** —— 控制指令下发、设备连接管理、工业数据采集，三位一体。

项目采用六边形可插拔架构，将工业数据采集建模为：

```
南向驱动 (Driver) → 核心引擎与规则路由 (Engine & Rules) → 北向传输 (Transport)
```

并支持反向控制指令下发，实现真正的**双向数据通道**。

```
   ____                  ____
  / ___|___  _ __ ___   / ___|
 | |   / _ \| '__/ _ \ | |
 | |__| (_) | | |  __/ | |___
  \____\___/|_|  \___|  \____| {version}

  IIoT Data Collection and Distribution Core
```

> 版本号由构建时 ldflags 注入（`-X main.version=x.y.z`）：发布版显示 `v0.0.5`，未注入时默认显示 `dev`。

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

- **上行采集**：周期定时轮询调度（Scheduler）与事件驱动主动订阅（Subscription，OPC UA 支持）。
- **下行控制**：北向传输（如 MQTT Command Topic）接收控制指令，反向调度驱动写入 PLC 寄存器/线圈，形成闭环控制。

### 生产级容错与调度

- **断线重连与指数退避**：设备离线自动后台重连，重连成功后自动恢复正常采集，退避上限 30 秒。
- **高频错误抑制**：针对 200ms 等高频采集任务在断网时的刷屏问题，提供 10 秒窗口限频与发生计数聚合。
- **调度超时保护**：每次读取以采集周期为超时上限，避免慢设备拖垮整体调度；测点可单独设置 `read-timeout`（如 `500ms`）覆盖该默认值。
- **断路器**：连续失败达到阈值后熔断，进入 5 分钟低频重试，避免持续冲击故障设备。
- **离线缓冲**：发布失败时将数据落盘持久化，连接恢复后自动重发，防止数据丢失。
- **死信队列（DLQ）**：多次重试仍不可恢复的数据进入死信队列，可通过 `/write/failed` 查询。
- **优雅降级（坏质量策略）**：对坏质量数据点支持丢弃 / 标记 / 告警等策略，避免污染下游。
- **毫秒级优雅退出**：信号中断即时捕获，倒序清理协程与网络连接，零资源泄露。

### 控制面 RESTful API

提供完整的控制面 API（chi 路由器，需在配置中显式设置 `api.listen`，不设置则不启动 API 服务；Bearer Token 鉴权），支持：

- 配置热重载（`PUT /configs`）、局部补丁（`PATCH /configs`）
- 驱动 / 传输 / 规则运行时查询与规则禁用
- 实时日志、数据流、流量与内存监控的 WebSocket 推送（`/tags/stream`、`/logs`、`/traffic`、`/memory`）
- 即时读写点位（`POST /write`）、最新值缓存查询（`/tags`）

## 核心组件

CoreC 由六个核心组件构成，各司其职：

| 组件 | 职责 |
|:---|:---|
| `Driver`（南向驱动） | 连接工业设备并读写数据，支持 Modbus、Siemens S7、OPC UA 等协议 |
| `Transport`（北向传输） | 将数据点发往外部系统，支持 MQTT、HTTP Push/Webhook 等 |
| `Engine`（核心引擎） | 数据总线消费、生命周期管理、双向指令调度 |
| `Scheduler`（主动调度器） | 按点位采集周期主动触发读取，工业采集独有 |
| `Rule`（规则引擎） | 优先级规则链匹配，`forward` / `drop` / `alert` / `transform` / `mirror` |
| `DataPoint`（统一数据点） | 含驱动、分组、测点名、类型、时间戳、质量戳、值 |

## 支持协议

### 南向驱动（Southbound Drivers）

| 驱动 | 类型名 | 读取 | 写入 | 订阅 | 批量读 | 最大批量 |
|:---|:---|:---:|:---:|:---:|:---:|:---:|
| Modbus TCP | `modbus-tcp` | ✅ | ✅ | ❌ | ✅ | 125 |
| Siemens S7 | `s7` | ✅ | ✅ | ❌ | ✅ | 220 |
| OPC UA | `opcua` | ✅ | ✅ | ✅ | ✅ | 1000 |

### 北向传输（Northbound Transports）

| 传输 | 类型名 | 特性 |
|:---|:---|:---|
| MQTT | `mqtt` | paho 异步客户端、QoS 0/1/2、Retain、动态主题模板、Command Topic 反向下发 |
| HTTP Push | `http` | 原生连接池复用、批量 JSON Push、自定义 Headers / Auth Token |

## 设计哲学

### 1. 核心与协议解耦

CoreC 只定义接口，不绑定任何具体协议。`core.Driver` 与 `core.Transport` 是整个系统的两个正交抽象面，所有协议以插件形式存在。这意味着：

- 新增一种工业协议，**不需要修改引擎代码**。
- 同一份核心可以同时连接 Modbus PLC、Siemens S7 PLC 和 OPC UA 服务器。
- 协议插件可以独立测试、独立版本化。

### 2. 通道优先，锁为辅

核心数据通路使用 Go channel（`DataBus` 容量 8192）实现无锁数据流转，仅在配置变更、缓存查询等低频路径使用 `sync.RWMutex`。这保证了高吞吐场景下采集协程与处理协程之间的零竞争数据传递。

### 3. 背压与降级而非阻塞

当系统过载时，CoreC 选择**丢弃最旧数据**（drop-oldest）而非阻塞采集协程——工业场景中过时的数据往往已无价值，阻塞采集反而会导致调度超时雪崩。被丢弃的数量经 API `/stats` 的 `TotalDropped` 暴露（含总线满丢弃 `PushDropped()`、慢订阅者跳过 `Dropped()` 与日志环缓冲丢弃），便于运维感知。

### 4. 可观测性一等公民

每一个组件（驱动、传输、规则、调度器、引擎）都暴露结构化的运行时状态与计数器，并通过统一的 API 与 WebSocket 流对外提供。规则命中统计（`HitCount` / `MissCount`）记录每条规则的命中与未命中次数，便于验证规则是否如预期工作。

## 与同类工具对比

| 特性 | CoreC | Telegraf | Node-RED | Fluent Bit | Ignition Edge |
|:---|:---:|:---:|:---:|:---:|:---:|
| 定位 | 采集控制核心 | 采集代理 | 可视化流编排 | 日志/数据处理器 | 工业边缘平台 |
| 语言 | Go | Go | Node.js | C | Java |
| 架构模式 | 六边形插件 | 插件管道 | 节点流 | 插件管道 | 商业平台 |
| 配置方式 | 单 YAML | TOML 多文件 | 可视化 UI | YAML/JSON | UI 向导 |
| 规则路由引擎 | ✅ 优先级链 | ❌ | ✅ 流逻辑 | ✅ 路由 | ✅ 标签路由 |
| 反向控制下发 | ✅ | ❌ | ✅ | ❌ | ✅ |
| 嵌入式使用 | ✅ Go 库 | ❌ | ❌ | ✅ | ❌ |
| 工业协议 | Modbus/S7/OPC UA | Modbus/OPC UA | 社区节点 | 有限 | 全套 |
| 二进制体积 | ~16 MB | ~50 MB | ~150 MB | ~2 MB | ~1 GB |
| 运行时 API | ✅ REST + WS | ❌ | ✅ HTTP | ✅ | ✅ |
| 商业授权 | MIT 开源 | MIT 开源 | JS 开源 | Apache 2.0 | 商业付费 |

::: info 选型建议
- 需要轻量、可嵌入、支持反向控制下发的边缘网关核心 → **CoreC**
- 需要丰富生态插件、纯采集转发 → Telegraf
- 需要可视化拖拽编排、快速原型 → Node-RED
- 需要完整 SCADA 功能与商业支持 → Ignition Edge
:::

## 下一步

- [安装](./installation.md) —— 从源码构建或获取预编译二进制
- [快速上手](./quickstart.md) —— 5 分钟跑通第一个数据点
- [数据流](./data-flow.md) —— 理解从设备到云端的数据全链路
