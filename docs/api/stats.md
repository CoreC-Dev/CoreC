---
title: 统计监控
description: CoreC 统计监控 API，GET /stats 返回引擎运行统计 EngineStats，GET /metrics 提供 Prometheus 指标，包含状态、运行时长、读写计数、丢弃数、每秒点数及各驱动/传输明细。
---

# 统计监控

`GET /stats` 返回核心引擎的完整运行时统计，包括连接状态、累计计数、吞吐速率及各驱动/传输的明细统计。吞吐计数器（读取/发布/错误）使用原子操作（`atomic`）维护，并发读取不阻塞采集。

## 获取引擎统计

### 请求

```http
GET /stats
```

### 响应

`200 OK`

```json
{
  "status": "running",
  "uptime": 3600000000000,
  "drivers": 2,
  "transports": 1,
  "rules": 5,
  "total_read": 45000,
  "total_publish": 44997,
  "total_errors": 3,
  "total_dropped": 0,
  "points_per_sec": 150.5,
  "driver_stats": {
    "plc1": {
      "name": "plc1",
      "type": "modbus-tcp",
      "state": 2,
      "last_read": "2024-09-08T10:30:00.123456789Z",
      "last_error": "",
      "tag_count": 120,
      "read_count": 45000,
      "error_count": 3,
      "reconnect_count": 1
    },
    "plc2": {
      "name": "plc2",
      "type": "s7",
      "state": 2,
      "last_read": "2024-09-08T10:30:00.5Z",
      "last_error": "",
      "tag_count": 64,
      "read_count": 32000,
      "error_count": 0,
      "reconnect_count": 0
    }
  },
  "transport_stats": {
    "mqtt-out": {
      "name": "mqtt-out",
      "type": "mqtt",
      "state": 2,
      "published": 44997,
      "failed": 0,
      "received": 0,
      "last_publish": "2024-09-08T10:30:00.1Z",
      "queue_size": 3,
      "dropped_commands": 0
    }
  }
}
```

## EngineStats 字段说明

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `status` | string | 引擎运行状态，见下表 |
| `uptime` | int64 | 运行时长（纳秒，Go `time.Duration` 的 JSON 序列化形式） |
| `drivers` | int | 已注册驱动数量 |
| `transports` | int | 已注册传输数量 |
| `rules` | int | 已注册规则数量 |
| `total_read` | uint64 | 全局累计读取次数 |
| `total_publish` | uint64 | 全局累计发布次数 |
| `total_errors` | uint64 | 全局累计错误次数 |
| `total_dropped` | uint64 | 全局累计丢弃数（数据总线驱逐 + 慢订阅者 + 日志事件的合计，见下文详解） |
| `points_per_sec` | float64 | 近 60 秒滑动窗口的每秒数据点吞吐速率；采集停止超过 1 分钟后归零 |
| `driver_stats` | object | 各驱动状态明细，键为驱动名 |
| `transport_stats` | object | 各传输状态明细，键为传输名 |

### 引擎状态（status）

| 值 | 说明 |
|:---|:---|
| `running` | 正常运行中 |
| `suspended` | 已挂起（采集暂停，API 仍可用） |
| `stopped` | 已停止 |

### uptime 单位说明

`uptime` 是 Go `time.Duration` 类型的 JSON 序列化结果，单位为**纳秒**。换算示例：

| uptime（纳秒） | 实际时长 |
|:---|:---|
| `1000000000` | 1 秒 |
| `60000000000` | 1 分钟 |
| `3600000000000` | 1 小时 |
| `86400000000000` | 1 天 |

::: tip 换算公式
`秒 = uptime / 1e9`，`分钟 = uptime / 6e10`，`小时 = uptime / 3.6e12`
:::

## total_dropped 详解

`total_dropped` 是**四类丢弃的合计**（引擎未启动时为 0）：

| 组成 | 来源 | 含义 |
|:---|:---|:---|
| 数据总线主通道驱逐 | `DataBus.PushDropped()` | 主通道缓冲满时按 drop-oldest 驱逐最旧数据点（采集 goroutine 永不阻塞） |
| 高优先级通道驱逐 | `DataBus.HighPriorityPushDropped()` | 高优先级（快间隔）通道缓冲满时同样 drop-oldest |
| 慢订阅者丢弃 | `DataBus.Dropped()` | `/tags/stream` 订阅者消费不及时，点被丢弃而非阻塞采集管线 |
| 日志事件丢弃 | `log.Dropped()` | 日志总线满或日志订阅者过慢而丢弃的**日志事件数**（非数据点） |

bad-quality 丢弃策略（`on-bad-quality: drop`）**不计入** `total_dropped`——配置策略不属于处理错误，单独经 `corec_bad_quality_dropped_total` 暴露。

