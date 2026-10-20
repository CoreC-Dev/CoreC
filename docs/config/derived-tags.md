---
title: 派生标签配置
description: CoreC 派生标签（Derived Tags）配置参考，通过算术表达式从缓存值计算 KPI 指标。
---

# 派生标签配置

`derived-tags` 段定义从已采集标签的算术表达式计算的虚拟标签（KPI）。派生标签由引擎周期性求值，结果推入数据总线和缓存，可被规则引擎和传输层消费。

```yaml
derived-tags:
  - name: oee
    expr: "availability * performance * quality"
    type: float32
    interval: 5s
    unit: "%"
    on-missing: skip
    stale-guard: 30s
```

## 字段说明

| 字段 | 类型 | 必需 | 说明 |
|------|------|------|------|
| `name` | string | 是 | 派生标签名，必须唯一 |
| `expr` | string | 是 | 算术表达式，标识符解析为缓存中的标签名 |
| `type` | DataType | 是 | 结果数据类型（int16/int32/float32/float64/bool 等） |
| `group` | string | 否 | 标签分组 |
| `interval` | duration | 是 | 求值周期（≥ 1s），如 `5s`、`1m` |
| `unit` | string | 否 | 工程单位 |
| `on-missing` | enum | 否 | 引用标签缺失时的行为：`skip`（默认）、`bad-quality`、`zero` |
| `stale-guard` | duration | 否 | 引用标签超过此时间未更新则标记 stale |

## 表达式语法

使用 [expr-lang/expr](https://github.com/expr-lang/expr) 表达式引擎，支持：

- 算术运算：`+`、`-`、`*`、`/`、`%`
- 比较运算：`==`、`!=`、`<`、`>`、`<=`、`>=`
- 逻辑运算：`&&`、`||`、`!`
- 条件表达式：`if(cond, a, b)`
- 数学函数：`abs()`、`ceil()`、`floor()`、`round()`、`max()`、`min()`、`len()`

表达式中的标识符（如 `availability`）解析为缓存中同名标签的值。跨驱动标签通过 `LatestCache.GetAll()` 聚合，以标签名为键。

::: warning 跨驱动同名标签：用限定引用（audit PIPE-2）
上面的扁平解析在**多个驱动存在同名标签**时是不确定的——`temp` 读到的是"最后写入"的那个驱动的值。用 `驱动名.标签名` 的限定引用精确点名：

```yaml
- name: spread
  expr: "plc1.temp - plc2.temp"   # 各自读各自驱动，不受同名冲突影响
  interval: 5s
```

限定引用参与 on-missing 策略与 stale-guard 的方式与扁平引用完全一致。`derived.kpi1` 还可以引用其它派生标签（求值时读的是它们的最新缓存值）。
:::

## on-missing 策略

| 策略 | 行为 |
|------|------|
| `skip` | 跳过本次求值，不产生数据点（默认） |
| `bad-quality` | 缺失变量以 0 代入，结果标记 `QualityBad` |
| `zero` | 缺失变量以 0 代入，正常求值 |

## 典型用例

### OEE（设备综合效率）

```yaml
derived-tags:
  - name: oee
    expr: "availability * performance * quality / 10000"
    type: float32
    interval: 5s
    unit: "%"
    on-missing: skip
```

### 能效比

```yaml
derived-tags:
  - name: cop
    expr: "cooling_capacity / power_input"
    type: float32
    interval: 10s
    on-missing: bad-quality
    stale-guard: 60s
```

### 温差

```yaml
derived-tags:
  - name: temp_diff
    expr: "temp_inlet - temp_outlet"
    type: float32
    interval: 2s
    unit: "°C"
```

::: tip
派生标签可与规则引擎组合：派生标签 `oee` → 规则 `oee < 0.85` → `forward` → notify-slack，实现 KPI 阈值告警全链路。
:::
