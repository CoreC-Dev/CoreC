# 架构决策记录 (ADR)

> 本文件记录 CoreC 关键设计决策的**上下文、选择与后果**，帮助接手者理解"为什么这么做"而非"怎么做"。
> 代码注释记录局部动机，这里集中记录跨模块、影响演进方向的取舍。
> 状态：`已采纳` / `部分采纳` / `已取代` / `待定`。

---

## ADR-001 六边形架构：core/ 零外部依赖

- **状态**：已采纳
- **上下文**：工业网关需同时支持多种南向协议（Modbus/S7/OPC UA）与北向传输（MQTT/HTTP），且要能作为库嵌入上层系统。若协议库与领域逻辑耦合，替换协议或被嵌入时牵一发动全身。
- **决策**：`core/` 为纯契约层，只依赖标准库；`driver/*`、`transport/*` 为适配器，依赖向内（适配器→core，反之禁止）。新协议通过 `init()` 工厂注册 + `all/all.go` 空白导入聚合，零侵入扩展。
- **后果**：协议替换/新增不影响引擎；`core` 接口是稳定契约，扩接口属破坏性变更（见 CONTRIBUTING §2.3）。代价是 `core` 不能认识协议特有概念，协议特有参数走 `Settings map[string]any`。

## ADR-002 规则匹配：优先级全局有序 + 统计完整性优先（驱动索引导出的受控扫描）

- **状态**：已取代（原「纯线性扫描」方案已被驱动索引取代；两条不变量保留）
- **上下文**：`rule/engine.go` 的 `Match` 曾对每个数据点线性遍历全部规则。完全索引会破坏两个不变量：(1) 规则按优先级全局有序——分桶索引后跨桶无法保证首个命中是全局最高优先级；(2) `ruleWrapper.Match` 在每次评估时更新 hit/miss 统计，索引跳过的规则将丢失 miss 统计。
- **决策**：保留「优先级全局有序 + 统计完整性」两条不变量；在实现上，`SetRules` 用 `sort.SliceStable` 稳定排序（rule/rule_build.go:101），并把带 `driver == 'x'` 顶级约束的规则装入按驱动分桶的索引（rule/rule_build.go:115-133）。`Match` 用双指针把「驱动桶 + 通用规则」两个有序列表在线合并成全局优先级顺序求值（rule/rule_match.go:38-75）；被索引跳过的规则经 `RecordMiss` 显式记 miss（rule/engine.go:150-167），统计语义与线性扫描一致。
- **后果**：单点评估成本从 O(全部规则) 降到 O(相关规则)，同时优先级次序与 hit/miss 统计不变形。同优先级规则的求值顺序由构造序号 seq 决胜（配置顺序），通用规则不再隐式遮蔽同优先级的 driver 规则。无索引可用时（子规则引擎等）回退线性扫描（rule/engine.go:124-126）。

## ADR-003 core.Logger / core.Metrics 端口注入

- **状态**：已采纳（驱动侧注入已落地；引擎内部日志仍走全局 `log/slog`，见 ADR-008 的剩余范围）
- **上下文**：驱动与核心组件早期直接使用全局 `log/slog` 与包级单例计数器，多实例嵌入时日志与统计混在一起、无法替换后端。`core.Logger`/`core.Metrics` 端口先行定义以确立契约。
- **决策**：
  - **驱动侧注入（已落地）**：`AddDriver` 对实现了 `SetLogger`/`SetMetrics` 可选接口的驱动注入后端——Logger 注入 `log.DefaultLogger()`（slog 适配器，log/logger_adapter.go:35），Metrics 注入引擎的共享 registry `stats.driverMetrics`（engine/driver_manager.go:70-78）。三个南向驱动经 `driverbase.BaseDriver` 持有 Logger（driverbase/driverbase.go:80-82）；驱动发射的协议级指标经 `core.DriverMetricsProvider` 角色进入 `/metrics`（engine/stats_collector.go:209-220）。
  - **引擎统计去单例（已落地）**：早期 `statistic.Manager` 包级单例方案已不存在；引擎统计集中在 `statsCollector` 角色结构（engine/stats_collector.go:20-56），含原子计数器、窗口化速率（`metrics.RateWindow`）与标注延迟直方图（`labeledLatencyStore`），每实例独立。
- **后果**：驱动可独立测试（注入 Noop/捕获后端）、多实例嵌入不共享计数器；驱动协议指标（modbus 异常码等）可流入 `/metrics`。引擎内部组件（scheduler/batcher 等）仍直接调用全局 `slog`——多实例嵌入时引擎日志无法按实例隔离，剩余范围由 ADR-008 跟踪。

