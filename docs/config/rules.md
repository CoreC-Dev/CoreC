---
title: 规则配置
description: CoreC 规则引擎配置参考，包含匹配表达式语法、动作类型、目标路由、优先级与数据变换。
---

# 规则配置

`rules` 段定义数据路由与处理规则。每个数据点在采集后按规则列表顺序求值匹配，命中则执行对应动作（转发、丢弃、告警、镜像、变换）。

```yaml
rules:
  - name: high-temp-alert
    match: "tag == 'temperature' && value > 95"
    action: alert
    target: cloud-mqtt
    priority: 1
```

## 规则通用字段

对应核心 `RuleConfig` 结构。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `name` | string | **是** | — | 规则名称，用于日志与统计引用 |
| `match` | string | **是** | — | 匹配表达式，见[匹配表达式语法](#匹配表达式语法) |
| `action` | string | **是** | — | 命中后执行的动作类型 |
| `target` | string | 条件必填 | — | 单目标动作的目标传输名 |
| `targets` | string[] | 条件必填 | — | `mirror` 动作的多个目标传输名列表 |
| `priority` | int | 否 | `0` | 优先级，数值越小优先级越高 |
| `transform` | object | 否 | — | 数据变换配置，仅 `transform` 动作使用 |

::: info
规则按 `priority` **升序**求值（数值小 = 优先级高）。首个命中的规则执行其动作后，数据点即结束本次规则求值流程，不再继续匹配后续规则。因此请将高优先级、强条件的规则放在前面。
:::

---

## 匹配表达式语法

`match` 字段是一个类 C/Go 风格的布尔表达式，对每个 `DataPoint` 求值。表达式可引用数据点字段、字面量，并使用比较与逻辑运算符。

### 可用字段

表达式可引用以下数据点字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `driver` | string | 驱动实例名 |
| `device` | string | 设备名 |
| `group` | string | 标签分组 |
| `tag` | string | 标签名 |
| `value` | any | 数据值（按标签类型解释为数值或布尔） |
| `type` | string | 数据类型名（如 `float32`） |
| `quality` | string | 数据质量：`good` / `bad` / `uncertain` |

### 运算符

| 类别 | 运算符 | 说明 |
| --- | --- | --- |
| 比较 | `==` `!=` `>` `>=` `<` `<=` | 数值与字符串均支持 `==` `!=`，大小比较仅限数值 |
| 正则 | `=~` `!~` | 正则匹配 / 不匹配（RE2），左操作数为字符串字段 |
| 字符串 | `contains` `suffix` `prefix` | 包含 / 后缀 / 前缀匹配，如 `tag contains 'motor'` |
| 范围 | `in` | 数值区间检查（闭区间），如 `value in 50..100` |
| 逻辑 | `&&` `\|\|` `!` | 与、或、非 |
| 分组 | `( )` | 括号改变优先级 |

### 字面量

| 类型 | 写法 | 示例 |
| --- | --- | --- |
| 字符串 | 单引号包裹 | `'temperature'` |
| 数值 | 数字字面量 | `95`、`0.2`、`-10` |
| 布尔 | `true` / `false` | `value == true` |

### 特殊匹配

| 取值 | 含义 |
| --- | --- |
| `ALL` | 匹配所有数据点，常用于兜底规则 |
| `RULE-SET:name` | 委托给名为 `name` 的外部规则提供者（`rule-providers`）匹配 |
| `SUB-RULE:name` | 委托给名为 `name` 的子规则组（`rule-groups`）匹配 |

### 示例

```yaml
# 按标签名 + 数值阈值
match: "tag == 'temperature' && value > 95"

# 按分组
match: "group == 'reactor'"

# 按驱动 + 质量
match: "driver == 'plc-modbus' && quality != 'good'"

# 布尔标签
match: "tag == 'emergency_stop' && value == true"

# 复合条件
match: "(group == 'sensors' || group == 'reactor') && value > 80"

# 正则匹配
match: "tag =~ '^reactor_.*'"

# 字符串包含 / 前缀
match: "tag contains 'motor' && driver prefix 'siemens'"

# 数值区间
match: "value in 50..200"

# 委托外部规则集
match: "RULE-SET:safety-rules"

# 兜底
match: "ALL"
```

::: tip
字符串比较使用单引号 `'...'`。表达式中的 `&&`、`||` 在 YAML 中无需转义，但若整行表达式含特殊字符，建议用双引号包裹整个 `match` 值。
:::

---

## 动作类型（action）

| `action` | 说明 | 需要的字段 |
| --- | --- | --- |
| `forward` | 转发到单个目标传输 | `target` |
| `drop` | 丢弃该数据点，不再传输 | 无 |
| `alert` | 转发到目标传输并触发告警回调 | `target` |
| `transform` | 对数据点做变换后转发 | `target` + `transform` |
| `mirror` | 镜像转发到多个目标传输 | `targets` |

### forward

将数据点发送到指定的单个传输。最常用的动作。

```yaml
- name: default-catch-all
  match: "ALL"
  action: forward
  target: mes-http-push
  priority: 999
```

### drop

丢弃匹配的数据点，常用于过滤无效或噪声数据。

```yaml
- name: drop-bad-quality
  match: "quality == 'bad'"
  action: drop
  priority: 1
```

### alert

转发数据点到目标传输，同时触发核心告警回调（可通过 API 订阅告警事件）。用于越限、急停等需即时通知的场景。

```yaml
- name: high-temp-alert
  match: "tag == 'temperature' && value > 95"
  action: alert
  target: cloud-mqtt
  priority: 1
```

::: info
`alert` 与 `forward` 的区别在于：`alert` 会额外在核心告警通道产生一条事件（含规则名、匹配表达式、数据点快照），便于运维侧实时感知；数据点本身仍会正常发送到 `target`。
:::

### transform

对数据点执行变换后转发。变换规则定义在 `transform` 子对象中。

```yaml
- name: celsius-to-fahrenheit
  match: "tag == 'temperature'"
  action: transform
  target: cloud-mqtt
  priority: 5
  transform:
    expression: "value * 1.8 + 32"
    tag-rename: "temperature_f"
```

### mirror

将同一数据点复制并分别发送到 `targets` 列表中的每个传输，实现多目的地冗余分发。

```yaml
- name: reactor-mirror
  match: "group == 'reactor'"
  action: mirror
  targets:
    - cloud-mqtt
    - mes-http-push
  priority: 10
```

::: warning
`mirror` 会将数据点复制 N 份（N = `targets` 长度），上游带宽与目标存储成本随之成倍增加。请仅在确有冗余或分流需求时使用。
:::

---

## target 与 targets

| 动作 | 使用字段 | 说明 |
| --- | --- | --- |
| `forward` / `alert` / `transform` | `target` | 单个传输名，必须与 `transports[].name` 一致 |
| `mirror` | `targets` | 传输名列表，每项必须存在 |
| `drop` | 无 | 不需要目标 |

```yaml
# 单目标
target: cloud-mqtt

# 多目标（mirror 专用）
targets:
  - cloud-mqtt
  - mes-http-push
```

::: tip
若 `target` / `targets` 引用了不存在的传输名，核心启动时会记录 `error` 并禁用该规则，不影响其他规则运行。
:::

---

## priority

规则求值顺序由 `priority` 决定：

- **数值越小，优先级越高**，越先求值；
- 首个命中的规则执行动作后，数据点结束本次规则流程；
- 未命中任何规则的数据点**将被丢弃**，因此建议始终配置一条 `match: "ALL"` 的兜底规则。

```yaml
rules:
  - name: critical-alert      # priority: 1  最先求值
    match: "tag == 'emergency_stop' && value == true"
    action: alert
    target: cloud-mqtt
    priority: 1

  - name: reactor-mirror      # priority: 10
    match: "group == 'reactor'"
    action: mirror
    targets: [cloud-mqtt, mes-http-push]
    priority: 10

  - name: default-catch-all   # priority: 999 兜底
    match: "ALL"
    action: forward
    target: mes-http-push
    priority: 999
```

::: warning
**未匹配任何规则的数据点会被丢弃。** CoreC 默认不转发（不同于代理工具的默认直连行为），强制显式声明路由意图。务必保留一条低优先级 `ALL` 兜底规则。
:::

---

## transform（数据变换）

`transform` 子对象定义数据变换规则，对应核心 `TransformConfig` 结构。仅 `action: transform` 时生效。

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `expression` | string | **是** | — | 值变换表达式，对 `value` 求值后替换原值 |
| `tag-rename` | string | 否 | — | 重命名后的标签名，留空则保持原名 |

### expression

对数据点的 `value` 执行的表达式，可引用 `value` 与数值字面量，支持四则运算与括号：

| 运算符 | 说明 |
| --- | --- |
| `+` `-` `*` `/` | 四则运算 |
| `( )` | 括号分组 |

```yaml
transform:
  expression: "value * 1.8 + 32"     # 摄氏转华氏
```

```yaml
transform:
  expression: "(value - 0) * 100 / 65535"   # 16 位原始值转 0-100 百分比
```

### tag-rename

变换后的数据点可重命名标签，便于下游区分原始量与工程量：

```yaml
- name: temp-unit-convert
  match: "tag == 'temperature'"
  action: transform
  target: cloud-mqtt
  priority: 5
  transform:
    expression: "value * 1.8 + 32"
    tag-rename: "temperature_f"   # 下游收到的 tag 名变为 temperature_f
```

::: info
`transform` 先执行 `expression` 计算新值，再应用 `tag-rename`。若仅需重命名而不改值，可写 `expression: "value"`。
:::

---

## 完整示例

```yaml
rules:
  # 1. 高温告警：立即转发到云 MQTT 并触发告警
  - name: high-temp-alert
    match: "tag == 'temperature' && value > 95"
    action: alert
    target: cloud-mqtt
    priority: 1

  # 2. 急停告警：高优先级
  - name: emergency-stop-alert
    match: "tag == 'emergency_stop' && value == true"
    action: alert
    target: cloud-mqtt
    priority: 2

  # 3. 丢弃坏质量数据
  - name: drop-bad-quality
    match: "quality == 'bad'"
    action: drop
    priority: 3

  # 4. 温度单位换算后转发
  - name: celsius-to-fahrenheit
    match: "tag == 'temperature'"
    action: transform
    target: cloud-mqtt
    priority: 5
    transform:
      expression: "value * 1.8 + 32"
      tag-rename: "temperature_f"

  # 5. 反应器数据镜像到 MQTT 与 MES 双通道
  - name: reactor-mirror
    match: "group == 'reactor'"
    action: mirror
    targets:
      - cloud-mqtt
      - mes-http-push
    priority: 10

  # 6. 兜底：其余全部转发到 MES HTTP
  - name: default-catch-all
    match: "ALL"
    action: forward
    target: mes-http-push
    priority: 999
```
