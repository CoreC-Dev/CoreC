---
title: 告警 API
description: CoreC 一等告警——活动告警查询（GET /alerts）、确认（POST /alerts/ack）与实时生命周期流（WS /alerts/stream）。
---

# 告警 API

规则（`action: alert`）与 bad-quality 策略（`on-bad-quality: alert`）触发的告警在引擎内有**一等状态**：每条告警以 `规则|驱动|标签` 为身份，随触发持续聚合计数，经确认（ack）标记，在静默满一个活动窗口后自动清除并进入有界历史。此前告警只是一条 WARN 日志，前端只能靠日志级别过滤"凑"出告警视图。

## 告警模型

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `key` | string | 稳定身份 `rule\|driver\|tag`——确认与流关联的键 |
| `rule` | string | 触发规则名（bad-quality 策略告警为 `bad-quality-policy`） |
| `driver` / `tag` | string | 触发点位 |
| `value` / `quality` | any / int | 最近一次触发时的值与质量（`quality` 为 `Quality` 枚举，按整数序列化：`0=good, 1=bad, 2=uncertain`；`value` 可缺省） |
| `first_fired` / `last_fired` | RFC 3339 | 首次/最近触发时间 |
| `count` | int | 活动期间的累计触发次数（按身份去重合并） |
| `acked` / `acked_at` | bool / RFC 3339 | 是否已被运维确认及确认时间（`acked_at` 确认后出现） |

**生命周期**：

```text
fired ──(持续再触发: count/last_fired 递增)──┐
   │                                        │
   ├── ack ──→ acked（保留在活动列表，标记确认）──┐
   │                                            │ 静默满 alert-active-window
   └────────────────────────────────────────────┴──→ cleared（移入历史）
```

- **确认粘滞**：ack 后的再触发只累加计数，不会重新弹醒告警（避免确认风暴）；只有静默清除会终结本次告警。
- **自动清除**：`alert-active-window`（默认 15m，`global.engine.alert-active-window` 可配）内无再触发即清除。注意：本模型无"恢复表达式"——清除只代表"不再触发"，不代表工况恢复。
- **有界**：活动注册表上限 1000 条（满时最旧者先行清除），历史上限 500 条事件（fired/acked/cleared）。

## 查询告警 — `GET /alerts`

```http
GET /alerts
Authorization: Bearer corec-secret-token
```

**响应**（`200 OK`）——活动列表按 `first_fired` 升序（最旧在前，运维列表的自然顺序），历史按时间新→旧：

```json
{
  "alerts": [
    {
      "key": "high-temp|plc1|temperature",
      "rule": "high-temp",
      "driver": "plc1",
      "tag": "temperature",
      "value": 92.5,
      "quality": 0,
      "first_fired": "2026-10-20T10:00:00Z",
      "last_fired": "2026-10-20T10:14:30Z",
      "count": 87,
      "acked": false
    }
  ],
  "history": [
    { "type": "acked", "alert": { "key": "low-pressure|plc2|press", "...": "..." }, "at": "2026-10-20T09:58:00Z" },
    { "type": "fired", "alert": { "key": "high-temp|plc1|temperature", "...": "..." }, "at": "2026-10-20T10:00:00Z" }
  ],
  "active_count": 1,
  "acked_count": 0
}
```

引擎未实现告警角色时返回 `501 Not Implemented`。

## 确认告警 — `POST /alerts/ack`

```http
POST /alerts/ack
Content-Type: application/json

{ "key": "high-temp|plc1|temperature" }
```

| 状态码 | 说明 |
|:---|:---|
| `204 No Content` | 确认成功 |
| `400 Bad Request` | 请求体非法或 `key` 缺失 |
| `404 Not Found` | 键不存在（告警已清除，客户端状态过期） |
| `501 Not Implemented` | 引擎未实现告警角色 |

key 放在请求体而非 URL 路径，因为键内含 `|` 字符。

## 实时告警流 — `WS /alerts/stream`

每条生命周期转换推送一个 JSON 事件（慢消费者丢事件而非阻塞采集管线，丢弃有计数）：

```json
{
  "type": "fired",
  "alert": { "key": "high-temp|plc1|temperature", "...": "..." },
  "at": "2026-10-20T10:00:00Z"
}
```

`type` 取值：`fired`（新告警激活）、`acked`（运维确认）、`cleared`（静默清除）。同一告警的重复触发**不产生**重复 `fired` 事件——计数值经 `GET /alerts` 轮询可见。

## 监控指标

| 指标 | 类型 | 标签 | 说明 |
|:---|:---|:---|:---|
| `corec_alerts_total` | counter | `rule` | 每条规则的累计触发次数（原始计数，未去重）——`rate()` 即触发频率 |
| `corec_alerts_active` | gauge | — | 当前活动告警数（含已确认） |

```promql
# 告警触发频率（每规则）
topk(5, rate(corec_alerts_total[5m]))

# 活动告警堆积告警
corec_alerts_active > 20
```
