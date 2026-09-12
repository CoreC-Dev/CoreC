---
title: 规则引擎
description: 规则引擎概念 —— 匹配表达式、动作类型、优先级排序与数据变换
---

# 规则引擎（Rules）

规则引擎是 CoreC 的**路由中枢**，决定每个数据点发往何处。它采用规则链设计：一组按优先级排序的规则，**首次匹配即生效**（first-match-wins）。本章详解匹配表达式、动作类型、优先级机制与运行时管理。

## 核心思想

```
DataPoint 进入规则引擎
        │
        ▼
  按优先级遍历规则链
        │
        ├─ 规则1 (priority=1)  match? ──否──▶ 继续下一条
        │                        │是
        │                        ▼
        │                   执行动作，返回
        ├─ 规则2 (priority=2)  ...
        │
        └─ 规则N (priority=999) match: "ALL"  ──▶ 兜底匹配
```

没有规则匹配的数据点将被**静默丢弃**（不转发到任何传输，但仍会更新 `LatestCache` 与广播给订阅者）。因此实践中**始终建议配置一条 `match: "ALL"` 的兜底规则**。

## 规则配置结构

```yaml
rules:
  - name: high-temp-alert        # 规则名（唯一标识）
    match: "tag == 'temperature' && value > 95"  # 匹配表达式
    action: alert                # 动作
    target: cloud-mqtt           # 单目标传输
    priority: 1                  # 优先级（数字越小越先匹配）
    transform:                   # 数据变换（可选，仅 transform 动作）
      expression: "value * 1.8 + 32"
      tag-rename: "temperature_f"
```

```yaml
  - name: reactor-mirror
    match: "group == 'reactor'"
    action: mirror               # 镜像动作
    targets:                     # 多目标传输
      - cloud-mqtt
      - mes-http-push
    priority: 10
```

| 字段 | 必填 | 说明 |
|:---|:---:|:---|
| `name` | ✅ | 规则名，用于统计与日志 |
| `match` | ✅ | 匹配表达式，详见下文 |
| `action` | ✅ | 动作类型：`forward` / `drop` / `alert` / `transform` / `mirror` |
| `target` | ❌* | 单目标传输名（`forward` / `alert` / `transform` 使用） |
| `targets` | ❌* | 多目标传输名列表（`mirror` 使用） |
| `priority` | ❌ | 优先级，默认 0；**数字越小优先级越高** |
| `transform` | ❌ | 变换配置，仅 `transform` 动作使用 |

::: warning target 与 targets
`forward` / `alert` / `transform` 使用 `target`（单数）；`mirror` 使用 `targets`（复数）。配置校验会检查引用的传输名是否存在，引用不存在的传输会导致启动失败。
:::

## 匹配表达式

匹配表达式（`match` 字段）支持简洁的字段比较与逻辑组合，无需引入完整表达式引擎即可覆盖绝大多数工业路由场景。

### 通配

```yaml
match: "ALL"
```

匹配所有数据点，通常用作兜底规则。大小写不敏感（`ALL`、`all` 均可）。

### 字段相等比较

```
field == 'value'
```

支持的字段来源于 `DataPoint`：

| 字段 | 来源 | 示例值 |
|:---|:---|:---|
| `driver` | `DataPoint.Driver` | `plc-modbus` |
| `device` | `DataPoint.Device` | `` |
| `group` | `DataPoint.Group` | `sensors` |
| `tag` | `DataPoint.Tag` | `temperature` |
| `type` | `DataPoint.Type.String()` | `float32` |
| `quality` | `DataPoint.Quality.String()` | `good` / `bad` / `uncertain` |

值用单引号或双引号包裹：

```yaml
match: "tag == 'temperature'"
match: 'group == "safety"'
match: "quality == 'bad'"
```

### 数值比较

对 `value` 字段支持四种数值比较运算符：

| 运算符 | 含义 | 示例 |
|:---|:---|:---|
| `>` | 大于 | `value > 95` |
| `<` | 小于 | `value < 10` |
| `>=` | 大于等于 | `value >= 100` |
| `<=` | 小于等于 | `value <= 0` |

数值比较会将 `DataPoint.Value` 转换为 `float64` 后比较，支持所有数值类型（`float32/64`、`int*`、`uint*`）。

### 逻辑组合（AND）

多个条件以 `&&` 连接，**全部满足**才匹配：

```yaml
match: "tag == 'temperature' && value > 95"
match: "group == 'safety' && tag == 'emergency_stop' && value == true"
```

