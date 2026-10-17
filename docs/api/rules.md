---
title: 规则管理
description: CoreC 规则管理 API，包含列出规则运行统计（GET /rules）、启用/禁用规则（PATCH /rules/disable）与规则试算（POST /rules/preview）。
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
| `continue` | bool | 命中后是否继续评估后续规则（默认 `false` = 终止） |
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

动态启用或禁用指定规则，无需重载配置。禁用的规则不再参与匹配，但保留其统计计数。

推荐按 `name` 寻址：规则名唯一（配置校验强制），在配置 reload / 重排后保持稳定；`index` 会随规则增删或重排漂移，仅为向后兼容保留。

### 请求

```http
PATCH /rules/disable
Content-Type: application/json
```

#### 请求体

```json
{
  "name": "overtemp-alarm",
  "disabled": true
}
```

| 字段 | 类型 | 必填 | 说明 |
|:---|:---|:---|:---|
| `name` | string | 与 `index` 二选一 | 规则名（对应 `GET /rules` 返回的 `name` 字段）。**推荐**，reload 后仍稳定。同时传入 `name` 与 `index` 时以 `name` 为准 |
| `index` | int | 与 `name` 二选一 | 规则索引（对应 `GET /rules` 返回的 `index` 字段）。向后兼容保留；注意 reload 或重排后索引会漂移 |
| `disabled` | bool | 是 | `true` 为禁用，`false` 为启用 |

::: warning 请求体限制
请求体最大 1 MiB。
:::

### 响应

#### 操作成功 — `204 No Content`

无响应体。

#### 规则不存在 — `400 Bad Request`

```json
{
  "error": "rule not found: \"overtemp-alarm\""
}
```

#### 索引越界 — `400 Bad Request`（index 寻址时）

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

```bash [禁用规则（按名）]
curl -X PATCH http://localhost:9090/rules/disable \
  -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"name": "overtemp-alarm", "disabled": true}'
```

```bash [启用规则（按名）]
curl -X PATCH http://localhost:9090/rules/disable \
  -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"name": "overtemp-alarm", "disabled": false}'
```

```bash [禁用规则（index，向后兼容）]
curl -X PATCH http://localhost:9090/rules/disable \
  -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"index": 1, "disabled": true}'
```

:::

### 典型用法

```bash
# 1. 查看当前规则列表，获取目标规则的 name
curl http://localhost:9090/rules \
  -H "Authorization: Bearer corec-secret-token"

# 2. 临时禁用高优先级的 drop 规则以排查问题（按 name，reload 后仍指向同一条规则）
curl -X PATCH http://localhost:9090/rules/disable \
  -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"name": "overtemp-alarm", "disabled": true}'

# 3. 排查完成后重新启用
curl -X PATCH http://localhost:9090/rules/disable \
  -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"name": "overtemp-alarm", "disabled": false}'
```

---

## 规则试算

离线试算：将一条 match 表达式在**临时规则引擎**中编译，并对一个样例数据点求值——**不触碰生产规则集**（不修改任何规则、不影响命中统计）。用于在保存规则前验证表达式的语法与语义，避免"保存后靠生产数据暴露编译错误"。

试算用的临时规则使用合成名 `preview` 与 `forward` 动作：`action`/`target` 的合法性属于配置校验（config.validate）的职责，试算只关心 `match` 表达式本身。

### 请求

```http
POST /rules/preview
Content-Type: application/json
```

#### 请求体

```json
{
  "match": "tag == 'temperature' && value > 95",
  "point": {
    "driver": "plc1",
    "device": "",
    "group": "g1",
    "tag": "temperature",
    "value": 96.5,
    "type": "float64",
    "quality": 0,
    "timestamp": "2024-09-08T10:30:00Z"
  }
}
```

| 字段 | 类型 | 必填 | 说明 |
|:---|:---|:---|:---|
| `match` | string | 是 | 待试算的 match 表达式。空字符串返回 `400` |
| `point` | object (DataPoint) | 否 | 样例数据点，结构与 `GET /tags` 返回的 `DataPoint` 一致，可直接粘贴后微调。省略时按零值 DataPoint 求值（适合只引用 `value` 的表达式） |

::: warning 请求体限制
请求体最大 1 MiB。
:::

### 响应

#### 试算成功 — `200 OK`

```json
{
  "matched": true
}
```

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `matched` | bool | 拥有该 match 表达式的规则是否会在此样例点上命中 |

#### 表达式编译错误 — `400 Bad Request`

错误信息**原样返回**（不做脱敏），便于前端在表达式编辑器旁直接展示 expr 的具体报错：

```json
{
  "error": "failed to create rule preview: invalid match expression \"tag ==\": compile expression \"tag ==\" (translated: \"tag ==\"): unexpected token EOF (1:6)"
}
```

#### 请求格式错误 — `400 Bad Request`

```json
{
  "error": "invalid character 'x' looking for beginning of value"
}
```

#### match 为空 — `400 Bad Request`

```json
{
  "error": "match is required"
}
```

#### 引擎不支持 — `501 Not Implemented`

当引擎未实现可选角色接口 `core.RulePreviewer`（如测试替身、第三方 Engine 实现）时返回：

```json
{
  "error": "rule preview not supported by this engine"
}
```

### 示例

::: code-group

```bash [命中试算]
curl -X POST http://localhost:9090/rules/preview \
  -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{
    "match": "value > 95",
    "point": {"driver": "plc1", "tag": "temperature", "value": 96.5, "type": "float64", "quality": 0, "timestamp": "2024-09-08T10:30:00Z"}
  }'
# → {"matched": true}
```

```bash [编译错误试算]
curl -X POST http://localhost:9090/rules/preview \
  -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"match": "value >", "point": {"tag": "t", "value": 1}}'
```

:::

### 典型用法

```bash
# 在 Dashboard 规则编辑器中：先试算，再保存
# 1. 从 GET /tags 复制真实数据点作为样例，试算新表达式（shell 示例
#    选用不含单引号的表达式；含字符串字面量的写法见上方请求体示例）
curl -X POST http://localhost:9090/rules/preview \
  -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"match": "value > 95 && value in 0..200", "point": {"driver": "plc1", "group": "safety", "tag": "temperature", "value": 96.5, "type": "float64", "quality": 0}}'
# → {"matched": true} 后再写入配置并 PUT /configs
```
