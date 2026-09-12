---
title: WebSocket 实时流
description: CoreC WebSocket 实时流接口，包含数据点流（/tags/stream）、日志流（/logs）、流量统计（/traffic）和内存监控（/memory）的连接方式与消息格式。
---

# WebSocket 实时流

CoreC 提供四个 WebSocket 端点，用于实时推送数据点、日志、流量统计和内存信息。所有端点均通过 HTTP GET 升级为 WebSocket 连接，使用 `github.com/coder/websocket` 库实现。

所有 WebSocket 端点均需认证。

## 连接方式

### 鉴权

WebSocket 端点同样受认证中间件保护。由于浏览器 `WebSocket` API 不支持自定义请求头，推荐通过 **Query 参数** 传递 Token：

::: code-group

```bash [wscat（命令行）]
wscat -c "ws://localhost:9090/tags/stream?token=corec-secret-token"
```

```js [浏览器 JavaScript]
const ws = new WebSocket('ws://localhost:9090/tags/stream?token=corec-secret-token')
ws.onmessage = (event) => {
  const point = JSON.parse(event.data)
  console.log(point)
}
```

```bash [Authorization Header（非浏览器客户端）]
# 部分客户端库支持自定义 Header
wscat -c "ws://localhost:9090/tags/stream" \
  -H "Authorization: Bearer corec-secret-token"
```

:::

### 通用行为

| 行为 | 说明 |
|:---|:---|
| 协议 | WebSocket（RFC 6455） |
| 消息格式 | JSON 文本帧 |
| 读取方向 | 服务端关闭读取循环（`CloseRead`），不处理客户端发送的消息 |
| 关闭 | 客户端断开连接时，服务端以 `StatusNormalClosure` 关闭 |
| 写入超时 | 每条消息写入超时 5 秒 |

---

## /tags/stream — 数据点实时流

实时推送引擎采集的每一个 `DataPoint`。数据来自核心 `Subscribe` 机制，经规则引擎匹配后推送。

### 请求

```http
GET /tags/stream?driver={name}
```

#### 查询参数

| 参数 | 类型 | 必填 | 说明 |
|:---|:---|:---|:---|
| `driver` | string | 否 | 仅订阅指定驱动名称的数据点；省略时订阅全部驱动 |

### 消息格式

每条消息为一个 `DataPoint` 对象（JSON 文本帧）：

```json
{
  "driver": "plc1",
  "device": "192.168.1.10",
  "group": "g1",
  "tag": "temperature",
  "value": 42.5,
  "type": 10,
  "quality": 0,
  "timestamp": "2024-09-08T10:30:00.123456789Z",
  "metadata": {
    "source": "register-40001"
  }
}
```

### DataPoint 字段说明

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `driver` | string | 采集驱动名称 |
| `device` | string | 设备标识 |
| `group` | string | 采集分组 |
| `tag` | string | 标签名称 |
| `value` | any | 标签值 |
| `type` | int | 数据类型枚举（`DataType` 为 `int`，按整数序列化）：`0=bool, 1=int8, …, 10=float64, 11=string, 12=bytes` |
| `quality` | int | 数据质量枚举（`Quality` 为 `int`，按整数序列化）：`0=good, 1=bad, 2=uncertain` |
| `timestamp` | string (RFC 3339) | 采集时间戳 |
| `metadata` | object | 附加元数据（可选，存在时才出现） |

### 示例

::: code-group

```bash [订阅全部驱动]
wscat -c "ws://localhost:9090/tags/stream?token=corec-secret-token"
```

```bash [按驱动过滤]
wscat -c "ws://localhost:9090/tags/stream?driver=plc1&token=corec-secret-token"
```

```js [浏览器示例]
const ws = new WebSocket(
  'ws://localhost:9090/tags/stream?driver=plc1&token=corec-secret-token'
)

ws.onopen = () => console.log('已连接数据流')
ws.onmessage = (e) => {
  const point = JSON.parse(e.data)
  console.log(`[${point.tag}] = ${point.value} (${point.quality})`)
}
ws.onerror = (e) => console.error('连接错误', e)
ws.onclose = () => console.log('连接已关闭')
```

:::

---

## /logs — 日志实时流

实时推送核心日志事件。数据来自日志总线 `log.Subscribe()`，源通道缓冲为 1024（`DefaultLogBufferSize`），每个订阅者通道缓冲为 128（`observable.DefaultSubscriberBuffer`）。

### 请求

```http
GET /logs
```

### 消息格式

每条消息为一个日志 `Event` 对象：

```json
{
  "level": 0,
  "type": "info",
  "payload": "RESTful API listening at 0.0.0.0:9090",
  "timestamp": "2024-09-08T10:30:00.123456789Z"
}
```

### Event 字段说明

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `level` | int | 日志级别数值（`slog.Level`，按整数序列化），见下表 |
| `type` | string | 日志类型字符串（`debug`/`info`/`warning`/`error`） |
| `payload` | string | 日志消息内容 |
| `timestamp` | string (RFC 3339) | 日志产生时间 |

### 日志级别（level）

`level` 为 `slog.Level`（`int`），按整数序列化：

| 数值 | 常量 | 字符串 |
|:---|:---|:---|
| `-4` | `LevelDebug` | `debug` |
| `0` | `LevelInfo` | `info` |
| `4` | `LevelWarn` | `warning` |
| `8` | `LevelError` | `error` |
| `18` | `LevelSilent` | `silent` |