::: warning 排查丢弃
`total_dropped` 持续增长时，先用 `/metrics` 的分组计数定位丢弃来源：

| 指标 | 含义 |
|:---|:---|
| `corec_databus_dropped_total{channel="main"}` / `{channel="priority"}` | 数据总线主/高优先级通道驱逐数 |
| `corec_slow_subscriber_dropped_total` | `/tags/stream` 慢订阅者丢弃数 |
| `corec_log_dropped_total` | 日志事件丢弃数（日志事件，非数据点） |
| `corec_bad_quality_dropped_total` | bad-quality 策略丢弃数（不在 total_dropped 内） |

数据总线驱逐增长通常意味着规则管线处理速度跟不上采集速率，可检查引擎 `data-bus-size` 缓冲是否足够（默认 8192，可通过 `global.engine.data-bus-size` 调整）；发布相关积压则检查传输可达性、`batch-size` 与 `flush-interval` 配置。
:::

## driver_stats 字段说明

`driver_stats` 是以驱动名为键的映射，值为 `DriverStatus` 结构：

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `name` | string | 驱动实例名称 |
| `type` | string | 驱动协议类型 |
| `state` | int | 连接状态枚举（`ConnState` 为 `int`）：`0=disconnected, 1=connecting, 2=connected, 3=error` |
| `last_read` | string (RFC 3339) | 最近一次成功读取时间 |
| `last_error` | string | 最近一次错误信息 |
| `tag_count` | int | 配置的标签总数 |
| `read_count` | uint64 | 累计成功读取次数 |
| `error_count` | uint64 | 累计错误次数 |
| `reconnect_count` | uint64 | 累计重连尝试次数 |

## transport_stats 字段说明

`transport_stats` 是以传输名为键的映射，值为 `TransportStatus` 结构：

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `name` | string | 传输实例名称 |
| `type` | string | 传输类型（`mqtt` / `http` / `notify`） |
| `state` | int | 连接状态枚举（`ConnState` 为 `int`）：`0=disconnected, 1=connecting, 2=connected, 3=error` |
| `published` | uint64 | 累计成功发布次数 |
| `failed` | uint64 | 累计失败次数 |
| `received` | uint64 | 入站数据点计数 |
| `last_publish` | string (RFC 3339) | 最近一次发布时间 |
| `queue_size` | int | 当前待发送队列长度 |
| `dropped_commands` | uint64 | 命令通道满时丢弃的写命令数（非零表示持续命令背压） |

---

## Prometheus 指标 — `GET /metrics`

`GET /metrics` 以 Prometheus 文本格式（`text/plain; version=0.0.4`）输出指标，与 `/stats` 同源于引擎统计，供 Prometheus 抓取。输出全部由引擎现有统计与可选角色接口生成，无需额外依赖。

```bash
curl http://localhost:9090/metrics \
  -H "Authorization: Bearer corec-secret-token"
```

### 引擎总览

| 指标 | 类型 | 说明 |
|:---|:---|:---|
| `corec_reads_total` | counter | 累计读取数据点数（`EngineStats.TotalRead`） |
| `corec_publishes_total` | counter | 累计发布数据点数（`EngineStats.TotalPublish`） |
| `corec_errors_total` | counter | 累计处理错误数（`EngineStats.TotalErrors`） |
| `corec_dropped_total` | counter | 累计丢弃数（`EngineStats.TotalDropped`，四类丢弃的合计，见上） |
| `corec_log_dropped_total` | counter | 因慢消费者丢弃的日志事件数 |
| `corec_drivers` / `corec_transports` / `corec_rules` | gauge | 已配置驱动/传输/规则数量 |
| `corec_uptime_seconds` | gauge | 引擎运行秒数 |
| `corec_points_per_second` | gauge | 近 60 秒每秒数据点吞吐（采集停止即归零，与 `/stats` 的 `points_per_sec` 同口径） |

### 按驱动

| 指标 | 类型 | 标签 | 说明 |
|:---|:---|:---|:---|
| `corec_driver_read_total` | counter | `driver`, `type` | 该驱动累计读取次数 |
| `corec_driver_errors_total` | counter | `driver` | 该驱动累计错误次数 |
| `corec_driver_reconnect_total` | counter | `driver` | 该驱动累计重连尝试次数 |
| `corec_driver_tags` | gauge | `driver` | 该驱动配置的标签数 |
| `corec_driver_connected` | gauge | `driver` | 1=已连接，0=其他状态 |
| `corec_driver_last_read_timestamp_seconds` | gauge | `driver` | 最近一次成功读取的 Unix 时间戳（首次读取前省略） |
| `corec_driver_last_read_seconds` | gauge | `driver` | 距最近一次成功读取的秒数（首次读取前省略） |
| `corec_scheduler_degraded` | gauge | `driver` | 1=该驱动存在被自动降频的采集任务 |
| `corec_scheduler_consecutive_errors` | gauge | `driver` | 采集任务连续读失败的最长连击（成功即清零） |
| `corec_scheduler_overruns_total` | counter | `driver` | 读取耗时超过采集间隔的累计次数 |

