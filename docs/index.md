---
layout: home

hero:
  name: "CoreC"
  text: "工业物联网\n数据采集分发核心"
  tagline: 连接 PLC 与工业设备，采集数据经规则引擎处理后分发至 MQTT / HTTP，支持云端反向控制与实时监控。
  image:
    src: /logo-animated.svg
    alt: CoreC
    width: 320
  actions:
    - theme: brand
      text: 简介
      link: /guide/introduction
    - theme: alt
      text: 快速上手
      link: /guide/quickstart
    - theme: alt
      text: GitHub
      link: https://github.com/CoreC-Dev/CoreC

features:
  - icon: 🔌
    title: 多协议设备接入
    details: Modbus 全系列（TCP / RTU / UDP / TLS）、Siemens S7 与 OPC UA（支持订阅）统一为同一套驱动配置，单实例可同时连接多台设备。
  - icon: ⚙️
    title: 规则引擎
    details: 优先级规则链按表达式决定每条数据的去向——转发、丢弃、告警、变换或镜像复制，全部在 YAML 中声明，无需写代码。
  - icon: 📡
    title: 北向数据分发
    details: 采集数据推送 MQTT 或 HTTP；断网期间自动落盘缓冲，连接恢复后按序重发，数据不丢失。
  - icon: 🔄
    title: 云端反向控制
    details: 通过 MQTT 命令主题接收云端下发的写指令，写入 PLC 寄存器或线圈；控制指令与上行采集互不阻塞。
  - icon: 📊
    title: 实时可观测
    details: REST API 与 WebSocket 实时查看数据流、日志、流量与内存；内置 Prometheus 指标与 Kubernetes 存活 / 就绪探针。
  - icon: 🖥️
    title: 部署与配套界面
    details: 单二进制、一份 YAML 描述完整采集拓扑，可独立运行或作为 Go 库嵌入；官方 Dashboard（独立仓库）提供多实例监控、配置中心与规则管理。
---