::: tip 级别过滤
日志级别由全局配置 `global.log-level` 控制。低于设定级别的事件不会产生，因此也不会通过 WebSocket 推送。可通过 `PATCH /configs` 动态调整日志级别。
:::

### 示例

```bash
wscat -c "ws://localhost:9090/logs?token=corec-secret-token"
```

---

## /traffic — 流量统计实时流

定期推送引擎流量统计，包含累计读取、发布和丢弃数。适合用于仪表盘实时流量监控。

### 请求

```http
GET /traffic?interval={duration}
```

#### 查询参数

| 参数 | 类型 | 必填 | 说明 |
|:---|:---|:---|:---|
| `interval` | string (duration) | 否 | 推送间隔，如 `1s`、`500ms`；省略时默认 `1s` |

### 消息格式

每次间隔推送一个统计对象：

```json
{
  "read": 45000,
  "publish": 44997,
  "dropped": 0
}
```

### 字段说明

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `read` | uint64 | 累计读取次数（对应 `EngineStats.TotalRead`） |
| `publish` | uint64 | 累计发布次数（对应 `EngineStats.TotalPublish`） |
| `dropped` | uint64 | 累计丢弃数（对应 `EngineStats.TotalDropped`） |

::: info 推送频率
默认每 1 秒推送一次（`time.NewTicker(wsPushInterval)`），可通过 `?interval=` 查询参数自定义间隔。
:::

### 示例

::: code-group

```bash [wscat]
wscat -c "ws://localhost:9090/traffic?token=corec-secret-token"
```

```js [浏览器仪表盘]
const ws = new WebSocket('ws://localhost:9090/traffic?token=corec-secret-token')

ws.onmessage = (e) => {
  const { read, publish, dropped } = JSON.parse(e.data)
  document.getElementById('read').textContent = read
  document.getElementById('publish').textContent = publish
  document.getElementById('dropped').textContent = dropped
}
```

:::

---

## /memory — 内存监控实时流

定期推送 Go 运行时内存统计，用于监控核心内存占用和 GC 活动。

### 请求

```http
GET /memory?interval={duration}
```

#### 查询参数

| 参数 | 类型 | 必填 | 说明 |
|:---|:---|:---|:---|
| `interval` | string (duration) | 否 | 推送间隔，如 `1s`、`2s`；省略时默认 `1s` |

### 消息格式

每次间隔推送一个内存统计对象：

```json
{
  "alloc": 12345678,
  "total_alloc": 98765432,
  "sys": 52428800,
  "num_gc": 42,
  "goroutines": 15
}
```

### 字段说明

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `alloc` | uint64 | 当前堆内存分配字节数（`runtime.MemStats.Alloc`） |
| `total_alloc` | uint64 | 累计分配字节数（`runtime.MemStats.TotalAlloc`） |
| `sys` | uint64 | 从操作系统获取的内存总字节数（`runtime.MemStats.Sys`） |
| `num_gc` | uint32 | GC 完成次数（`runtime.MemStats.NumGC`） |
| `goroutines` | int | 当前 goroutine 数量（`runtime.NumGoroutine()`） |

::: info 推送频率
默认每 1 秒推送一次（`time.NewTicker(wsPushInterval)`），可通过 `?interval=` 查询参数自定义间隔。
:::

::: warning 性能影响
每次推送都会调用 `runtime.ReadMemStats()`，该操作会触发 STW（Stop-The-World）。1 秒的推送间隔对生产环境影响可忽略。
:::

### 示例

::: code-group

```bash [wscat]
wscat -c "ws://localhost:9090/memory?token=corec-secret-token"
```

```js [浏览器仪表盘]
const ws = new WebSocket('ws://localhost:9090/memory?token=corec-secret-token')

ws.onmessage = (e) => {
  const { alloc, sys, num_gc, goroutines } = JSON.parse(e.data)
  console.log(`内存: ${(alloc / 1048576).toFixed(1)} MB`)
  console.log(`GC 次数: ${num_gc}, Goroutines: ${goroutines}`)
}
```

:::

---

## 连接生命周期

所有 WebSocket 端点遵循相同的连接生命周期：

```
客户端                          服务端
  │                               │
  │── HTTP GET (Upgrade) ────────→│  websocket.Accept()
  │←─ 101 Switching Protocols ────│
  │                               │
  │                               │  CloseRead(ctx)  ← 关闭读取循环
  │                               │
  │←─ JSON 消息帧 ────────────────│  wsjson.Write()  ← 持续推送
  │←─ JSON 消息帧 ────────────────│
  │←─ JSON 消息帧 ────────────────│
  │                               │
  │── Close Frame ───────────────→│  ctx.Done()
  │←─ Close Frame ────────────────│  StatusNormalClosure
  │                               │
```

### 关闭码

| 关闭码 | 说明 |
|:---|:---|
| `1000` (Normal Closure) | 客户端正常断开 |
| `1011` (Internal Error) | 服务端内部错误（defer 默认关闭码） |

::: tip 重连建议
客户端应实现自动重连机制，在连接断开后使用指数退避策略重新建立连接。建议初始延迟 1 秒，最大延迟 30 秒。
:::