## ADR-004 OfflineBuffer 崩溃安全写入（fsync + 目录 fsync + 原子 rename）

- **状态**：已采纳
- **上下文**：传输中断时数据落盘缓存，恢复后补传。若写入不崩溃安全，断电可能丢失已"落盘"数据或留下半写文件。
- **决策**：`engine/offlinebuffer.go` 采用 write→fsync(数据)→close→rename→fsync(目录) 的原子写入序列。目录 fsync 确保 rename 的目录项更新持久化，否则断电后文件内容持久但 rename 不可见。
- **后果**：严格的崩溃一致性；在 journaling 文件系统（ext4 data=ordered/xfs）上目录 fsync 通常冗余但保留以求严格。代价是每次落盘两次 fsync，吞吐受限于磁盘——可接受，因 OfflineBuffer 仅在传输中断时启用。

## ADR-005 断路器冷却期持续探活（间隔粗但不停止）

- **状态**：已采纳
- **上下文**：驱动/传输连续失败达阈值后进入断路器冷却，避免雪崩式重连。早期文档曾误述为"冷却期不探活"。
- **决策**：`common/util/util.go` 的 `ReconnectLoopWithBreakerCounted` 在触发断路器后仅将 backoff 切换为固定 5 分钟，**循环继续运行**——每次 5 分钟后仍调用 `connect()` 探活，成功即恢复。即冷却期"有探活、仅间隔粗"。
- **后果**：设备恢复后最坏需等约 5 分钟才重连成功。若场景需更快恢复，可改小 `common/util` 的 `defaultCircuitBreakerBackoff` 常量（当前为包内固定值 5 分钟，非运行时可配）或改为递减间隔。当前工业场景 5 分钟可接受。

## ADR-006 DataBus 弃旧策略（非阻塞、弃最旧）

- **状态**：已采纳
- **上下文**：生产者（驱动采集）快于消费者（worker 处理）时，DataBus 主通道会满。阻塞生产者会拖慢采集周期；丢弃新数据会丢失最新值。
- **决策**：`engine/databus.go` 的 `Push` 用非阻塞 select，通道满时**丢弃最旧的一条**腾位给最新，并计 `pushDropped`。最新值优先于旧值，符合工业监控"宁要新不要旧"的语义。
- **后果**：消费者持续跟不上时旧数据被弃，`pushDropped` 反映背压程度。控制类指令不走 DataBus（走独立 commandCh），故弃旧不影响控制。未来若在 DataBus 承载控制反馈流，需为该流提供独立的背压策略。

## ADR-007 Modbus 批读合并（贪心合并连续/重叠地址，上限 125 寄存器）

- **状态**：已采纳
- **上下文**：Modbus 单次读最多 125 寄存器（协议限制）。配置中相邻地址的 tag 若逐个读取，RTT 累积大。
- **决策**：`driver/modbus` 的 `performBatchReads`（modbus_read.go:382）按 area 分组、按起始地址排序后，用 `mergeBatches`（modbus_read.go:516）贪心合并连续或重叠地址到同一请求，单请求不超过 `modbusMaxBatchSize = 125`（modbus_base.go:47，经 `Capabilities().MaxBatchSize` 生效）；批量读失败（重试后）的 tag 回退逐个读。
- **后果**：大幅减少 Modbus RTT，提升高频采集吞吐。合并只针对可合并的连续/重叠地址，离散地址仍独立读。

## ADR-008 引擎内部日志未走 core.Logger 端口（驱动侧已注入）

- **状态**：部分采纳（驱动侧注入已完成，见 ADR-003；引擎内部改造仍延迟）
- **上下文**：`core.Logger` 端口与 Noop 实现早已定义，驱动已通过 `SetLogger` 收到注入的 Logger；但引擎内部（scheduler/batcher/discovery/transport_manager 等角色）仍直接调用全局 `log/slog`。
- **决策**：引擎内部改造延迟实施。原因：(1) 当前引擎包内非测试代码有约 113 处 `slog.X` 调用，分布在 17 个文件（runtime.go/scheduler.go/discovery.go/transport_manager.go/batcher.go/offlinebuffer.go/write_manager.go 等）；(2) `core.Logger` 接口签名（`Debug/Info/Warn/Error(msg string, args ...any)`）与 `slog` 的 KV args 语义一致（`SlogLogger` 适配器已存在），真正的工程量在于把 Logger 实例穿过 9 个角色结构的构造与转发链；(3) 引擎日志当前统一经 `log.Init` 安装的 ObservableHandler 进入事件总线，改造的收益（按实例隔离）只在多实例嵌入场景兑现。
- **后果**：多实例嵌入时引擎内部日志无法按实例隔离（已知技术债）；驱动侧已无此问题。端口契约稳定（core.Logger 不需变更），未来注入时只需改引擎内部接线。

