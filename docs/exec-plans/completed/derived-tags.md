# 执行计划：派生/虚拟标签（Derived Tags）

> 状态：待执行 · 关联功能：边缘计算 KPI/OEE · 业务行为影响：**变更行为**（新增可观测数据点，需显式登记 R2）
> 跨仓库：CoreC-Dashboard 对应计划见 `CoreC-Dashboard/docs/exec-plans/completed/derived-tags.md`

## 1. 目标

配置驱动定义**计算点位**（`效率 = output_power / input_power * 100`、`OEE = availability * performance * quality`、`温差 = T1 - T2`），引擎按周期求值后构造 `core.DataPoint` 推入 DataBus，自动流经规则引擎与北向传输——无需额外接线即可被 MQTT 转发、被告警规则匹配、被（未来的）Historian 存储。

## 2. 背景与缺口

- 现有 `transform` 规则动作（`engine/publish.go:16 applyTransform`）只能对**单个**点的 `point.Value` 做算术变换 + 改名，**不能跨点引用**。
- 无 `derived-tags` 配置段，无法表达"由若干已采集 tag 计算出的新 tag"。
- 控制工程师高频诉求：OEE / 能耗 / 效率 / 温差等 KPI 需在边缘侧算出，而非依赖上层 MES。

## 3. 设计

### 3.1 归属层：engine 组件（非 driver）

不做成 `driver/virtual`：driver 只能依赖 `core`/`common`（ARCHITECTURE 依赖方向），无法访问引擎的 `LatestCache`。做成 **engine 内部组件** `engine/derived.go`，与 `scheduler`/`batcher`/`discovery` 同级——engine 持有 `cache *LatestCache` 与 `dataBus *DataBus`，天然可读缓存、可推点。

### 3.2 求值数据源：`LatestCache.GetAll()`

`engine/cache.go:134 GetAll() map[string]DataPoint` 返回**跨所有驱动、按 tag 名索引**的最新值快照（64 分片 atomic.Pointer，无锁廉价）。派生 tag 的 expr 中标识符 = tag 名，求值时从该快照取值。

> 已知语义边界（须写入文档）：派生值基于各引用 tag 的**最新缓存**，非同一采样瞬时。对 KPI 通常可接受；需高频同瞬时对齐的场景不适用。

### 3.3 表达式引擎：复用 expr-lang/expr

`rule/expr.go` 已用 `github.com/expr-lang/expr`。派生 tag 编译期：
- 用 `expr.Compile(expr, expr.Env(env), expr.AllowUndefinedVariables())` 编译，env = `map[string]float64`（引用 tag 名 → 值）。
- **安全白名单**：禁函数调用（`expr.WithNoFunctions()` 或等价），只允许算术 + 比较运算符。防止任意表达式副作用。
- 编译失败 → 启动报错（fail-closed，对齐 core-beliefs 的安全默认）。

### 3.4 配置 schema（新顶层段）

```yaml
derived-tags:
  - name: efficiency
    expr: "output_power / input_power * 100"
    type: float32           # DataType: bool|uint16|int16|...|float32|float64
    group: kpi              # 可选，DataPoint.Group
    interval: 5s            # 求值周期
    unit: "%"               # 可选，写入 Metadata["unit"]
    on-missing: skip        # skip | bad-quality | zero
    stale-guard: 30s        # 引用 tag 超 30s 未更新 → 标记 stale（0 = 不检测）
```

- `on-missing`：任一引用 tag 在缓存中不存在时——`skip`（不发点）/ `bad-quality`（发 QualityBad 点）/ `zero`（当 0 求值）。
- `stale-guard`：引用 tag 的 `DataPoint.Timestamp` 距今超过该阈值 → 派生点 `IsStale=true` 或 QualityUncertain（复用现有 stale 概念）。
- 除零 / 溢出：求值返回 NaN → 按 `on-missing` 同策略处理（默认 QualityBad，不 panic）。

### 3.5 核心类型变更

