---
title: 全局配置
description: CoreC 全局配置项参考，包含日志级别、API 服务、引擎调优等设置。
---

# 全局配置

`global` 段是 CoreC 核心的顶层配置，控制日志输出、管理 API 服务以及引擎运行参数。该段为**可选**配置，未提供时核心将使用内置默认值运行。

```yaml
global:
  log-level: info
  api:
    listen: 0.0.0.0:9090
    secret: "corec-secret-token"
  engine:
    data-bus-size: 8192
    # workers: 0                 # 0 = runtime.NumCPU()（默认）
    shutdown-timeout: 30s
```

## 配置项总览

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `log-level` | string | 否 | `info` | 日志输出级别 |
| `api` | object | 否 | — | 管理 API（RESTful + WebSocket）服务配置 |
| `engine` | object | 否 | — | 引擎运行参数（数据总线、worker、超时等） |
| `buffer` | object | 否 | — | 离线持久化缓冲配置，传输失败时将数据落盘以防丢失，详见下文 |

---

## log-level

日志输出级别，控制核心及各驱动、传输组件的日志详细程度。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `log-level` | string | 否 | `info` | 取值见下表 |

可选取值（从最详细到最简略）：

| 取值 | 说明 |
| --- | --- |
| `debug` | 调试信息，包含每次采集的原始数据，仅用于开发排障 |
| `info` | 常规运行信息，包含连接建立、规则命中、传输统计等 |
| `warn` | 警告信息，如重试、单次采集失败、规则解析告警 |
| `warning` | `warn` 的别名，等价于 `warn` |
| `error` | 错误信息，仅记录驱动断连、传输不可达等严重事件 |
| `silent` | 静默级别，抑制所有日志输出 |

```yaml
global:
  log-level: debug   # 生产环境建议 info，排障时临时切换为 debug
```

::: tip
生产环境请保持 `info`。长时间开启 `debug` 会产生大量日志，影响磁盘 I/O 与采集吞吐量。
:::

---

## api

CoreC 内置管理 API，提供驱动管理、数据读写、规则热更新、统计监控及 WebSocket 实时推送能力。该服务同时被 [API 参考](/api/overview) 中的所有接口使用。仅当 `listen` 设置时 API 服务才会启用。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `api.listen` | string | 否 | — | 监听地址，格式 `host:port`；未设置则不启用管理 API |
| `api.secret` | string | **是**† | — | 鉴权令牌，客户端需以 `Authorization: Bearer <secret>` 携带 |
| `api.tls-cert` | string | 否 | — | TLS 证书文件路径，启用 HTTPS 时需与 `tls-key` 同时设置 |
| `api.tls-key` | string | 否 | — | TLS 私钥文件路径 |
| `api.allowed-origins` | string[] | 否 | — | CORS 允许的来源列表；留空则默认放行所有来源（`*`） |
| `api.rate-limit-per-sec` | int | 否 | `0`（不限速） | 每秒每 IP 最大请求数，用于限流防护 |
| `api.read-header-timeout` | duration | 否 | `10s` | 读取请求头的最大时长 |
| `api.read-timeout` | duration | 否 | `0`（禁用） | 读取整个请求的最大时长 |
| `api.write-timeout` | duration | 否 | `0`（禁用） | 写响应的最大时长 |
| `api.idle-timeout` | duration | 否 | `120s` | keep-alive 下等待下一请求的最大时长 |
| `api.pprof-disabled` | bool | 否 | `false` | 设为 `true` 完全禁用 pprof 端点 |
| `api.pprof-addr` | string | 否 | — | pprof 独立端口地址（如 `127.0.0.1:6060`），无需认证，应绑定回环地址 |

> † `secret` 在 `listen` 设置时为必填，且长度至少 8 个字符。

```yaml
global:
  api:
    listen: 0.0.0.0:9090
    secret: "corec-secret-token"
    # tls-cert: /path/to/cert.pem
    # tls-key: /path/to/key.pem
    # allowed-origins:
    #   - http://localhost:3000
    # rate-limit-per-sec: 100
    # read-header-timeout: 10s
    # read-timeout: 0        # 0 = disabled
    # write-timeout: 0       # 0 = disabled
    # idle-timeout: 120s
```

### listen

监听地址，采用 `host:port` 形式。**未设置时管理 API 不启用**，核心仅保留数据采集与传输能力。

| 取值示例 | 含义 |
| --- | --- |
| `0.0.0.0:9090` | 监听所有网卡的 9090 端口 |
| `127.0.0.1:9090` | 仅监听本机回环地址，外部不可访问 |
| `:9090` | 等价于 `0.0.0.0:9090` |