## ADR-009 端到端延迟追踪：pipeline 直方图已实现，OTLP 导出与 trace 贯穿延迟

- **状态**：部分采纳
- **上下文**：端到端延迟追踪要求，包含三部分：(1) `corec_pipeline_latency_seconds` 直方图；(2) OTLP 导出接 Jaeger/Tempo；(3) DataPoint 透传 trace context。
- **决策**：
  - **pipeline 延迟直方图**：已实现。数据年龄以 `corec_data_age_seconds` 记录 Publish 时刻与 `DataPoint.Timestamp` 的差值（采集到发布的端到端延迟）。观测点覆盖：批量路径在 `publishWithRetryAndBuffer` 内**每次尝试、逐点**观测（engine/batcher_retry.go:34-39，重试期间年龄增长被如实记录）；直发路径在 `publishToTargets` 观测（engine/processing.go:347-348）；fallback 直发路径在 `tryFallback` 观测（engine/transport_manager.go:313-315）。直方图由 `labeledLatencyStore` 持有：全局聚合 + 按传输标注两份（engine/labeled_latency.go:105-108），经 `core.LabeledLatencyProvider` 角色暴露（route 层优先读取标注版，无角色时回退 `core.LatencyProvider`/`core.DataAgeProvider` 的无标注版，hub/route/metrics_engine.go:190-208）。读取延迟同理按驱动标注（`corec_read_latency_seconds{driver=...}`）。
  - **OTLP 导出**：延迟实施。当前 `common/trace` 的 span 仅以 `slog.Debug` 记录（debug 关闭时为共享 noopSpan，common/trace/trace.go:92-95）。落地 OTLP 导出属运维基础设施配置（部署 Jaeger/Tempo + 配置 OTLP endpoint），非代码架构变更。
  - **DataPoint trace context 贯穿**：延迟实施。在 `core.DataPoint` 增加 trace 字段属 core 契约变更（虽为新增可选字段、非破坏性，但需评估序列化影响和 hexagonal 边界）。当前 trace 主要在 HTTP 入口（`hub/route` 经 `trace.Middleware` 解析 traceparent 并生成 W3C trace ID），未从采集端贯穿到 Publish 端。
- **后果**：用户最关心的"数据有多新鲜"已可通过 `corec_data_age_seconds` 直方图量化回答（p50/p99/p999，含按传输细分）。全链路 trace 可视化需待 OTLP 基础设施部署。

## ADR-010 规则引擎 `continue` 标记：多规则匹配（iptables 风格）

- **状态**：已采纳
- **上下文**：原规则引擎采用首次匹配即生效（first-match-wins）语义——`Match` 返回单个 `*RuleMatchResult`，第一条命中规则执行后立即停止。这无法表达"告警后继续正常转发"等复合场景：要么用 alert 规则"吃掉"数据点（下游收不到），要么不告警。用户需在一条数据点上触发多个动作（告警 + 转发、变换 + 路由、多级告警），但旧语义下只能选一个。
- **决策**：为 `RuleConfig` 增加 `Continue bool` 字段（YAML `continue`，默认 `false`）。`Match` 返回类型改为 `[]*RuleMatchResult`——引擎按优先级遍历，命中规则加入结果集，若 `!r.Continue()` 则终止并返回全部结果，否则继续评估。`engine/processing.go` 逐条执行结果集中的动作；`transform` 动作对 `point` 的修改对后续规则的**发布数据**可见（但不影响匹配，见下）。配置校验拒绝 `drop` + `continue: true`（丢弃意味着停止，继续评估矛盾）。
- **后果**：
  - **向后兼容**：`continue` 默认 `false` → 所有规则均为终止规则 → 结果集至多 1 个元素 → 与旧的 first-match-wins 完全等价。现有配置无需修改。
  - **接口变更**：`core.RuleEngine.Match` 签名从 `*RuleMatchResult` 改为 `[]*RuleMatchResult`，`core.Rule` 接口新增 `Continue() bool` 方法。`core.RuleManager`（hub 使用的子集）不含 `Match`，故 hub mock 不受影响。
  - **统计语义**：miss 统计基于"最后一条终止规则的优先级"——若所有命中规则均 `continue: true`（无终止），则所有其他驱动的规则记 miss；否则仅优先级低于终止规则的规则记 miss。
  - **性能**：`continue: true` 规则增多时单点评估次数上升，但工业场景规则数小（<100），无感知。
  - **局限性（transform 不影响后续匹配）**：`Match()` 对所有规则求值时使用的是**原始** `DataPoint`，`transform` 动作在结果集遍历时才修改 `point`——因此 transform 后续规则的**发布数据**看到变换后的值，但后续规则的**匹配条件**已在原始值上求值完毕。例如 `transform(tag: temp_c→temp_f, continue: true)` 后跟 `match: tag == 'temp_f'` 不会命中。这是"批量匹配 + 事后执行"设计的固有取舍：将匹配与执行交错可解决，但会模糊规则引擎与处理流水线的边界并绕过驱动索引优化。当前设计支持所有常见场景（alert+forward、多级告警、transform+兜底转发），transform-then-conditional-route 可用 `match: ALL` 规避。