`core/config.go`（或 config 包的 Config 结构）新增字段：
```go
type DerivedTagConfig struct {
    Name       string        `yaml:"name"`
    Expr       string        `yaml:"expr"`
    Type       DataType      `yaml:"type"`
    Group      string        `yaml:"group,omitempty"`
    Interval   string        `yaml:"interval"`
    Unit       string        `yaml:"unit,omitempty"`
    OnMissing  string        `yaml:"on-missing,omitempty"`   // 默认 skip
    StaleGuard string        `yaml:"stale-guard,omitempty"`
}
// Config.DerivedTags []DerivedTagConfig `yaml:"derived-tags,omitempty"`
```

### 3.6 生命周期接线

- `engine.Start`：若 `config.DerivedTags` 非空，构造 `*derivedTagEvaluator`，编译所有 expr，按各自 interval 起 `time.Ticker` goroutine（绑定 `e.ctx` 取消 + `e.wg`）。
- 每个 tick：`cache.GetAll()` → 取引用值 → 求值 → 构造 `DataPoint{Driver:"derived", Tag:name, Group:, Value:, Type:, Quality:, Timestamp:time.Now(), Metadata:{unit:}}` → `dataBus.Push(point)`。
- `engine.Stop`：取消 ctx，wg.Wait 收集 goroutine（对齐现有关停顺序）。
- `engine.Reload`：热重载时停止旧 evaluator、启动新配置（与 scheduler 热重载同模式）。

## 4. 批次分解（单一职责提交）

| 批次 | 范围 | 文件 | 验收 |
|---|---|---|---|
| **D1** | core 类型 + config 解析 | `core/config.go`(或 config 包) 加 `DerivedTagConfig`/`DerivedTags`；`config/validate.go` 校验（name 唯一、expr 非空、interval 合法、on-missing 枚举） | `go test ./config/...` 通过；非法配置被拒 |
| **D2** | evaluator 实现 | 新 `engine/derived.go`：编译 + 周期求值 + push；`engine/derived_test.go`：多引用求值、缺 tag（skip/bad/zero）、除零、stale guard、expr 安全白名单拒危险函数 | `go test -race ./engine/...` 通过；goleak 无泄漏 |
| **D3** | 生命周期接线 | `engine/engine.go` Start/Stop/Reload 接 evaluator；`engine_lifecycle_test.go` 加启停/热重载测试 | 启停零 goroutine 泄漏；Reload 切换配置生效 |
| **D4** | 配置示例 + 文档 | `config.example.yaml` 加 `derived-tags` 段示例；`docs/config/` 加派生标签参考；`README.md` 特性列表追加 | `scripts/check-docs.sh` 无死链；示例可被 validate 通过 |

> R2 约束：本功能为**变更行为**（新增可观测数据点）。D1–D4 中纯新增代码不改变现有行为；仅当用户配置 `derived-tags` 段时才产生新点。须在提交信息标注 `feat(derived-tags): ...` 并在台账登记"变更行为"。

## 5. DoD

- [ ] `derived-tags` 段可配置 ≥3 个派生 tag，含跨驱动引用、除零、缺 tag 三种场景，均按 `on-missing` 策略正确产出。
- [ ] expr 危险函数（如 `load()`、`exec()`）在编译期被拒。
- [ ] 派生点经规则引擎与 MQTT 传输正常流转（端到端测试 `e2e/`）。
- [ ] `go test -race ./...` 全绿；新增文件 gocyclo <15、行数 <400。
- [ ] `golangci-lint run` 无新增告警；`archtest` 依赖方向通过（engine 不反向依赖）。
- [ ] 文档同步：config 参考 + README 特性 + 本计划移入 `completed/`。

## 6. 风险与缓解

| 风险 | 缓解 |
|---|---|
| expr 安全漏洞（任意表达式） | 编译期白名单禁函数；fuzz test 验证危险输入被拒 |
| 缓存读非同瞬时 | 文档显式声明语义边界；stale-guard 提供时效保障 |
| 高频派生 tag 加压 DataBus | interval 下限校验（如 ≥1s）；DataBus 满时派生点可降级为 QualityUncertain |
| Reload 期间 evaluator 切换竞态 | 复用 scheduler Reload 的 mu + ctx cancel 模式；-race 测试覆盖 |

## 7. 依赖

- `github.com/expr-lang/expr`（已在 go.sum，rule 包使用）。
- 无新外部依赖。
