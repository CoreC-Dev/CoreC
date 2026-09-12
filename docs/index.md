---
layout: home

hero:
  name: "CoreC"
  text: "工业物联网\n数据采集分发核心"
  tagline: 六边形插件架构 · Go 1.27.1 · 高性能
  image:
    src: /logo-animated.svg
    alt: CoreC
    width: 320
  actions:
    - theme: brand
      text: 快速开始
      link: /guide/quickstart
    - theme: alt
      text: 架构设计
      link: /architecture/overview
    - theme: alt
      text: 配置参考
      link: /config/global

features:
  - icon: ⚡
    title: 高性能管道
    details: Channel 驱动的无锁数据流，8192 缓冲深度，drop-oldest 背压策略，采集 goroutine 永不阻塞。
  - icon: 🔌
    title: 多协议驱动
    details: 支持 Modbus 全系列（TCP/RTU/UDP/TLS）、Siemens S7、OPC UA 三大工业协议，统一 Driver 接口，按 interval 分组批量读取。
  - icon: 📡
    title: 多目标传输
    details: MQTT 异步发布 + HTTP 连接池推送，支持 batch 和 flush-interval，topic 模板渲染。
  - icon: 🛡️
    title: 规则引擎
    details: 优先级匹配，支持 forward、drop、alert、transform、mirror 五种动作，表达式条件过滤。
  - icon: 🔄
    title: 反向控制
    details: MQTT command topic 接收云端下发指令，Command Loop 转发至设备 Write，不阻塞上行管道。
  - icon: 📊
    title: 实时监控
    details: RESTful API + WebSocket 实时流，LatestCache RWMutex 保护，原子计数器零开销统计。
---
