<p align="center">
  <img src="docs/public/logo-full-animated.svg" alt="CoreC" width="420">
</p>

# CoreC (Connect · Collect · Control)

[![Go Version](https://img.shields.io/badge/Go-1.27.1-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![golangci-lint](https://github.com/CoreC-Dev/CoreC/actions/workflows/ci.yml/badge.svg)](https://github.com/CoreC-Dev/CoreC/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**CoreC** 是一个面向工业物联网 (IIoT) 与边缘计算的**高性能、配置驱动、插件化数据采集与控制核心**。

> **CoreC = Connect · Collect · Control** —— 设备连接管理、工业数据采集、控制指令下发，三位一体。

项目采用六边形可插拔架构，将 `Inbound -> Rule -> Outbound` 的数据流概念映射为工业领域的 **`南向驱动 (Driver) -> 核心引擎与规则路由 (Engine & Rules) -> 北向传输 (Transport)`**，并支持反向控制指令下发。

```text
   ____                  ____ 
  / ___|___  _ __ ___   / ___|
 | |   / _ \| '__/ _ \ | |    
 | |__| (_) | | |  __/ | |___ 
  \____\___/|_|  \___|  \____| {version}

  IIoT Data Collection and Distribution Core
```

---

## 核心特性

- **纯核心定位 (Headless & Embeddable)**: 无任何 UI 强绑定，轻量级单二进制，既可作为独立 Daemon 运行，也能轻松嵌入上层 SCADA、MES 或边缘网关系统。
- **六边形插件化架构**: 统一抽象的 `core.Driver`（南向）与 `core.Transport`（北向）接口，支持 Go `init()` 自动工厂注册模式。
- **配置即逻辑 (YAML-Driven)**: 三段式 YAML 配置，统一管理驱动、点位、传输与路由规则。支持 `tags-file` 将数百采集点拆分到独立文件，配合 `tags-interval` 实现标签热重载。
- **双向数据通道 (Bidirectional)**:
  - **上行采集**: 周期定时轮询调度 (Scheduler) 与事件驱动主动订阅 (Subscription)。
  - **下行控制**: 北向传输（如 MQTT Command Topic）接收控制指令，反向调度驱动写入 PLC 寄存器/线圈。
- **生产级容错与调度**:
  - **断线重连与指数退避**: 设备离线自动后台重连，重连成功后自动恢复正常采集。
  - **断路器保护**: 驱动连续重连失败达到阈值后触发断路器，将重连间隔提升至 5 分钟低频重试，避免无效重连消耗资源。
  - **高频错误抑制**: 针对 200ms 等高频采集任务在断网时的刷屏问题，提供 10s 窗口限频与发生计数聚合。
  - **调度器自动降级**: 采集任务连续失败时自动放慢采集频率（10 倍间隔），恢复后自动还原。
  - **离线持久化缓冲**: 传输不可达时数据落盘缓存，传输恢复后自动回放，防止数据丢失。
  - **坏质量数据策略**: 可配置 `QualityBad` 数据的处理方式（发布/丢弃/标记/告警）。
  - **数据陈旧检测**: 缓存值超过阈值未更新时 API 响应标记 `is_stale`，帮助消费者识别断连停滞。
  - **写入重试与死信队列**: 控制指令下发失败自动重试，耗尽后进入死信队列供查询和手动重试。
  - **传输故障降级**: 配置 `fallback` 备用传输，主传输失败时自动切换。
  - **毫秒级优雅退出**: 信号中断即时捕获，倒序清理协程与网络连接，零资源泄露。
- **External Controller RESTful API**: 提供完整的控制面 API，支持配置热重载、规则命中统计与运行时禁用、实时日志/数据/流量 WebSocket 流。
- **生产级可观测性**: Prometheus `/metrics` 端点（计数器/仪表盘指标，Prometheus 文本格式）、pprof 性能分析端点（CPU/堆/goroutine）、分布式追踪（trace ID 传播 + span 计时）。
- **传输层加密**: MQTT 支持 TLS/mTLS（`mqtts://` + 客户端证书 + CA 验证），API 支持 HTTPS。

---

## 架构映射

| CoreC 组件 | 说明 |
|:---|:---|
| `Driver` (南向驱动) | 支持 Modbus 全家族 (TCP/RTU/UDP/TLS), Siemens S7, OPC UA 等 |
| `Transport` (北向传输) | 支持 MQTT、HTTP Push/Webhook（Kafka、gRPC 规划中） |
| `Engine` (核心引擎) | 数据总线消费、生命周期管理、双向指令调度 |
| `Scheduler` (主动调度器) | 按点位采集周期主动触发读取 |
| `Rule` (规则引擎) | 优先级规则链匹配，支持 `forward`, `drop`, `alert`, `mirror`, `transform` |
| `DataPoint` (统一数据点) | 包含测点名、类型、时间戳、质量戳 (Quality)、值 |

---

## 支持协议

### 1. 南向驱动 (Southbound Drivers)

- **Modbus TCP** (`driver/modbus/`):
  - 支持 `0xxxx` (线圈)、`1xxxx` (离散输入)、`3xxxx` (输入寄存器)、`4xxxx` (保持寄存器)。
  - 支持多种数据类型（`bool`, `uint16`, `int16`, `uint32`, `int32`, `uint64`, `int64`, `float32`, `float64`）。
  - 支持保持寄存器与线圈的下发写入。
- **Modbus RTU** (`driver/modbus/`):
  - 通过串口（RS-232 / RS-485）连接 Modbus RTU 设备，支持与 TCP 相同的四种寄存器区与数据类型。
  - 可配置波特率（`baud-rate`，默认 9600）、数据位（`data-bits`，默认 8）、校验位（`parity`：`none`/`even`/`odd`，默认 `none`）、停止位（`stop-bits`，默认自动）。
  - 支持保持寄存器与线圈的下发写入。
- **Modbus RTU over TCP** (`modbus-rtuovertcp`)：RTU 帧封装在 TCP 连接中，适用于串口转以太网网关场景。
- **Modbus UDP** (`modbus-udp`)：Modbus TCP over UDP，适用于对实时性要求高、可容忍丢包的场景。
- **Modbus RTU over UDP** (`modbus-rtuoverudp`)：RTU 帧封装在 UDP 中。
- **Modbus TCP over TLS** (`modbus-tls`)：加密版 Modbus TCP，支持双向 TLS 认证（mTLS），需配置 `cert-file`、`key-file`、`ca-file`。
- **Siemens S7** (`driver/s7/`):
  - 支持 S7-200/300/400/1200/1500。
  - 支持 DB 块（如 `DB1.DBD0` 浮点、`DB1.DBW4` 字、`DB1.DBX0.0` 位）、Merker(M)、Inputs(I/E)、Outputs(Q/A)。
  - 支持位级读-改-写（Read-Modify-Write）及各种数据块写入下发。
- **OPC UA** (`driver/opcua/`):
  - 支持标准 NodeID 解析（如 `ns=2;s=Conveyor.Speed`, `ns=1;i=1001`）。
  - 支持轮询读取 (Polling) 与事件订阅 (Subscription)。
  - 支持测点值写入下发。

### 2. 北向传输 (Northbound Transports)

- **MQTT** (`transport/mqtt/`):
  - 支持 QoS 0/1/2、Retain、动态主题模板 (`factory/{{.Driver}}/{{.Group}}/{{.Tag}}`)。
  - 支持 Command Topic 反向订阅接收下发控制指令。
- **HTTP Push** (`transport/httppush/`):
  - 原生连接池复用，支持批量 JSON Push、自定义 Headers / Auth Token。

---

## 快速上手

### 1. 编译构建
```bash
git clone https://github.com/CoreC-Dev/CoreC.git
cd CoreC
go build -o corec.exe ./cmd/corec
```

### 2. 运行测试
```bash
go test -v ./...
```

### 3. 配置文件示例
参考 [`config.example.yaml`](config.example.yaml)：

```yaml
global:
  log-level: info

drivers:
  - name: plc-modbus
    type: modbus-tcp
    settings:
      host: 192.168.1.100
      port: 502
      slave-id: 1
    tags:
      - name: temperature
        address: "40001"
        type: float32
        group: sensors
        interval: 1s

transports:
  - name: cloud-mqtt
    type: mqtt
    settings:
      broker: tcp://broker.emqx.io:1883
      topic-template: "factory/{{.Driver}}/{{.Group}}/{{.Tag}}"
      command-topic: "factory/commands/#"

rules:
  - name: high-temp-alert
    match: "tag == 'temperature' && value > 90"
    action: alert
    target: cloud-mqtt
    priority: 1
  - name: default-catch-all
    match: "ALL"
    action: forward
    target: cloud-mqtt
    priority: 999
```

### 4. 启动核心
```bash
./corec.exe -c config.example.yaml
```

---

## External Controller API

CoreC 内置 RESTful 控制面 API。在配置文件中启用：

```yaml
global:
  api:
    listen: 0.0.0.0:9090
    secret: your-secret-token
```

### 认证

所有受保护端点需要 Bearer Token 认证（WebSocket 支持 `?token=` 查询参数）：

```bash
curl -H "Authorization: Bearer your-secret-token" http://localhost:9090/drivers
```

### REST 端点

| 方法 | 路径 | 说明 |
|:---|:---|:---|
| GET | `/` | 健康检查 |
| GET | `/version` | 版本信息 |
| GET | `/configs` | 当前配置概要 |
| PUT | `/configs` | 热重载配置（`{"path":"config.yaml"}` 或 `{"payload":"<yaml>"}` ） |
| PATCH | `/configs` | 修改运行时设置（如 `{"log-level":"debug"}` ） |
| GET | `/drivers` | 列出所有南向驱动及状态 |
| GET | `/drivers/{name}` | 查看单个驱动 |
| GET | `/drivers/{name}/tags` | 查看驱动下所有测点最新值 |
| GET | `/transports` | 列出所有北向传输及状态 |
| GET | `/transports/{name}` | 查看单个传输 |
| GET | `/tags` | 所有测点最新缓存值 |
| POST | `/write` | 下发控制指令 `{"driver":"plc1","tag":"temp","value":50}` |
| GET | `/write/failed` | 查询死信队列中重试耗尽的失败写入指令 |
| GET | `/rules` | 列出规则 + **命中/未命中统计** |
| PATCH | `/rules/disable` | 运行时启用/禁用规则 `{"index":0,"disabled":true}` |
| GET | `/stats` | 引擎运行统计 |

### WebSocket 端点

| 路径 | 说明 |
|:---|:---|
| `/logs` | 实时日志流（捕获所有 `slog` 调用） |
| `/traffic` | 实时吞吐量统计（每秒推送） |
| `/tags/stream` | 实时数据点流（`?driver=xxx` 可过滤） |
| `/memory` | 实时内存使用与 GC 统计 |

### CORS

API 服务器默认启用 CORS（`Access-Control-Allow-Origin: *`），支持浏览器 Dashboard 直接访问。

---

## AI 开发与维护交接

详细的代码架构解析、扩展编写指南及后续演进规划，请参阅：[AI 交接文档 (AI_HANDOVER.md)](AI_HANDOVER.md)。

---

## 许可证

本项目以 **MIT 许可证** 开源：允许商用、修改与再分发，但**必须保留版权与许可声明**，且作者**不承担任何担保与责任**（"AS IS"）。

```text
MIT License
Copyright (c) 2026 CoreC Contributors
```

完整条款见 [LICENSE](LICENSE)。

### 第三方依赖许可证

本项目依赖以下开源组件，其许可证如下：

| 依赖 | 版本 | 许可证 |
|:---|:---|:---|
| [expr-lang/expr](https://github.com/expr-lang/expr) | v1.17.8 | MIT |
| [tidwall/gjson](https://github.com/tidwall/gjson) | v1.19.0 | MIT |
| [coder/websocket](https://github.com/coder/websocket) | v1.8.15 | ISC |
| [eclipse/paho.mqtt.golang](https://github.com/eclipse/paho.mqtt.golang) | v1.5.1 | EPL-2.0 / EDL-1.0 |
| [go-chi/chi/v5](https://github.com/go-chi/chi) | v5.3.2 | MIT |
| [goccy/go-yaml](https://github.com/goccy/go-yaml) | v1.19.2 | MIT |
| [gopcua/opcua](https://github.com/gopcua/opcua) | v0.9.1 | MIT |
| [robinson/gos7](https://github.com/robinson/gos7) | v0.0.0-20260622162611-2d6806f80c8b | MIT |
| [simonvetter/modbus](https://github.com/simonvetter/modbus) | v1.6.4 | MIT |
| [goburrow/serial](https://github.com/goburrow/serial) *(间接)* | v0.1.0 | MIT |
| [tidwall/match](https://github.com/tidwall/match) *(间接)* | v1.1.1 | MIT |
| [tidwall/pretty](https://github.com/tidwall/pretty) *(间接)* | v1.2.0 | MIT |
| [gorilla/websocket](https://github.com/gorilla/websocket) *(间接)* | v1.5.3 | BSD-3-Clause |
| [golang.org/x/net](https://golang.org/x/net) *(间接)* | v0.59.0 | BSD-3-Clause |
| [golang.org/x/sync](https://golang.org/x/sync) *(间接)* | v0.23.0 | BSD-3-Clause |