::: info 当前表达式能力
CoreC 规则匹配使用内置的**零依赖表达式引擎**（`rule/expr.go`，无第三方依赖），支持：`==`/`!=` 相等、`=~`/`!~` 正则匹配（RE2）、`contains`/`suffix`/`prefix` 字符串运算、`> < >= <=` 数值比较、`in lo..hi` 数值区间、`&&`/`||` 逻辑与或、`!` 逻辑非、`( )` 括号分组、`ALL` 全匹配。可用的字段包括 `driver`、`device`、`group`、`tag`、`type`、`quality`、`value`。
:::

### 匹配示例速查

| 场景 | 表达式 |
|:---|:---|
| 匹配所有 | `ALL` |
| 按测点名 | `tag == 'temperature'` |
| 按分组 | `group == 'reactor'` |
| 按驱动 | `driver == 'plc-modbus'` |
| 过滤坏质量 | `quality == 'bad'` |
| 温度超限 | `tag == 'temperature' && value > 95` |
| 压力过低 | `tag == 'pressure' && value < 10` |
| 急停触发 | `tag == 'emergency_stop' && value == true` |
| 安全组且特定点 | `group == 'safety' && tag == 'emergency_stop'` |

## 动作类型

### forward —— 转发

最常用的动作，将数据点发往指定传输：

```yaml
- name: to-cloud
  match: "group == 'sensors'"
  action: forward
  target: cloud-mqtt
  priority: 100
```

### drop —— 丢弃

丢弃数据点，不转发到任何传输。常用于过滤噪声或坏质量数据：

```yaml
- name: drop-bad-quality
  match: "quality == 'bad'"
  action: drop
  priority: 1
```

::: tip drop 与缓存的关系
`drop` 只阻止北向转发，数据点**仍会**更新 `LatestCache` 并广播给 WebSocket 订阅者。即被 drop 的值仍可通过 API 查询，只是不上云。这保证了本地可观测性不受转发策略影响。
:::

### alert —— 告警

转发数据点到目标传输，**同时**触发告警回调。引擎内置的告警处理器会输出结构化告警日志：

```yaml
- name: high-temp-alert
  match: "tag == 'temperature' && value > 95"
  action: alert
  target: cloud-mqtt
  priority: 1
```

触发时日志：

```text
WARN ALERT rule=high-temp-alert driver=plc-modbus tag=temperature value=96.3
```

`alert` 本质是 `forward` + 告警通知。告警处理器可通过 `Engine.OnAlert(handler)` 注册自定义回调（如推送钉钉、邮件等）。

### mirror —— 镜像

将同一份数据点发往**多个**传输，实现一份数据多目的地分发：

```yaml
- name: reactor-mirror
  match: "group == 'reactor'"
  action: mirror
  targets:
    - cloud-mqtt       # 同时上云
    - mes-http-push    # 同时进 MES
  priority: 10
```

::: tip mirror 的典型用途
- 关键数据同时上云和本地 MES，双写保证
- 安全数据同时推送到告警通道和归档通道
- 灰度切换期间新旧传输并行
:::

### transform —— 变换

对数据点执行变换后转发。变换配置：

```yaml
- name: celsius-to-fahrenheit
  match: "tag == 'temperature'"
  action: transform
  target: cloud-mqtt
  priority: 5
  transform:
    expression: "value * 1.8 + 32"   # 值变换表达式
    tag-rename: "temperature_f"       # 重命名测点
```

| 字段 | 说明 |
|:---|:---|
| `expression` | 值变换表达式（对 `value` 字段运算） |
| `tag-rename` | 变换后的新测点名 |

::: info transform 求值引擎
`transform` 动作使用内置的算术求值引擎（`rule/arith.go`）对 `expression` 求值后替换 `value`，再应用 `tag-rename` 重命名测点，最后转发到目标传输。表达式支持 `+`/`-`/`*`/`/` 四则运算、`( )` 括号分组、数值字面量与 `value` 变量（如 `value * 1.8 + 32`）。非数值 `value`（如 `bool`、`string`、`nil`）时**静默保留原值**（不记录警告）；仅当表达式本身求值失败时才记录警告并保留原值。
:::

## 优先级机制

规则按 `priority` **升序**排列，数字越小优先级越高：

```yaml
rules:
  - name: drop-bad-quality
    match: "quality == 'bad'"
    action: drop
    priority: 1          # 最先匹配，坏质量直接丢弃

  - name: high-temp-alert
    match: "tag == 'temperature' && value > 95"
    action: alert
    target: cloud-mqtt
    priority: 2          # 其次匹配，高温告警

  - name: reactor-mirror
    match: "group == 'reactor'"
    action: mirror
    targets: [cloud-mqtt, mes-http-push]
    priority: 10         # 反应器数据镜像

  - name: default-catch-all
    match: "ALL"
    action: forward
    target: mes-http-push
    priority: 999        # 兜底，最后匹配
```

