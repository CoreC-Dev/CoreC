---
title: 统计监控
description: CoreC 统计监控 API，GET /stats 返回引擎运行统计 EngineStats，包含状态、运行时长、读写计数、丢弃数、每秒点数及各驱动/传输明细。
---

# 统计监控

`GET /stats` 返回核心引擎的完整运行时统计，包括连接状态、累计计数、吞吐速率及各驱动/传输的明细统计。所有计数器使用原子操作（`atomic`），读取零开销。

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
      "error_count": 3
    },
    "plc2": {
      "name": "plc2",
      "type": "s7",
      "state": 2,
      "last_read": "2024-09-08T10:30:00.5Z",
      "last_error": "",
      "tag_count": 64,
      "read_count": 32000,
      "error_count": 0
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
      "queue_size": 3
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
| `total_dropped` | uint64 | 全局累计丢弃数据点数（背压策略丢弃） |
| `points_per_sec` | float64 | 当前每秒数据点吞吐速率 |
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

`total_dropped` 统计因背压策略被丢弃的数据点总数。CoreC 采用 **drop-oldest** 背压策略：

- 当内部 channel 缓冲区满时，丢弃最旧的数据点以腾出空间
- 采集 goroutine **永不阻塞**，优先保证采集实时性
- 丢弃数持续增长通常意味着下游传输（Publish）处理速度跟不上采集速率

::: warning 排查丢弃
若 `total_dropped` 持续增长，建议检查：
1. 传输端（MQTT/HTTP）是否可达，网络是否有瓶颈
2. 传输 `batch-size` 和 `flush-interval` 配置是否合理
3. 引擎 `data-bus-size` 缓冲是否足够（默认 8192，可通过 `global.engine.data-bus-size` 调整）
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

## transport_stats 字段说明

`transport_stats` 是以传输名为键的映射，值为 `TransportStatus` 结构：

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `name` | string | 传输实例名称 |
| `type` | string | 传输类型（`mqtt`/`http` 等） |
| `state` | int | 连接状态枚举（`ConnState` 为 `int`）：`0=disconnected, 1=connecting, 2=connected, 3=error` |
| `published` | uint64 | 累计成功发布次数 |
| `failed` | uint64 | 累计失败次数 |
| `received` | uint64 | 入站数据点计数 |
| `last_publish` | string (RFC 3339) | 最近一次发布时间 |
| `queue_size` | int | 当前待发送队列长度 |

## 示例

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
