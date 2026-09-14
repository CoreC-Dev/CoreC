---
title: 完整示例
description: CoreC 配置文件完整带注释示例，涵盖全局、驱动、传输、规则四大段。
---

# 完整示例

本页给出一份可直接使用的 CoreC 配置文件，每个段落附带注释说明。可将其作为 `config.yaml` 启动核心：

```bash
./corec -f config.yaml
```

::: tip
完整文件见仓库根目录 [`config.example.yaml`](https://github.com/CoreC-Dev/CoreC/blob/main/config.example.yaml)。本页在其基础上补充了逐段讲解。
:::

---

## 配置总览

CoreC 配置采用 YAML，顶层段如下：

| 段 | 作用 | 必填 | 对应文档 |
| --- | --- | --- | --- |
| `node` | 拓扑自动发现（节点身份、上游订阅） | 否 | [链式核心 → 自动发现](/guide/chained-core#拓扑自动发现-auto-discovery) |
| `global` | 核心全局设置（日志、API、引擎调优） | 否 | [全局配置](/config/global) |
| `drivers` | 南向采集驱动列表 | 条件必填 | [驱动配置](/config/drivers) |
| `transports` | 北向传输通道列表 | **是** | [传输配置](/config/transports) |
| `rules` | 数据路由与处理规则 | 否 | [规则配置](/config/rules) |

::: info
`node` 段是可选的。省略时所有自动发现功能关闭，引擎行为与旧版完全一致。设置 `node.id` 后，`topic-template`、`command-topic`、`parser` 等字段在省略时自动生成。
:::

---

## 完整配置文件

```yaml
# ============================================================
# CoreC 配置示例
# 工业数据采集核心
# ============================================================

# ---------- 全局设置 ----------
global:
  log-level: info                    # 日志级别：debug/info/warn/error/fatal
  api:
    listen: 0.0.0.0:9090             # 管理 API 监听地址（未设置则不启用 API）
    secret: "corec-secret-token"     # 鉴权令牌（listen 设置时必填，至少 8 字符）
  engine:
    data-bus-size: 8192              # 内部数据通道容量
    workers: 4                       # 处理协程数（0 = NumCPU）
    shutdown-timeout: 30s            # 优雅关停超时
    error-throttle-window: 10s       # 错误日志抑制窗口
    default-tag-interval: 1s         # 标签未设 interval 时的回退周期
    on-bad-quality: mark-and-publish # 坏质量数据处理策略
    stale-threshold: 30s             # 数据陈旧判定阈值
    write-retry-count: 3             # 写入指令重试次数
  buffer:                            # 离线持久化缓冲（可选）
    enabled: false
    path: /var/lib/corec/buffer      # 缓冲文件目录（enabled=true 时必填）
    max-size: 10000                  # 缓冲文件最大数量

# ============================================================
# 南向驱动
# 支持: modbus-tcp, modbus-rtu, modbus-rtuovertcp, modbus-udp,
#       modbus-rtuoverudp, modbus-tls, s7, opcua
# ============================================================
drivers:
  # ---------- 1. Modbus TCP 驱动 (PLC / 传感器网关) ----------
  - name: plc-modbus                 # 驱动实例名，全局唯一
    type: modbus-tcp                 # 协议类型
    settings:
      host: 192.168.1.100            # 设备 IP
      port: 502                      # Modbus 标准端口
      slave-id: 1                    # 从站地址 (1-247)
      timeout: 3s                    # 读写超时
      retry: 3                       # 失败重试次数
    tags:
      - name: temperature            # 标签名
        address: "40001"             # 保持寄存器 0
        type: float32                # 数据类型
        group: sensors               # 分组
        interval: 1s                 # 采集周期
        deadband: 0.2                # 死区过滤，变化 <0.2 不输出
      - name: pressure
        address: "40003"             # 保持寄存器 2
        type: float32
        group: sensors
        interval: 1s
      - name: pump_status
        address: "00001"             # 线圈 0
        type: bool
        group: actuators
        interval: 2s

  # ---------- 2. Siemens S7 驱动 (S7-300 / S7-1200 / S7-1500) ----------
  - name: siemens-s7-300
    type: s7
    settings:
      host: 192.168.1.200
      port: 102                      # ISO-on-TCP 端口
      rack: 0                        # 机架号
      slot: 2                        # S7-300 用 2，S7-1200/1500 用 1
      timeout: 3s
    tags:
      - name: reactor_temp
        address: "DB1.DBD0"          # DB1 双字 0 (Float32)
        type: float32
        group: reactor
        interval: 500ms
      - name: motor_speed
        address: "DB1.DBW4"          # DB1 字 4 (Uint16)
        type: uint16
        group: motors
        interval: 1s
      - name: emergency_stop
        address: "I0.0"              # 输入位 0.0
        type: bool
        group: safety
        interval: 200ms

  # ---------- 3. OPC UA 客户端驱动 (SCADA / MES / OPC 服务器) ----------
  - name: opc-server
    type: opcua
    settings:
      endpoint: "opc.tcp://192.168.1.50:4840"   # OPC UA 端点
      mode: polling                  # polling 或 subscription
      timeout: 5s
    tags:
      - name: conveyor_speed
        address: "ns=2;s=Conveyor.Speed"        # 命名空间+字符串 NodeId
        type: float64
        group: conveyor
        interval: 1s
      - name: batch_count
        address: "ns=1;i=1001"       # 命名空间+数字 NodeId
        type: uint32
        group: production
        interval: 2s

# ============================================================
# 北向传输
# 支持: mqtt, http
# ============================================================
transports:
  # ---------- 1. MQTT 云端传输 (发布 + 命令回写) ----------
  - name: cloud-mqtt
    type: mqtt
    settings:
      broker: tcp://broker.emqx.io:1883        # Broker 地址
      client-id: factory-edge-01               # 客户端 ID（需唯一）
      qos: 1                                   # 服务质量等级
      topic-template: "factory/{{.Driver}}/{{.Group}}/{{.Tag}}"  # 发布主题模板
      command-topic: "factory/commands/#"      # 订阅命令主题（支持通配符）
    batch-size: 50                             # 单批最大点数
    flush-interval: 1s                         # 定时刷新间隔

  # ---------- 2. HTTP Webhook / REST 推送 (MES / 数据湖 API) ----------
  - name: mes-http-push
    type: http
    settings:
      url: "http://localhost:8080/api/v1/telemetry"   # 上游 API
      method: POST                                     # HTTP 方法
      headers:
        Authorization: "Bearer mes-secret-key"         # 鉴权头
      timeout: 3s                                      # 请求超时
    batch-size: 100
    flush-interval: 5s

# ============================================================
# 规则
# 按 priority 升序求值 (数值越小 = 优先级越高)
# 动作: forward, drop, alert, transform, mirror
# ============================================================
rules:
  # ---------- 1. 高温告警 -> 立即转发到云 MQTT ----------
  - name: high-temp-alert
    match: "tag == 'temperature' && value > 95"
    action: alert
    target: cloud-mqtt
    priority: 1

  # ---------- 2. 急停 -> 高优先级告警 ----------
  - name: emergency-stop-alert
    match: "tag == 'emergency_stop' && value == true"
    action: alert
    target: cloud-mqtt
    priority: 2

  # ---------- 3. 安全与反应器数据 -> 镜像到 MQTT 与 MES 双通道 ----------
  - name: reactor-mirror
    match: "group == 'reactor'"
    action: mirror
    targets:
      - cloud-mqtt
      - mes-http-push
    priority: 10

  # ---------- 4. 兜底：其余全部转发到 MES HTTP ----------
  - name: default-catch-all
    match: "ALL"
    action: forward
    target: mes-http-push
    priority: 999
```

---

## 分段讲解

### global 段

```yaml
global:
  log-level: info
  api:
    listen: 0.0.0.0:9090
    secret: "corec-secret-token"
  engine:
    data-bus-size: 8192
    workers: 4
    shutdown-timeout: 30s
    default-tag-interval: 1s
```

| 配置点 | 说明 |
| --- | --- |
| `log-level: info` | 生产推荐级别，排障时临时切 `debug` |
| `api.listen` | 管理 API 监听所有网卡的 9090 端口（未设置则不启用） |
| `api.secret` | `listen` 设置时**必填**，客户端以 `Bearer` 令牌鉴权 |
| `engine.data-bus-size` | 内部数据通道容量 8192 |
| `engine.workers` | 规则管道处理协程数 |
| `engine.default-tag-interval` | 标签未设 `interval` 时的回退采集周期 |
| `engine.on-bad-quality` | 坏质量数据处理策略（`publish`/`drop`/`mark-and-publish`/`alert`） |
| `engine.stale-threshold` | 数据陈旧判定阈值，超过此时间未更新的值标记 `is_stale` |
| `engine.write-retry-count` | 写入指令失败重试次数，耗尽后进入死信队列 |
| `buffer.enabled` | 启用离线缓冲后，传输失败时数据落盘待回放 |

详见 [全局配置](/config/global)。

### drivers 段

示例配置了三种典型驱动：

| 驱动 | 协议 | 设备 | 关键设置 |
| --- | --- | --- | --- |
| `plc-modbus` | Modbus TCP | 192.168.1.100 | `slave-id: 1`，保持寄存器与线圈混采 |
| `siemens-s7-300` | S7 | 192.168.1.200 | `slot: 2`（S7-300），DB / I 区寻址 |
| `opc-server` | OPC UA | 192.168.1.50 | `mode: polling`，字符串与数字 NodeId |

::: tip
注意各驱动标签的 `interval` 差异：安全相关量（`emergency_stop`）用 `200ms` 高频采集，普通量用 `1s`–`2s`，避免无谓的通信开销。
:::

详见 [驱动配置](/config/drivers)。

### transports 段

| 传输 | 协议 | 目标 | 特点 |
| --- | --- | --- | --- |
| `cloud-mqtt` | MQTT | EMQX Broker | 支持命令回写，主题按 `驱动/分组/标签` 分层 |
| `mes-http-push` | HTTP | 本地 MES API | Bearer 鉴权，大批量低频率推送 |

```yaml
topic-template: "factory/{{.Driver}}/{{.Group}}/{{.Tag}}"
# 实际渲染：factory/plc-modbus/sensors/temperature
```

详见 [传输配置](/config/transports)。

### rules 段

规则按 `priority` 升序求值，本例的路由策略为：

| 优先级 | 规则 | 命中条件 | 动作 |
| --- | --- | --- | --- |
| 1 | `high-temp-alert` | 温度 > 95 | `alert` → cloud-mqtt |
| 2 | `emergency-stop-alert` | 急停触发 | `alert` → cloud-mqtt |
| 10 | `reactor-mirror` | 反应器分组 | `mirror` → cloud-mqtt + mes-http-push |
| 999 | `default-catch-all` | 全部 | `forward` → mes-http-push |

::: warning
第 4 条 `default-catch-all` 是**兜底规则**。未匹配任何规则的数据点会被丢弃，因此务必保留一条 `match: "ALL"` 的低优先级规则。
:::

详见 [规则配置](/config/rules)。

---

## 启动与验证

将上述配置保存为 `config.yaml`，启动核心：

```bash
./corec -f config.yaml
```

验证管理 API 可达：

```bash
# 查看驱动列表
curl -H "Authorization: Bearer corec-secret-token" \
     http://localhost:9090/api/v1/drivers

# 查看传输状态
curl -H "Authorization: Bearer corec-secret-token" \
     http://localhost:9090/api/v1/transports
```

预期返回各驱动与传输的运行状态 JSON。更多接口见 [API 参考](/api/overview)。

::: info
若日志出现驱动 `error` 状态，请依次检查：设备网络可达性、`slave-id` / `slot` / `endpoint` 正确性、防火墙端口放行。
:::