### 首次匹配即生效（First-Match-Wins）

引擎遍历排序后的规则链，**第一条匹配的规则生效后立即返回**，不再评估后续规则：

```go
func (e *Engine) Match(point DataPoint) *MatchResult {
    for _, r := range e.rules {  // 已按 priority 升序
        if r.Match(point) {
            return &MatchResult{Rule: r, Targets: r.Targets()}
        }
    }
    return nil  // 无匹配
}
```

::: warning 优先级设计要点
1. **顺序不保证**：规则使用 `sort.Slice`（非稳定排序）按 `priority` 排序，相同 `priority` 的规则之间的相对顺序**不保证**与配置文件顺序一致。建议显式设置不同优先级避免歧义
2. **兜底放最后**：`match: "ALL"` 的规则务必设置最大 priority，否则会"吃掉"所有后续规则
3. **过滤放最前**：`drop` 类规则设最小 priority，尽早过滤避免无效转发
:::

### 推荐的优先级分层

| 优先级范围 | 用途 | 示例 |
|:---|:---|:---|
| 1 ~ 9 | 过滤与丢弃 | 丢弃坏质量、丢弃高频噪声 |
| 10 ~ 99 | 告警与异常 | 温度超限、急停触发 |
| 100 ~ 499 | 业务路由 | 反应器镜像、安全组专用通道 |
| 500 ~ 998 | 特殊处理 | 变换、重路由 |
| 999 | 兜底转发 | `match: "ALL"` 默认通道 |

## 运行时管理

### 查询规则与命中统计

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  http://localhost:9090/rules | jq
```

```json
{
  "rules": [
    {
      "index": 0,
      "name": "drop-bad-quality",
      "type": "simple",
      "match": "quality == 'bad'",
      "action": "drop",
      "target": "",
      "priority": 1,
      "disabled": false,
      "hit_count": 23,
      "hit_at": "2024-01-15T10:30:15Z",
      "miss_count": 15211,
      "miss_at": "2024-01-15T10:30:15Z"
    },
    {
      "index": 1,
      "name": "high-temp-alert",
      "match": "tag == 'temperature' && value > 95",
      "action": "alert",
      "target": "cloud-mqtt",
      "priority": 2,
      "disabled": false,
      "hit_count": 3,
      "miss_count": 15231
    }
  ]
}
```

| 字段 | 说明 |
|:---|:---|
| `index` | 规则在排序链中的位置 |
| `disabled` | 是否被运行时禁用 |
| `hit_count` | 累计命中次数 |
| `hit_at` | 最后命中时刻 |
| `miss_count` | 累计未命中次数 |
| `miss_at` | 最后未命中时刻 |

::: tip 命中统计的价值
`hit_count` / `miss_count` 记录每条规则的命中与未命中次数。通过命中率可以验证规则是否如预期工作——如果一条告警规则 `hit_count` 始终为 0，可能阈值设置有误或测点名不匹配。
:::

### 运行时禁用 / 启用规则

无需重启即可临时禁用某条规则（如告警风暴时静默）：

```bash
# 禁用 index=1 的规则
curl -s -X PATCH -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"index": 1, "disabled": true}' \
  http://localhost:9090/rules/disable | jq
```

被禁用的规则在匹配时直接返回 `false`，等效于从规则链中移除，但配置不变，可随时重新启用。

## 完整规则配置示例

```yaml
rules:
  # ── 第 1 层：过滤 ──
  - name: drop-bad-quality
    match: "quality == 'bad'"
    action: drop
    priority: 1

  # ── 第 2 层：告警 ──
  - name: high-temp-alert
    match: "tag == 'temperature' && value > 95"
    action: alert
    target: cloud-mqtt
    priority: 2

  - name: emergency-stop-alert
    match: "tag == 'emergency_stop' && value == true"
    action: alert
    target: cloud-mqtt
    priority: 3

  # ── 第 3 层：业务路由 ──
  - name: reactor-mirror
    match: "group == 'reactor'"
    action: mirror
    targets:
      - cloud-mqtt
      - mes-http-push
    priority: 10

  # ── 第 4 层：变换 ──
  - name: celsius-to-fahrenheit
    match: "tag == 'temperature'"
    action: transform
    target: cloud-mqtt
    priority: 100
    transform:
      expression: "value * 1.8 + 32"
      tag-rename: "temperature_f"

  # ── 兜底 ──
  - name: default-catch-all
    match: "ALL"
    action: forward
    target: mes-http-push
    priority: 999
```

## 下一步

- [数据流](./data-flow.md) —— 规则引擎在处理循环中的位置
- [传输](./transports.md) —— 规则 `target` 指向的北向目的地
- [快速上手](./quickstart.md) —— 回到最小配置实践
