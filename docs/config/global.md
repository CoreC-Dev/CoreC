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
    workers: 4
    shutdown-timeout: 30s
```

## 配置项总览

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `log-level` | string | 否 | `info` | 日志输出级别 |
| `api` | object | 否 | — | 管理 API（RESTful + WebSocket）服务配置 |
| `engine` | object | 否 | — | 引擎运行参数（数据总线、worker、超时等） |
| `buffer` | object | 否 | — | ⚠️ 已废弃，解析不报错但不生效，详见下文 |

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
| `error` | 错误信息，仅记录驱动断连、传输不可达等严重事件 |
| `fatal` | 致命错误，核心无法继续运行时输出 |

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
| `api.read-timeout` | duration | 否 | `30s` | 读取整个请求的最大时长 |
| `api.write-timeout` | duration | 否 | `30s` | 写响应的最大时长 |
| `api.idle-timeout` | duration | 否 | `120s` | keep-alive 下等待下一请求的最大时长 |

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
    # read-timeout: 30s
    # write-timeout: 30s
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
| `read-timeout` | `30s` | 读取整个请求超时 |
| `write-timeout` | `30s` | 写响应超时 |
| `idle-timeout` | `120s` | keep-alive 空闲超时 |

### 鉴权示例

所有管理 API 请求需在请求头携带令牌：

```bash
curl -H "Authorization: Bearer corec-secret-token" \
     http://localhost:9090/api/v1/drivers
```

WebSocket 连接以查询参数传递：

```bash
wscat -c "ws://localhost:9090/api/v1/ws?token=corec-secret-token"
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

```yaml
global:
  engine:
    data-bus-size: 8192
    workers: 4
    shutdown-timeout: 30s
    error-throttle-window: 10s
    default-tag-interval: 1s
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

---

## buffer

> ⚠️ **`global.buffer` 已废弃（deprecated）。**
> 离线缓冲/断网续传功能不再支持。该段仍可被解析（向后兼容），但**不会产生任何效果**，配置非空时核心会输出一条 `warn` 日志提示移除该段。
> 如需批量缓冲和重试，请使用 transport 级别的 `batch-size`、`flush-interval`、`retry-count` 配置。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `buffer.enabled` | bool | 否 | `false` | （已废弃，不生效） |
| `buffer.max-size` | int | 否 | `0` | （已废弃，不生效） |
| `buffer.path` | string | 否 | — | （已废弃，不生效） |

::: warning
建议从配置中**移除整个 `buffer` 段**以消除启动告警。该段仅保留用于向后兼容解析。
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
    workers: 4
    shutdown-timeout: 30s
    default-tag-interval: 1s
```

::: tip
将 `secret` 通过环境变量或外部密钥管理注入，而非直接写入配置文件。可结合启动脚本做 `${COREC_API_SECRET}` 替换。
:::