### 按传输

| 指标 | 类型 | 标签 | 说明 |
|:---|:---|:---|:---|
| `corec_transport_published_total` | counter | `transport`, `type` | 该传输累计发布消息数 |
| `corec_transport_failed_total` | counter | `transport` | 该传输累计发布失败数 |
| `corec_transport_received_total` | counter | `transport` | 该传输累计入站数据点数 |
| `corec_transport_queue_size` | gauge | `transport` | 当前待发送队列长度 |
| `corec_transport_connected` | gauge | `transport` | 1=已连接，0=其他状态 |
| `corec_transport_dropped_commands_total` | counter | `transport` | 命令通道满时在入站丢弃的写命令数（仅非零时输出） |

### 丢弃分组、离线缓冲与死信

| 指标 | 类型 | 说明 |
|:---|:---|:---|
| `corec_databus_dropped_total{channel="main"\|"priority"}` | counter | 数据总线主/高优先级通道驱逐数 |
| `corec_slow_subscriber_dropped_total` | counter | `/tags/stream` 慢订阅者丢弃数 |
| `corec_bad_quality_dropped_total` | counter | `on-bad-quality: drop` 策略丢弃数 |
| `corec_offline_buffer_pending` | gauge | 离线缓冲中等待回放的批次数 |
| `corec_offline_buffer_drained_total` | counter | 离线缓冲累计成功回放批次数 |
| `corec_offline_buffer_pushed_total` | counter | 重试耗尽后累计写入离线缓冲的批次数 |
| `corec_offline_buffer_bytes` | gauge | 离线缓冲占用磁盘字节数 |
| `corec_offline_buffer_evicted_total` | counter | 缓冲满时被驱逐的最旧批次数 |
| `corec_flush_batches_dropped_total` | counter | 异步批冲刷队列因持续背压丢弃的满批数 |
| `corec_dead_letter_queue_size` | gauge | 写死信队列当前深度 |
| `corec_dead_letter_pushed_total` | counter | 累计进入写死信队列的指令数 |

### 告警、HTTP 与延迟

| 指标 | 类型 | 说明 |
|:---|:---|:---|
| `corec_alerts_total{rule}` | counter | 每条规则累计触发次数（原始计数，未去重） |
| `corec_alerts_active` | gauge | 当前活动告警数（含已确认） |
| `corec_http_requests_total{method,status}` | counter | API 网关累计 HTTP 请求数 |
| `corec_http_request_duration_seconds` | histogram | HTTP 请求耗时（秒） |
| `corec_read_latency_seconds{driver}` | histogram | 驱动读取延迟（秒） |
| `corec_publish_latency_seconds{transport}` | histogram | 传输发布延迟（秒） |
| `corec_data_age_seconds{transport}` | histogram | 数据年龄（发布时刻 − 采集时间戳，秒） |

### Go 运行时与驱动自上报

| 指标 | 类型 | 说明 |
|:---|:---|:---|
| `corec_goroutines` | gauge | 运行中的 goroutine 数 |
| `corec_mem_heap_alloc_bytes` / `corec_mem_heap_sys_bytes` / `corec_mem_stack_inuse_bytes` | gauge | 堆使用/堆获取/栈使用字节数 |
| `corec_mem_total_alloc_bytes` | counter | 累计分配字节数 |
| `corec_gc_count` | counter | GC 完成次数 |
| `corec_gc_pause_total_seconds` | counter | GC 停顿累计秒数 |
| `corec_cpu_count` | gauge | 可用逻辑 CPU 数 |
| 驱动自上报系列 | — | 驱动协议级指标按驱动自有名称原样输出（如 `modbus_*`、`opcua_*`、`s7_*`） |

## /stats 使用示例

```bash
curl http://localhost:9090/stats \
  -H "Authorization: Bearer corec-secret-token"
```

### 使用 jq 提取关键字段

```bash
# 查看引擎状态和运行时长（秒）
curl -s http://localhost:9090/stats \
  -H "Authorization: Bearer corec-secret-token" \
  | jq '{status, uptime_sec: (.uptime / 1e9)}'

# 查看各驱动连接状态
curl -s http://localhost:9090/stats \
  -H "Authorization: Bearer corec-secret-token" \
  | jq '.driver_stats | to_entries[] | {driver: .key, state: .value.state}'

# 监控丢弃速率
watch -n 1 'curl -s http://localhost:9090/stats \
  -H "Authorization: Bearer corec-secret-token" \
  | jq ".total_dropped"'
```
