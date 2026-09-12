---
title: 规则管理
description: CoreC 规则管理 API，包含列出规则运行统计（GET /rules）和启用/禁用规则（PATCH /rules/disable）。
---

# 规则管理

规则（Rule）是 CoreC 规则引擎的核心单元，定义数据点的路由策略。以下端点用于查询规则运行统计及动态启用/禁用规则。

所有端点均需认证。

## 获取规则列表

获取所有规则的运行统计信息，包括命中次数、禁用状态等。

### 请求

```http
GET /rules
```

### 响应

`200 OK`

```json
{
  "rules": [
    {
      "index": 0,
      "name": "forward-all",
      "type": "simple",
      "match": "ALL",
      "action": "forward",
      "target": "mqtt-out",
      "targets": [],
      "priority": 100,
      "disabled": false,
      "hit_count": 44997,
      "hit_at": "2024-09-08T10:30:00Z",
      "miss_count": 0,
      "miss_at": "0001-01-01T00:00:00Z"
    },
    {
      "index": 1,
      "name": "drop-noise",
      "type": "simple",
      "match": "tag prefix 'noise_'",
      "action": "drop",
      "target": "",
      "targets": [],
      "priority": 200,
      "disabled": true,
      "hit_count": 0,
      "hit_at": "0001-01-01T00:00:00Z",
      "miss_count": 12000,
      "miss_at": "2024-09-08T10:29:59Z"
    }
  ]
}
```

### 字段说明 — RuleStat

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `index` | int | 规则在配置中的序号（用于禁用/启用操作） |
| `name` | string | 规则名称 |
| `type` | string | 规则类型（`simple`、`rule-set`、`sub-rule`） |
| `match` | string | 匹配表达式 |
| `action` | string | 匹配后执行的动作，见下表 |
| `target` | string | 目标传输名称（`forward`/`alert`/`transform` 动作使用） |
| `targets` | string[] | 多目标传输名称列表（`mirror` 动作使用） |
| `priority` | int | 优先级（数值越小优先级越高，按升序排列后 first-match-wins） |
| `disabled` | bool | 是否已禁用 |
| `hit_count` | uint64 | 累计命中次数 |
| `hit_at` | string (RFC 3339) | 最近一次命中时间 |
| `miss_count` | uint64 | 累计未命中次数 |
| `miss_at` | string (RFC 3339) | 最近一次未命中时间 |

### 动作类型（action）

| 值 | 说明 |
|:---|:---|
| `forward` | 转发至 `target` 指定的传输 |
| `drop` | 丢弃该数据点 |
| `alert` | 转发并触发告警回调 |
| `transform` | 变换值后转发 |
| `mirror` | 转发至 `targets` 指定的多个传输 |

### 示例

```bash
curl http://localhost:9090/rules \
  -H "Authorization: Bearer corec-secret-token"
```

---

## 启用/禁用规则

动态启用或禁用指定索引的规则，无需重载配置。禁用的规则不再参与匹配，但保留其统计计数。

### 请求

```http
PATCH /rules/disable
Content-Type: application/json
```

#### 请求体

```json
{
  "index": 1,
  "disabled": true
}
```

| 字段 | 类型 | 必填 | 说明 |
|:---|:---|:---|:---|
| `index` | int | 是 | 规则索引（对应 `GET /rules` 返回的 `index` 字段） |
| `disabled` | bool | 是 | `true` 为禁用，`false` 为启用 |

::: warning 请求体限制
请求体最大 1 MiB。
:::

### 响应

#### 操作成功 — `204 No Content`

无响应体。

#### 索引越界 — `400 Bad Request`

```json
{
  "error": "rule index out of range: 99"
}
```

#### 请求格式错误 — `400 Bad Request`

```json
{
  "error": "invalid character 'x' looking for beginning of value"
}
```

### 示例

::: code-group

```bash [禁用规则]
curl -X PATCH http://localhost:9090/rules/disable \
  -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"index": 1, "disabled": true}'
```

```bash [启用规则]
curl -X PATCH http://localhost:9090/rules/disable \
  -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"index": 1, "disabled": false}'
```

:::

### 典型用法

```bash
# 1. 查看当前规则列表，获取目标规则的 index
curl http://localhost:9090/rules \
  -H "Authorization: Bearer corec-secret-token"

# 2. 临时禁用高优先级的 drop 规则以排查问题
curl -X PATCH http://localhost:9090/rules/disable \
  -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"index": 1, "disabled": true}'

# 3. 排查完成后重新启用
curl -X PATCH http://localhost:9090/rules/disable \
  -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"index": 1, "disabled": false}'
```
