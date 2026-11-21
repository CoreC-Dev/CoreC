<p align="center">
  <img src="docs/public/logo-full-animated.svg" alt="CoreC" width="420">
</p>

<p align="center">
  <a href="https://github.com/CoreC-Dev/CoreC/actions/workflows/ci.yml"><img src="https://github.com/CoreC-Dev/CoreC/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License: MIT"></a>
</p>

**CoreC** 是一个面向工业物联网 (IIoT) 与边缘计算的**高性能、配置驱动、插件化数据采集与控制核心**。

> **CoreC = Connect · Collect · Control** —— 设备连接管理、工业数据采集、控制指令下发，三位一体。

项目采用六边形可插拔架构：**南向驱动 (Driver) → 核心引擎与规则路由 (Engine & Rules) → 北向传输 (Transport)**，并支持反向控制指令下发。

## 核心特性

- **纯核心定位**：无 UI 强绑定，轻量级单二进制，可独立运行或嵌入 SCADA/MES/边缘网关系统。
- **插件化架构**：统一的 `core.Driver` / `core.Transport` 接口，`init()` 工厂注册。
- **配置即逻辑**：三段式 YAML 统一管理驱动、点位、传输与规则；`tags-file` 标签热重载；`${ENV_VAR}` 环境变量替换。
- **双向数据通道**：上行周期轮询与事件订阅；下行控制指令写入设备；命令跨实例多跳透传。
- **生产级容错**：断线重连与断路器、调度自动降级、离线持久化缓冲、写入重试与死信队列、传输故障降级、优雅关停。
- **控制面 API**：RESTful + WebSocket——配置热重载与备份/恢复、资源管理、批量写、一等告警、实时数据/日志/告警流。
- **可观测性**：Prometheus `/metrics`（按驱动/传输拆分的指标族）、pprof、W3C Trace Context 请求追踪。
- **传输层加密**：MQTT TLS/mTLS、API HTTPS。

## 支持协议

- **南向驱动**：Modbus 全家族（TCP / RTU / UDP / TLS / RTU over TCP/UDP）、Siemens S7、OPC UA（轮询 + 订阅）。
- **北向传输**：MQTT（QoS 0/1/2、主题模板、命令订阅）、HTTP Push/Webhook、Notify 告警通知（Slack / Teams / Email / Webhook）。

详见 [驱动指南](docs/guide/drivers.md) 与 [传输指南](docs/guide/transports.md)。

## 快速上手

```bash
git clone https://github.com/CoreC-Dev/CoreC.git
cd CoreC
go build -o corec ./cmd/corec
COREC_API_SECRET=change-me-please ./corec -c config.example.yaml
```

最小配置示例见 [`config.example.yaml`](config.example.yaml)；完整步骤见 [快速上手指南](docs/guide/quickstart.md)，构建与部署方式见 [安装指南](docs/guide/installation.md)。

## 文档

- [文档首页](docs/index.md) · [简介](docs/guide/introduction.md) · [架构总览](ARCHITECTURE.md)
- [配置参考](docs/config/example.md) · [API 参考](docs/api/overview.md)（REST/WebSocket 端点索引与认证）
- [数据流](docs/guide/data-flow.md) · [规则](docs/guide/rules.md) · [可观测性](docs/guide/observability.md) · [级联部署](docs/guide/chained-core.md)
- [MCP 适配器](docs/guide/mcp.md) · [安全姿态](SECURITY.md) · [质量门禁](docs/gates.md) · [贡献指南](CONTRIBUTING.md)

配套前端：[CoreC-Dashboard](https://github.com/CoreC-Dev/CoreC-Dashboard)（多实例监控、配置中心、规则管理与写入控制）。

## 许可证

本项目以 [MIT 许可证](LICENSE) 开源。第三方依赖许可证清单见 [docs/license.md](docs/license.md)。