::: warning
当 `api.listen` 设置时，`api.secret` 为**必填项**且长度至少 8 个字符。若未设置或过短，核心启动时将拒绝加载配置。请使用高熵随机字符串，避免硬编码到版本库。
:::

### tls-cert / tls-key

提供 TLS 证书与私钥文件路径即可启用 HTTPS。两者需同时设置。

```yaml
api:
  listen: 0.0.0.0:9090
  secret: "corec-secret-token"
  tls-cert: /etc/corec/certs/server.crt
  tls-key: /etc/corec/certs/server.key
```

### allowed-origins（CORS）

配置 CORS 允许的来源列表，用于浏览器端仪表板跨域访问。**留空时默认放行所有来源**（`Access-Control-Allow-Origin: *`）。如需限制跨域访问，显式列出允许的来源：

```yaml
api:
  allowed-origins:
    - http://localhost:3000
    - https://dashboard.example.com
```

### rate-limit-per-sec

每秒每 IP 最大请求数，用于防止管理 API 被滥用。设为 `0` 或不设置则不限速。

```yaml
api:
  rate-limit-per-sec: 100
```

### HTTP 超时

以下字段控制底层 HTTP Server 的超时行为，均为可选，未设置时使用默认值：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `read-header-timeout` | `10s` | 读取请求头超时 |
| `read-timeout` | `0`（禁用） | 读取整个请求超时 |
| `write-timeout` | `0`（禁用） | 写响应超时 |
| `idle-timeout` | `120s` | keep-alive 空闲超时 |

### 鉴权示例

所有管理 API 请求需在请求头携带令牌：

```bash
curl -H "Authorization: Bearer corec-secret-token" \
     http://localhost:9090/drivers
```

WebSocket 连接以查询参数传递：

```bash
wscat -c "ws://localhost:9090/tags/stream?token=corec-secret-token"
```

---

## engine

引擎运行参数，控制内部数据通道容量、处理协程数及关停超时等。所有字段均为可选，未设置时使用默认值。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `engine.data-bus-size` | int | 否 | `8192` | 内部数据通道缓冲容量，越大越抗突发但耗内存 |
| `engine.workers` | int | 否 | `NumCPU` | 规则管道处理协程数，`0` 表示按 CPU 核数 |
| `engine.shutdown-timeout` | duration | 否 | `30s` | 优雅关停时等待驱动/传输停止的最大时长 |
| `engine.error-throttle-window` | duration | 否 | `10s` | 调度器重复错误/超限日志的抑制时间窗 |
| `engine.default-tag-interval` | duration | 否 | `1s` | 未显式设置 `interval` 的标签的回退采集周期 |
| `engine.on-bad-quality` | string | 否 | `publish` | 坏质量数据处理策略，取值见下文 |
| `engine.stale-threshold` | duration | 否 | `0`（禁用） | 数据陈旧判定阈值，超过此时长未更新的缓存值标记为 `is_stale` |
| `engine.write-retry-count` | int | 否 | `3` | 写入指令失败重试次数，重试耗尽后进入死信队列 |
| `engine.command-concurrency` | int | 否 | `16` | 写入指令最大并行数，超限施加背压并溢入死信队列；设 `1` 恢复串行 |
| `engine.high-priority-workers` | int | 否 | `2` | 快间隔标签（`interval` ≤ 1s）专用处理协程数，与批量读取协程隔离 |

```yaml
global:
  engine:
    data-bus-size: 8192
    # workers: 0                 # 0 = runtime.NumCPU()（默认）
    shutdown-timeout: 30s
    error-throttle-window: 10s
    default-tag-interval: 1s
    on-bad-quality: mark-and-publish
    stale-threshold: 30s
    write-retry-count: 3
    # command-concurrency: 16
    # high-priority-workers: 2
```

### data-bus-size

连接驱动与处理管道的内部数据通道容量。突发负载下较大的值可减少丢点，但会增加内存占用。

### workers

规则管道处理协程数。设为 `0` 或不设置时按运行时 CPU 核数（`runtime.NumCPU()`）确定。

### shutdown-timeout

优雅关停时等待所有驱动与传输停止的最大时长，超时后强制退出。采用 Go duration 字符串。

### error-throttle-window

调度器在此时长窗口内抑制重复的错误/超限日志，避免日志风暴。

### default-tag-interval

标签未显式设置 `interval` 时的回退采集周期。采用 Go duration 字符串。