## ADR-011 CORS 默认维持 `*`（直连形态兼容 + 文档引导收紧，方案 A+C）

- **状态**：已采纳（2026-10-17 项目所有者批准方案 A+C；零行为变更，纯文档 + 配置样例注释）
- **上下文**：`hub/route/middleware.go` 的 `corsMiddleware` 在未配置 `global.api.allowed-origins`（`core/engine.go` `APIConfig.AllowedOrigins`，yaml 键 `allowed-origins`）时输出 `Access-Control-Allow-Origin: *`，并放行方法 `GET, POST, PUT, PATCH, OPTIONS` 与头 `Content-Type, Authorization`；代码注释明示意图是浏览器 Dashboard "can connect out of the box"。这是刻意设计而非疏漏，且**不是无认证裸奔**——配套防线全部在位（均经实测核实）：
  - **认证恒在**：全部业务端点位于 auth group 内（未认证面仅 `/`、`/version`、`/healthz/*` 探针）；空 secret fail-closed；token 与 secret 先 `sha256` 定长化再 `hmac.Equal` 恒时比较。无 token 的跨域页面只能拿到 401。
  - **token 传递面收窄**：HTTP 只认 `Authorization: Bearer`；`?token=` 仅允许 WS upgrade（浏览器 WS API 不能设自定义头）；`safeRequestLogger` 对 query token 脱敏。
  - **限流不信任转发头**：按直连 `RemoteAddr` 令牌桶限流，注释明示 `X-Forwarded-For` 可伪造、trusted-proxy 支持须显式 opt-in。
  - **无 cookie**：凭据走 header，浏览器不会自动附带——经典 CSRF（cookie 自动携带）模型不适用。
  - WebSocket upgrade 的 Origin 校验（`hub/route/common.go`）与 CORS 共用同一 allowlist，未配置时同样放行。
  Dashboard 有两种已文档化的部署形态：**同源反代**（Dashboard `server.mjs` 把 `/corec-api/*` 反代到 CoreC，注释明言 "Eliminates CSP, CORS, and port-blocking issues"）不依赖宽松 CORS；**直连模式**（静态 SPA——如 GitHub Pages——直接访问远程 CoreC，Dashboard `deploy.yml` 明确描述该架构）**必须**允许跨域，否则浏览器无法直连后端。收紧默认值会让直连形态开箱即坏。
  真实残余风险是"**token 泄露一次即放大为跨域可读写**"：带 `Authorization` 头的请求在 CORS 语义里不算 credentialed request，`*` 下响应体可被任意网页读取——比"只能写不能读"更宽。早期审计文档曾把该默认表述为"开箱即用但裸奔"，不准确，以本 ADR 为准修正。
- **决策**：采 **A + C**——维持默认 `*`（方案 A），叠加文档加固（方案 C）：在 `config.example.yaml`、`docs/config/global.md` 与 `docs/guide/installation.md` 写明"何时应收紧"。**收紧方式即 `global.api.allowed-origins` 配置**：一旦列出来源，中间件仅对列表内来源反射 `Origin`（限制模式，WS 同步收紧），未配置则维持 `*`。备选评估：**B**（默认收紧为同源）属破坏性变更，直连形态开箱即坏——若确认直连仅用于受控内网/演示，按流程另立 issue 显式评估；**D**（trusted-proxy 按真实 IP 限流）为独立议题，不与本决策绑定。
- **后果**：直连形态保持开箱可用；生产公网直连部署应把 Dashboard 域名配入 `allowed-origins`；同源反代部署天然无 CORS 需求——**生产建议优先同源反代形态**。残余风险（token 泄露即跨域可读写）依赖认证 + TLS + 高熵 secret + allowlist 的组合缓解，文档已同步引导。

---

*新增决策请追加至本文件，遵循"上下文 / 决策 / 后果"三段式，并标注状态。*
