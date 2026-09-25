# 架构决策记录 (ADR)

> 本文件记录 CoreC 关键设计决策的**上下文、选择与后果**，帮助接手者理解"为什么这么做"而非"怎么做"。
> 代码注释记录局部动机，这里集中记录跨模块、影响演进方向的取舍。
> 状态：`已采纳` / `已废弃` / `待定`。

---

## ADR-001 六边形架构：core/ 零外部依赖

- **状态**：已采纳
- **上下文**：工业网关需同时支持多种南向协议（Modbus/S7/OPC UA）与北向传输（MQTT/HTTP），且要能作为库嵌入上层系统。若协议库与领域逻辑耦合，替换协议或被嵌入时牵一发动全身。
- **决策**：`core/` 为纯契约层，只依赖标准库；`driver/*`、`transport/*` 为适配器，依赖向内（适配器→core，反之禁止）。新协议通过 `init()` 工厂注册 + `all/all.go` 空白导入聚合，零侵入扩展。
- **后果**：协议替换/新增不影响引擎；`core` 接口是稳定契约，扩接口属破坏性变更（见 CONTRIBUTING §2.3）。代价是 `core` 不能认识协议特有概念，协议特有参数走 `Settings map[string]any`。

## ADR-002 规则匹配采用线性扫描（非索引）

- **状态**：已采纳
- **上下文**：`rule/engine.go` 的 `Match` 对每个数据点线性遍历全部规则。可建倒排索引加速，但会破坏两个不变量：(1) 规则按优先级全局有序——分桶索引后跨桶无法保证首个命中是全局最高优先级；(2) `ruleWrapper.Match` 在每次评估时更新 hit/miss 统计，索引跳过的规则将丢失 miss 统计。
- **决策**：保留线性扫描，优先级全局有序 + 统计完整性优先于性能。代码注释明示 "Correctness is preserved over performance"。
- **后果**：规则数 <100 时无性能感知；>500 时每点多微秒级开销，在高频采集下累积。若未来规则数显著增长，可对"等值匹配类"规则做预筛候选集（仍按优先级排序），但需接受统计语义变化或对被跳过规则显式记 miss。当前工业场景规则数小，不实施。

## ADR-003 core.Logger / core.Metrics 端口分阶段注入

- **状态**：部分采纳（statistic.Manager 已注入；Logger/Metrics 端口待注入）
- **上下文**：已定义 `core.Logger`/`core.Metrics` 端口与 Noop 实现，但驱动/组件尚未实际持有或调用，仍直接用全局 `log/slog`。端口先行是为了确立契约，避免未来接入时改 core。
- **决策**：
  - **statistic.Manager**：已完成注入（IMPROVEMENTS #8）。`statistic.DefaultManager` 包级单例改为 `CoreCEngine.statManager` 实例字段，通过 `NewScheduler` 注入 scheduler。多实例嵌入不再共享计数器。
  - **core.Logger / core.Metrics**：端口先定义、暂不注入；标注为 "defined but NOT yet injected... larger, separate refactor"。注入需经 Driver.Init / 驱动构造器改造，涉及 94 处 `slog.X` 调用和 10+ 文件，属较大重构，单独进行（见 ADR-008）。
- **后果**：`statistic.DefaultManager` 仍保留（向后兼容）但引擎内已不使用。当前多实例嵌入时日志仍混在一起——这是已知技术债，待 Logger 注入重构时消除。

## ADR-004 OfflineBuffer 崩溃安全写入（fsync + 目录 fsync + 原子 rename）

- **状态**：已采纳
- **上下文**：传输中断时数据落盘缓存，恢复后补传。若写入不崩溃安全，断电可能丢失已"落盘"数据或留下半写文件。
- **决策**：`engine/offlinebuffer.go` 采用 write→fsync(数据)→close→rename→fsync(目录) 的原子写入序列。目录 fsync 确保 rename 的目录项更新持久化，否则断电后文件内容持久但 rename 不可见。
- **后果**：严格的崩溃一致性；在 journaling 文件系统（ext4 data=ordered/xfs）上目录 fsync 通常冗余但保留以求严格。代价是每次落盘两次 fsync，吞吐受限于磁盘——可接受，因 OfflineBuffer 仅在传输中断时启用。

## ADR-005 断路器冷却期持续探活（间隔粗但不停止）

- **状态**：已采纳
- **上下文**：驱动/传输连续失败达阈值后进入断路器冷却，避免雪崩式重连。早期文档曾误述为"冷却期不探活"。
- **决策**：`common/util/util.go` 的 `ReconnectLoopWithBreakerCounted` 在触发断路器后仅将 backoff 切换为固定 5 分钟，**循环继续运行**——每次 5 分钟后仍调用 `connect()` 探活，成功即恢复。即冷却期"有探活、仅间隔粗"。
- **后果**：设备恢复后最坏需等约 5 分钟才重连成功。若场景需更快恢复，可调小 `defaultCircuitBreakerBackoff` 或改为递减间隔。当前工业场景 5 分钟可接受。