### on-bad-quality

控制处理管线对 `QualityBad` 数据点的处理策略。驱动层在读取失败时标记 `QualityBad`（如 Modbus 异常响应、通信超时），此配置决定这些坏值如何流转：

| 取值 | 行为 | 适用场景 |
| --- | --- | --- |
| `publish` | 正常发布（默认，向后兼容） | 下游系统自行处理坏值 |
| `drop` | 丢弃，不进入规则匹配和发布 | 避免坏值污染下游数据 |
| `mark-and-publish` | 发布但清空 `value`（置 nil），保留 `quality=bad` | 让下游知晓点位状态但不受错误值影响 |
| `alert` | 正常发布并触发告警回调 | 需要对坏值即时告警的场景 |

```yaml
global:
  engine:
    on-bad-quality: mark-and-publish
```

### stale-threshold

数据陈旧判定阈值。当缓存中某个数据点的 `Timestamp` 距当前时间超过此阈值时，API 响应中该数据点的 `is_stale` 字段标记为 `true`，帮助消费者区分实时数据与因驱动断连而停滞的旧值。设为 `0` 或不设置则禁用陈旧检测。

```yaml
global:
  engine:
    stale-threshold: 30s   # 超过 30s 未更新的数据点标记为 stale
```

### write-retry-count

写入指令（`POST /write`）失败后的重试次数。采用指数退避（100ms × 2^n，上限 2s）。重试全部耗尽后，失败的指令进入死信队列，可通过 `GET /write/failed` 查询。未设置或设为 `0` 时使用默认值 3。总尝试次数为 `write-retry-count + 1`。

```yaml
global:
  engine:
    write-retry-count: 5
```

### command-concurrency

写入指令（`POST /write`、MQTT command topic）的最大并行执行数。超出此限额的指令施加背压（传输的 command channel 填满后溢入死信队列），防止单个慢写入串行化所有后续控制指令。未设置或设为 `0` 时使用默认值 16。设为 `1` 可恢复完全串行行为。

```yaml
global:
  engine:
    command-concurrency: 32
```

### high-priority-workers

快间隔标签（`interval` ≤ 1s）的专用处理协程数。这些协程仅从 DataBus 高优先级通道读取，与批量读取协程隔离，避免低频批量读取突发时饿死高频采集。未设置或设为 `0` 时使用默认值 2。

```yaml
global:
  engine:
    high-priority-workers: 4
```

---

## buffer

离线持久化缓冲配置。当北向传输（MQTT Broker / HTTP Endpoint）不可达时，批量器重试耗尽后会将失败批次写入本地磁盘，待传输恢复后自动回放（drain），防止数据丢失。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `buffer.enabled` | bool | 否 | `false` | 是否启用离线缓冲 |
| `buffer.max-size` | int | 否 | `10000` | 缓冲文件最大数量，超出时驱逐最旧文件 |
| `buffer.path` | string | **是**† | — | 缓冲文件存储目录路径 |

> † `buffer.enabled` 为 `true` 时 `buffer.path` 必填，且 `max-size` 若设置需 ≥ 10，否则配置加载时返回错误。

```yaml
global:
  buffer:
    enabled: true
    path: /var/lib/corec/buffer
    max-size: 10000
```

::: tip 工作机制
启用后，每个配置了 `batch-size` 或 `flush-interval` 的传输会启动独立的 drain 协程，每 30s 尝试回放缓冲文件中的数据。回放按时间顺序逐文件进行，首个文件发送失败则停止回放等待下一轮。传输恢复后缓冲数据自动清空。
:::

::: warning 与重试的关系
离线缓冲是重试耗尽后的兜底机制。传输失败时先按 `retry-count` 进行内存重试，重试全部失败后才写入磁盘缓冲。配置 `buffer.enabled: true` 但未配置传输的 `batch-size`/`flush-interval` 时，传输仍会启用默认 5s 定时刷新以支持缓冲回放。
:::

---

## 完整示例

```yaml
global:
  log-level: info
  api:
    listen: 0.0.0.0:9090
    secret: "change-me-to-a-strong-random-token"
    rate-limit-per-sec: 100
  engine:
    data-bus-size: 8192
    # workers: 0                 # 0 = runtime.NumCPU()（默认）
    shutdown-timeout: 30s
    default-tag-interval: 1s
```

::: tip
将 `secret` 通过环境变量或外部密钥管理注入，而非直接写入配置文件。可结合启动脚本做 `${COREC_API_SECRET}` 替换。
:::