## ADR-006 DataBus 弃旧策略（非阻塞、弃最旧）

- **状态**：已采纳
- **上下文**：生产者（驱动采集）快于消费者（worker 处理）时，DataBus 主通道会满。阻塞生产者会拖慢采集周期；丢弃新数据会丢失最新值。
- **决策**：`engine/databus.go` 的 `Push` 用非阻塞 select，通道满时**丢弃最旧的一条**腾位给最新，并计 `pushDropped`。最新值优先于旧值，符合工业监控"宁要新不要旧"的语义。
- **后果**：消费者持续跟不上时旧数据被弃，`pushDropped` 反映背压程度。控制类指令不走 DataBus（走独立 commandCh），故弃旧不影响控制。未来若在 DataBus 承载控制反馈流，需为该流提供独立的背压策略。

## ADR-007 Modbus 批读合并（贪心合并连续/重叠地址，上限 125 寄存器）

- **状态**：已采纳
- **上下文**：Modbus 单次读最多 125 寄存器（协议限制）。配置中相邻地址的 tag 若逐个读取，RTT 累积大。
- **决策**：`driver/modbus/modbus_base.go` 的 `performBatchReads` + `mergeBatcher` 贪心合并连续或重叠地址到同一请求，单请求不超过 125 寄存器。
- **后果**：大幅减少 Modbus RTT，提升高频采集吞吐。合并只针对可合并的连续/重叠地址，离散地址仍独立读。

## ADR-008 core.Logger / core.Metrics 端口注入延迟（IMPROVEMENTS #6）

- **状态**：待定（延迟实施）
- **上下文**：`core.Logger` 和 `core.Metrics` 端口已定义（含 NoopLogger/NoopMetrics），但引擎和驱动仍直接使用全局 `log/slog`。IMPROVEMENTS #6 要求注入 `core.Logger` 替换全部 `slog.X` 调用。
- **决策**：延迟实施。原因：(1) 涉及 94 处 `slog.X` 调用分布在 10 个文件（engine.go/scheduler.go/discovery.go/driver_manager.go/batcher.go/publish.go/command_manager.go/offlinebuffer.go/transport_manager.go/tagfile.go）；(2) 需修改 5+ 构造器签名（NewScheduler/newTransportBatcher/NewOfflineBuffer/newTagFileWatcher/NewDiscovery）以传入 logger；(3) `core.Logger` 接口签名（`Debug/Info/Warn/Error(msg string, args ...any)`）与 `slog` 的 KV args 语义需适配层；(4) 驱动层也需同步改造（Driver.Init 注入），代码注释已标注为 "larger, separate refactor"。
- **后果**：多实例嵌入时日志无法按实例隔离。端口已就绪，未来注入时不需改 core 契约。此项作为独立重构跟踪。

## ADR-009 端到端延迟追踪：pipeline 直方图已实现，OTLP 导出与 trace 贯穿延迟（IMPROVEMENTS #9）

- **状态**：部分采纳
- **上下文**：IMPROVEMENTS #9 要求端到端延迟追踪，包含三部分：(1) `corec_pipeline_latency_seconds` 直方图；(2) OTLP 导出接 Jaeger/Tempo；(3) DataPoint 透传 trace context。
- **决策**：
  - **pipeline 延迟直方图**：已实现。`corec_data_age_seconds`（`engine.DataAgeHistogram`，IMPROVEMENTS #10）记录 Publish 时刻与 `DataPoint.Timestamp` 的差值，即采集到发布的端到端延迟。在 4 个发布站点（batcher publish、direct publish、fallback batcher、fallback direct）均调用 `e.dataAge.Observe(time.Since(point.Timestamp))`。通过 `core.DataAgeProvider` 角色接口暴露，route 层 type-assert 读取。
  - **OTLP 导出**：延迟实施。当前 `common/trace` 的 span 仅以 `slog.Debug` 记录（debug 关闭时为 noopSpan）。落地 OTLP 导出属运维基础设施配置（部署 Jaeger/Tempo + 配置 OTLP endpoint），非代码架构变更。
  - **DataPoint trace context 贯穿**：延迟实施。在 `core.DataPoint` 增加 trace 字段属 core 契约变更（虽为新增可选字段、非破坏性，但需评估序列化影响和 hexagonal 边界）。当前 trace 主要在 HTTP 入口（`hub/route` middleware 解析 traceparent），未从采集端贯穿到 Publish 端。
- **后果**：用户最关心的"数据有多新鲜"已可通过 `corec_data_age_seconds` 直方图量化回答（p50/p99/p999）。全链路 trace 可视化需待 OTLP 基础设施部署。

---

*新增决策请追加至本文件，遵循"上下文 / 决策 / 后果"三段式，并标注状态。*
