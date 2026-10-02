# CoreC 技术债务清单（Phase 1 扫描产出）

> 本文件由 Phase 1 全仓只读扫描生成，是后续 Phase 2–5 改造的权威问题列表。
> 扫描日期：2025-01 · 分支：`harnessing` · Go 1.27.1 · 166 .go / 42,120 行 · 85 _test.go
> 规则依据：`docs/HARNESS-RULES.md`（附录 B 字段定义）

---

## 字段说明（附录 B）

| 字段 | 含义 |
|------|------|
| ID | 唯一标识，格式 `<域>-<序号>` |
| 标题 | 一句话描述问题 |
| 位置 | `文件:行号`（可多处） |
| 类别 | 架构 / 复杂度 / 重复 / 文档 / 测试 / 安全 / 性能 |
| 严重度 | P0（必须当前批次修复）/ P1（高优先）/ P2（常规） |
| 证据 | 可复现的客观事实（行号、测量值、grep 结果） |
| 修复建议 | 具体操作方向 |
| 业务行为影响 | **无** / **修复bug** / **变更行为**（严格分离，R2） |
| 关联批次 | Phase 2 / Phase 3 / Phase 4 批次 N / Phase 5 / 待人工决策 |
| 验收方式 | 可机械验证的通过条件 |
| 状态 | 待处理 / 进行中 / 已修复 / 已验证 / 待决策 |

**严重度定义**：P0 = 影响正确性/安全/可用性，或阻塞全部后续改造，必须当前批次修复；P1 = 高频改动或高故障代价；P2 = 常规技术债。

**业务行为影响严格分离（R2）**：`无` = 纯重构/补测试/补文档，不改运行时行为；`修复bug` = 修复缺陷使行为符合预期；`变更行为` = 改变对外可观测行为，需人工决策。

---

## 统计概览

| 指标 | 值 |
|------|-----|
| 总条目 | **75** |
| P0 | **1**（TEST-001） |
| P1 | **27** |
| P2 | **47** |
| 按类别 | 架构×2 · 复杂度×26 · 重复×9 · 文档×13 · 测试×11 · 安全×6 · 性能×8 |
| 业务行为影响=无 | **65**（纯重构/补测试/补文档） |
| 业务行为影响=修复bug | **6**（SEC-001告警 · PERF-001 · PERF-002 · PERF-005 · PERF-006 · PERF-007 · PERF-008） |
| 业务行为影响=变更行为 | **7**（SEC-001改默认 · SEC-002 · SEC-003 · SEC-004 · SEC-006 · PERF-003 · PERF-004）→ 待人工决策 |
| 整体测试覆盖率 | 80.1%（85 _test.go，0 FAIL，goleak 4 包启用） |
| 生产 InsecureSkipVerify | 0（1 处 WS Origin 跳过 + 1 处仅测试） |
| TODO/FIXME/XXX/HACK | 0 |
| 死链 | 0（孤儿文档 2） |

---

## 条目

### 架构（ARCH）

| ID | 标题 | 位置 | 类别 | 严重度 | 证据 | 修复建议 | 业务行为影响 | 关联批次 | 验收方式 | 状态 |
|---|---|---|---|---|---|---|---|---|---|---|
| ARCH-001 | 无 depguard/结构测试，依赖方向无机械约束 | .golangci.yml（无 depguard）；无 *_test.go 做 import graph 断言 | 架构 | P1 | golangci-lint 配置无 depguard；全仓无结构测试验证分层。当前依赖方向实际正确（0 环/0 跨层/0 跨域），但无护栏防止退化 | Phase 3：加 depguard 规则 + 结构测试断言允许边表；允许边表：core←{config,driver,transport,engine,rule,hub,log,common}；engine←{driver,transport,rule,config,common}；driver/transport←{core,common}；hub←{engine,config,core,common} | 无 | Phase 3 | depguard 零报错 + 结构测试通过 | 待处理 |
| ARCH-002 | config.validate 耦合全局 core.globalRegistry 单例 | config/config.go:240,244,313,391 | 架构 | P2 | validateDrivers/validateTransports 调 core.RegisteredDrivers()/core.RegisteredTransports()（全局单例），使 config 包对 core 有隐式运行时依赖，阻碍 config 独立测试 | 注入 DriverRegistry/TransportRegistry 接口到 config 校验路径，消除全局单例耦合 | 无 | Phase 4 批次 2 | config 包测试不依赖 core 全局状态 | 已修复 |

### 复杂度（CPLX）

| ID | 标题 | 位置 | 类别 | 严重度 | 证据 | 修复建议 | 业务行为影响 | 关联批次 | 验收方式 | 状态 |
|---|---|---|---|---|---|---|---|---|---|---|
| CPLX-001 | 超长文件: MQTT publisher 1186 行 | transport/mqtt/publisher.go:1-1186 | 复杂度 | P1 | 1186 行(>800, T1 阈值 400) | 拆分 replay_window.go / command_handler.go / tls_config.go / publisher.go | 无 | Phase 4 批次 7 | 文件 <400 行 | 已修复 |
| CPLX-002 | 超长文件: Modbus base 953 行 | driver/modbus/modbus_base.go:1-953 | 复杂度 | P1 | 953 行(>800) | 拆分 modbus_read.go / modbus_write.go / modbus_address.go / modbus_base.go | 无 | Phase 4 批次 4 | 文件 <400 行 | 已修复 |
| CPLX-003 | 超长文件: engine.go 875 行 | engine/engine.go:1-875 | 复杂度 | P1 | 875 行(>800) | 拆分 engine_lifecycle.go / engine_stats.go / engine_config.go | 无 | Phase 4 批次 3 | 文件 <400 行 | 已修复 |
| CPLX-004 | 超长文件: S7 driver 727 行 | driver/s7/s7.go:1-727 | 复杂度 | P1 | 727 行(600-800) | 拆分 s7_address.go / s7_codec.go / s7.go | 无 | Phase 4 批次 6 | 文件 <400 行 | 已修复 |
| CPLX-005 | 超长文件: route server 701 行 | hub/route/server.go:1-701 | 复杂度 | P1 | 701 行(600-800) | 拆分 server_lifecycle.go / middleware.go / router.go | 无 | Phase 4 批次 10 | 文件 <400 行 | 待处理 |
| CPLX-006 | 超长文件: OPCUA client 662 行 | driver/opcua/client.go:1-662 | 复杂度 | P1 | 662 行(600-800) | 拆分 opcua_subscription.go / opcua_read.go / opcua_write.go / client.go | 无 | Phase 4 批次 5 | 文件 <400 行 | 已修复 |
| CPLX-007 | 超长文件: rule engine 659 行 | rule/engine.go:1-659 | 复杂度 | P1 | 659 行(600-800) | 拆分 rule_engine.go / rule_build.go / rule_match.go | 无 | Phase 4 批次 9 | 文件 <400 行 | 已修复 |
| CPLX-008 | 超长文件: batcher 512 行 | engine/batcher.go:1-512 | 复杂度 | P2 | 512 行(400-600) | 拆出 retry_buffer.go | 无 | Phase 4 批次 3 | 文件 <400 行 | 已修复 |
| CPLX-009 | 超长文件: httppush 480 行 | transport/httppush/push.go:1-480 | 复杂度 | P2 | 480 行(400-600) | 拆出 webhook.go / push_config.go | 无 | Phase 4 批次 8 | 文件 <400 行 | 已修复 |
| CPLX-010 | 超长文件: config 467 行 | config/config.go:1-467 | 复杂度 | P2 | 467 行(400-600) | 拆出 validate.go / env_expand.go | 无 | Phase 4 批次 2 | 文件 <400 行 | 已修复 |
| CPLX-011 | 超长文件: route metrics 453 行 | hub/route/metrics.go:1-453 | 复杂度 | P2 | 453 行(400-600) | 按指标族拆分 metrics_driver.go / metrics_transport.go / metrics_runtime.go | 无 | Phase 4 批次 10 | 文件 <400 行 | 待处理 |
| CPLX-012 | 超长文件: executor 450 行 | hub/executor/executor.go:1-450 | 复杂度 | P2 | 450 行(400-600) | 拆出 diff.go / apply.go | 无 | Phase 4 批次 11 | 文件 <400 行 | 已修复 |
| CPLX-013 | 超长文件: driver_manager 422 行 | engine/driver_manager.go:1-422 | 复杂度 | P2 | 422 行(400-600) | 拆出 tagfile_watcher.go | 无 | Phase 4 批次 3 | 文件 <400 行 | 已修复 |
| CPLX-014 | 超高圈复杂度: scheduler.runTask gocyclo=27 | engine/scheduler.go:190-350(161行) | 复杂度 | P2 | gocyclo=27(>20), //nolint:gocyclo 抑制; 嵌套 7 层 | 抽取 deadband 过滤/错误降级/重连为独立函数；早返回降低嵌套 | 无 | Phase 4 批次 3 | gocyclo <20 且嵌套 ≤4 层 | 已修复 |
| CPLX-015 | 超高圈复杂度: engine.Start gocyclo=26 | engine/engine.go:235-379(145行) | 复杂度 | P2 | gocyclo=26(>20), //nolint:gocyclo 抑制 | 按子系统拆为 startDrivers/startTransports/startScheduler/startBatchers | 无 | Phase 4 批次 3 | gocyclo <20 | 已修复 |
| CPLX-016 | 超高圈复杂度: modbus.readTag gocyclo=26 | driver/modbus/modbus_base.go:437-536(100行) | 复杂度 | P2 | gocyclo=26(>20), //nolint:gocyclo 抑制 | 按数据类型分派表替换 if/switch 链；拆 decodeInt/decodeFloat/decodeBool | 无 | Phase 4 批次 4 | gocyclo <20 | 已修复 |
| CPLX-017 | 超高圈复杂度: executor.ApplyConfig gocyclo=22 | hub/executor/executor.go:275-364(90行) | 复杂度 | P2 | gocyclo=22(>20), //nolint:gocyclo 抑制 | 按子系统 diff 拆为 applyDrivers/applyTransports/applyRules/applyEngine | 无 | Phase 4 批次 11 | gocyclo <20 | 已修复 |
| CPLX-018 | 圈复杂度候选带(16-20, 8 函数) | config/config.go:298 validateDrivers=20; transport/mqtt/publisher.go:393 buildTLSConfig=20,:467 Start=18; driver/s7/s7.go:621 decodeS7Buffer=18; transport/httppush/push.go:90 Init=17,:274 PublishBatch=17; driver/modbus/modbus_base.go:298 Read=16; config/secrets.go:187 mergeSettings=16 | 复杂度 | P2 | gocyclo 实测均 >15 且 ≤20(低于项目阈值 20, 候选) | 逐个拆分决策分支；优先 buildTLSConfig(20)与 validateDrivers(20) | 无 | 跨域·各批次 | gocyclo <16 | 待处理 |
| CPLX-019 | 深嵌套(>4 层, 13 函数) | engine/scheduler.go:190 runTask(7层@331); transport/mqtt/publisher.go:635 handleCommandMessage; transport/httppush/push.go:189 Start; hub/route/server.go:532 rateLimitMiddleware,:642 authentication; driver/opcua/client.go:300 subscriptionLoop; engine/driver_manager.go:368 scheduleDriverTags; engine/discovery.go:254 reconcile; engine/batcher.go:295 publish; driver/s7/s7.go:250 Read; driver/modbus/modbus_base.go:298 Read; config/config.go:298 validateDrivers; common/util/util.go:303 ReconnectLoopWithBreakerCounted | 复杂度 | P2 | awk 测最大 tab 缩进 ≥5(函数内 ≥4 层嵌套); 最深 runTask=7 | 用早返回(guard clauses)消嵌套；提取嵌套块为命名函数 | 无 | 跨域·各批次 | 最大嵌套 ≤4 层 | 已修复 |
| CPLX-020 | God Object: CoreCEngine 43 字段/59 方法 | engine/engine.go:24-144(struct) | 复杂度 | P1 | 43 字段, 59 方法(均远超阈值 10/12) | 按职责拆为 EngineCore/DriverRegistry/TransportRegistry/StatsCollector 等组合结构 | 无 | Phase 4 批次 3 | 字段 ≤10 且方法 ≤12 | 延期至阶段5后 |
| CPLX-021 | God Object: MQTTTransport 46 字段/20 方法 | transport/mqtt/publisher.go:41-123(struct) | 复杂度 | P1 | 46 字段, 20 方法 | 拆为 MQTTConn(连接)/CommandGate(命令+HMAC+重放)/Publisher(发布)组合 | 无 | Phase 4 批次 7 | 字段 ≤10 且方法 ≤12 | 已修复 |
| CPLX-022 | God Object: OPCUADriver 35 字段/18 方法 | driver/opcua/client.go:22-72(struct) | 复杂度 | P1 | 35 字段, 18 方法 | 拆为 OPCUAConn/SubscriptionManager/NodeCache 组合 | 无 | Phase 4 批次 5 | 字段 ≤10 且方法 ≤12 | 已修复 |
| CPLX-023 | God Object: modbusBase 26 字段/22 方法 | driver/modbus/modbus_base.go:88-136(struct) | 复杂度 | P2 | 26 字段, 22 方法 | 拆为 ModbusConn/BatchPlanner/TagResolver 组合 | 无 | Phase 4 批次 4 | 字段 ≤10 且方法 ≤12 | 已修复 |
| CPLX-024 | 字段过多: S7Driver 27 字段 / HTTPTransport 25 字段 | driver/s7/s7.go:50-96; transport/httppush/push.go:31-71 | 复杂度 | P2 | 字段数 >10 | 收敛连接/状态字段到子结构体 | 无 | Phase 4 批次 6/8 | 字段 ≤10 | 已修复 |
| CPLX-025 | God Object: transportBatcher 16 字段/13 方法 | engine/batcher.go:53-110(struct) | 复杂度 | P2 | 16 字段, 13 方法 | 拆出 RetryPolicy/BufferState 子结构 | 无 | Phase 4 批次 3 | 字段 ≤10 且方法 ≤12 | 延期至阶段5后 |
| CPLX-026 | 高参数计数(>5, 3 函数) | common/util/util.go:303 ReconnectLoopWithBreakerCounted(7参); engine/batcher.go:120 newTransportBatcher(6参); common/util/util.go:289 ReconnectLoopWithBreaker(6参) | 复杂度 | P2 | 参数数 6-7(>5) | 引入 ReconnectOpts/BatcherOpts 配置结构体收拢参数 | 无 | Phase 4 批次 1 | 参数 ≤5 | 待处理 |

### 重复（DUP）

| ID | 标题 | 位置 | 类别 | 严重度 | 证据 | 修复建议 | 业务行为影响 | 关联批次 | 验收方式 | 状态 |
|---|---|---|---|---|---|---|---|---|---|---|
| DUP-001 | driver 生命周期方法跨 modbus/opcua/s7 重复 ~176 行 | driver/modbus/modbus_base.go, driver/opcua/client.go, driver/s7/s7.go | 重复 | P1 | Connect/Close/Health/Reconnect 等生命周期方法在三个驱动中近乎逐行复制；共享 util.ReconnectLoopWithBreakerCounted 已复用，重复在外层 wrapper | 提取 BaseDriver 生命周期骨架到 common/driverbase（或嵌入 mixin），各驱动仅覆写协议特定逻辑 | 无 | Phase 4 批次 1 | jscpd 重复率下降；驱动生命周期测试共享 | 已修复 |
| DUP-002 | reconnect_count_test.go 跨 modbus/s7 近乎逐行重复 ~350 行 | driver/modbus/reconnect_count_test.go, driver/s7/reconnect_count_test.go | 重复 | P1 | 两文件结构/断言/辅助函数几乎相同，仅驱动类型不同 | 提取 testutil.ReconnectTestHarness，参数化驱动工厂 | 无 | Phase 4 批次 1 | 测试重复行数 <50 | 已修复 |
| DUP-003 | TLS 证书加载逻辑跨 mqtt/httppush/modbus 重复 | transport/mqtt/publisher.go, transport/httppush/push.go, driver/modbus/tls.go | 重复 | P2 | loadCertPool/buildTLSConfig 三处近似实现 | 提取 common/tlsutil.BuildTLSConfig(opts) | 无 | Phase 4 批次 1 | TLS 配置单点实现 | 已修复 |
| DUP-004 | ParseReconnectSettings 跨驱动重复 | driver/modbus/modbus_base.go, driver/opcua/client.go, driver/s7/s7.go | 重复 | P2 | 从 config map 解析 reconnect 参数的逻辑三处近似 | 提取 common/driverutil.ParseReconnectSettings | 无 | Phase 4 批次 1 | 单点实现 | 已修复 |
| DUP-005 | numericValue 与 util.ToFloat64 功能重叠 | （多处引用） | 重复 | P2 | 两个函数做相同的 string→float64 转换 | 统一为 util.ToFloat64 | 无 | Phase 4 批次 1 | 单点实现 | 待处理 |
| DUP-006 | modbus 地址解析逻辑分散 | driver/modbus/modbus_base.go | 重复 | P2 | parseModbusAddress 与 register 计算逻辑分散 | 集中到 modbus_address.go（配合 CPLX-002 拆分） | 无 | Phase 4 批次 4 | 地址解析单文件 | 已修复 |
| DUP-007 | util.GetDurationSetting 重复调用模式 | common/util/util.go + 多处调用 | 重复 | P2 | 从 config map 取 duration 的模式多处重复 | 提取 helper 或确认已有 util 函数覆盖 | 无 | Phase 4 批次 1 | 单点实现 | 已归档（util.GetDurationSetting 已是单点实现，各调用点无额外重复验证逻辑） |
| DUP-008 | engine 配置应用逻辑分散重复 | engine/engine.go, engine/driver_manager.go | 重复 | P2 | applyEngineConfig/autoFill 等配置应用逻辑分散且有重复 | 配合 CPLX-003 拆分集中到 engine_config.go | 无 | Phase 4 批次 3 | 配置应用单文件 | 已修复 |
| DUP-009 | engine 统计/延迟计算重复 | engine/engine.go, engine/statistic/ | 重复 | P2 | 统计计算逻辑在 engine.go 和 statistic 包间有重复 | 集中到 engine/statistic 包 | 无 | Phase 4 批次 3 | 统计单包 | 已修复 |

### 文档（DOC）

| ID | 标题 | 位置 | 类别 | 严重度 | 证据 | 修复建议 | 业务行为影响 | 关联批次 | 验收方式 | 状态 |
|---|---|---|---|---|---|---|---|---|---|---|
| DOC-001 | 配置示例错误描述 env 替换为"字节级"（实际 tree-based 防注入） | config.example.yaml:19 vs config/config.go:48-54 | 文档 | P1 | 示例注释写"byte-level (pre-YAML-parse)"；代码注释写"Unlike naive byte-level replacement…parses YAML tree first"。代码已重构为 tree-based 防注入，示例未同步 | 将 config.example.yaml:18-21 改为"tree-based(parse→expand→re-marshal)，env 值经 YAML 编码器转义防注入" | 无 | Phase 2 | 文档 lint 校验示例描述与 config.go 注释一致 | 已修复 |
| DOC-002 | IMPROVEMENTS.md 引用 engine.go 不存在行号 | IMPROVEMENTS.md:12,25,60 引用 engine/engine.go:1244/1560/1314/1370 | 文档 | P1 | engine.go 仅 875 行；processingLoop 已迁至 processing.go:7、publishToTargets 至 publish.go:82 等。引用行号不可能存在 | 归档前逐条更新为"文件:函数名"形式，或迁移有效条目到 tech-debt-tracker.md 后归档 | 无 | Phase 2 | 文档 lint 校验 file:line 引用行号 ≤ 文件实际行数 | 已归档 |
| DOC-003 | IMPROVEMENTS.md 引用 batcher.go/publisher.go 已偏移行号 | IMPROVEMENTS.md:20,49 | 文档 | P1 | 实际行号偏移 48–177 行 | 同 DOC-002 | 无 | Phase 2 | 同 DOC-002 | 已归档 |
| DOC-004 | REALTIME_EVALUATION.md 行号引用偏移 | REALTIME_EVALUATION.md:55 引用 types.go:175 | 文档 | P2 | ReadTimeout 实际在 core/types.go:229(偏移 +54) | 更新为 types.go:229 或改用字段名 | 无 | Phase 2 | 同 DOC-002 | 已归档 |
| DOC-005 | QUALITY_ASSESSMENT.md 行号引用偏移 | QUALITY_ASSESSMENT.md:88,204 等 | 文档 | P2 | publisher.go:438-440 实际是 TLS cert pool 代码；modbus_base.go:325 实际是 tag 迭代。行号漂移 | 归档前更新行号或改用函数名；评分方法论迁移到 docs/QUALITY_SCORE.md | 无 | Phase 2 | 同 DOC-002 | 已归档 |
| DOC-006 | AI_HANDOVER.md engine.go 行数声明过时 | AI_HANDOVER.md:96,426 | 文档 | P2 | 声明"已从 1923 行拆为 823 行"，实际 875 行 | 更新为 875 行或删除具体行数(易腐) | 无 | Phase 2 | 文档 lint 校验声明行数与 wc -l 一致 | 已归档 |
| DOC-007 | AI_HANDOVER.md 文件树遗漏新增文件 | AI_HANDOVER.md:31-160 | 文档 | P2 | 遗漏 driver/modbus/{batch_test,lifecycle_test,reconnect_count_test}.go、transport/mqtt/{command_auth_test,...}.go 等 | 阶段 2 写 ARCHITECTURE.md 时以代码现状为准重绘；AI_HANDOVER 归档 | 无 | Phase 2 | 结构测试校验文档列举文件集 ⊆ 实际文件集 | 已归档 |
| DOC-008 | 缺少 AGENTS.md（harness 入口地图） | 仓库根 | 文档 | P2 | ls AGENTS.md → 不存在；§2.1/§2.2 要求为必需项 | Phase 2 按 §2.2/附录 E 创建(≤100 行地图) | 无 | Phase 2 | 存在性检查 + 行数 ≤200 + 含必需 5 节 | 已修复 |
| DOC-009 | 缺少 ARCHITECTURE.md（领域与分层地图） | 仓库根 | 文档 | P2 | ls ARCHITECTURE.md → 不存在；§2.1 要求为必需项 | Phase 2 创建(领域地图+包分层+依赖方向规则) | 无 | Phase 2 | 存在性检查 + 依赖方向规则与代码抽样一致 | 已修复 |
| DOC-010 | 18/27 Go 包缺 // Package 包文档注释 | cmd/corec, common/{metrics,observable,trace,util}, core, demo/chained/{4 包}, driver/s7, e2e, engine/statistic, hub, hub/{executor,route}, log, transport/parser | 文档 | P2 | 缺注释 18 包；有注释 9 包 | 为每个缺注释包在主 .go 文件加 // Package X <一句话职责> | 无 | Phase 2 | revive 包注释检查通过 | 已修复 |
| DOC-011 | docs/api/COREC_API_CONTRACT.md 未被索引链接（孤儿） | docs/api/COREC_API_CONTRACT.md | 文档 | P2 | grep 全 docs/ 无链接指向它；VitePress sidebar 未收录 | 在 config.ts API sidebar 加入条目 | 无 | Phase 2 | 文档 lint 校验每个 .md 至少一条入站链接 | 已修复 |
| DOC-012 | docs/API_REFERENCE.md 未被索引链接（孤儿） | docs/API_REFERENCE.md | 文档 | P2 | 无入站链接；与 docs/api/* 职责重叠 | 确认与 docs/api/overview.md 关系后合并或链接 | 无 | Phase 2 | 同 DOC-011 | 已修复 |
| DOC-013 | config.example.yaml 未演示 scale/offset 字段 | config.example.yaml vs core/types.go:219-220 | 文档 | P2 | TagConfig.Scale/Offset 为 omitempty 但属业务常用，根级示例未演示 | 在示例某 tag 加 scale: 0.01 / offset: 0.0 注释行 | 无 | Phase 2 | 文档 lint 校验示例覆盖非 omitempty-仅 字段 | 已修复 |

### 测试（TEST）

| ID | 标题 | 位置 | 类别 | 严重度 | 证据 | 修复建议 | 业务行为影响 | 关联批次 | 验收方式 | 状态 |
|---|---|---|---|---|---|---|---|---|---|---|
| TEST-001 | engine/discovery.go 拓扑自动发现 11 函数全 0.0% 覆盖 | engine/discovery.go（全文件）; engine/discovery_test.go:342行 | 测试 | P0 | discovery_test.go 只测纯辅助函数，从未启动真实 Discovery 心跳/协调 goroutine。11 函数全 0.0% | Phase 5 最先补：启动真实 Discovery 实例，测试心跳/协调/拓扑变更/关停；用 mock 邻居节点 | 无 | Phase 5（最先） | discovery.go 覆盖率 ≥60% | 已修复 |
| TEST-002 | MQTT Publish() 仅 8.3%、subscribeCommands/subscribeData 0% | transport/mqtt/publisher.go | 测试 | P1 | 需 broker；Publish 核心路径几乎未测 | 用 mock MQTT broker(eclipse-paho 兼容) 补 Publish/subscribe 路径测试 | 无 | Phase 5 | Publish 覆盖率 ≥70% | 待处理 |
| TEST-003 | OPC UA startSubscription/subscriptionLoop/handleConnectionLost 全 0% | driver/opcua/client.go | 测试 | P1 | 订阅/重连路径无测试 | 用 mock OPC UA server 补订阅生命周期测试 | 无 | Phase 5 | 订阅路径覆盖率 ≥50% | 待处理 |
| TEST-004 | Modbus handleConnectionLost 0%、readTag 10.5%、三种 connect 0% | driver/modbus/modbus_base.go | 测试 | P1 | 重连/读路径覆盖不足 | 用 mock Modbus slave 补重连+读路径测试 | 无 | Phase 5 | handleConnectionLost/readTag 覆盖率 ≥60% | 待处理 |
| TEST-005 | engine rollbackStart（Start 半途失败回滚）0% | engine/engine.go | 测试 | P1 | 无测试注入中途失败验证回滚 | 注入中途失败的 driver/transport factory，断言回滚正确 | 无 | Phase 5 | rollbackStart 覆盖率 ≥80% | 待处理 |
| TEST-006 | cmd/corec 24.6%（main 0%、run 18%） | cmd/corec/main.go | 测试 | P1 | main/run 路径几乎未测；信号测试忽略退出码 | 补 main/run 集成测试（os.Args 注入+信号模拟） | 无 | Phase 5 | cmd/corec 覆盖率 ≥50% | 待处理 |
| TEST-007 | transport/parser 66.2%（parseScalar 23.8%、parseTimestamp 33.3%） | transport/parser/ | 测试 | P1 | 边界值/错误路径覆盖不足 | 补边界值表驱动测试 | 无 | Phase 5 | parser 覆盖率 ≥85% | 已修复 |
| TEST-008 | log ParseLevel/adapter 0% | log/ | 测试 | P2 | 日志级别解析/适配器未测 | 补 ParseLevel 表驱动测试 + adapter 行为测试 | 无 | Phase 5 | log 覆盖率 ≥80% | 已修复 |
| TEST-009 | demo 0% 覆盖 | demo/chained/ | 测试 | P2 | demo 包无测试 | 补 demo 场景冒烟测试（或标注为 fixture 不计入覆盖） | 无 | Phase 5 | demo 冒烟测试通过 | 已修复 |
| TEST-010 | hub/route 离线缓冲指标 12.5% | hub/route/ | 测试 | P2 | 离线缓冲指标路径覆盖不足 | 补离线缓冲指标测试 | 无 | Phase 5 | 离线缓冲指标覆盖率 ≥70% | 待处理 |
| TEST-011 | engine cache.GetAll 0% | engine/ | 测试 | P2 | cache GetAll 未测 | 补 GetAll 测试 | 无 | Phase 5 | GetAll 覆盖率 ≥80% | 已修复 |

### 安全（SEC）

| ID | 标题 | 位置 | 类别 | 严重度 | 证据 | 修复建议 | 业务行为影响 | 关联批次 | 验收方式 | 状态 |
|---|---|---|---|---|---|---|---|---|---|---|
| SEC-001 | OPC UA 默认 SecurityPolicy#None + Anonymous 且无告警 | driver/opcua/client.go:184-194 | 安全 | P1 | L184 if d.securityPolicy != "" 才追加 SecurityPolicy，空则默认 None(无加密)；L190-193 username 空时 AuthAnonymous。全仓无告警(对比 MQTT/webhook 均有 Warn) | 启动时若 security-policy/username 为空则 slog.Warn（D5=仅加告警） | 修复bug | Phase 4 批次 5 | grep 确认 Init 路径有告警；config 校验测试断言空 security-policy 产生 warning | 已修复 |
| SEC-002 | MQTT 命令转发默认无认证(command-secret 空=接受未签名控制命令) | transport/mqtt/publisher.go:646(门控)+:487-488(告警) | 安全 | P1 | L646 if t.commandSecret != "" 才校验 HMAC；空时直接放行未签名命令→PLC 写入。已 Warn 但不阻止。command_auth_test.go:123 固化此向后兼容行为 | config 校验在 command-topic 非空且 command-secret 空时 fail-closed 拒绝启动（D7=变更行为） | 变更行为 | Phase 4 批次 7 | 现有 command_auth_test.go 覆盖签名校验；新增测试断言 fail-closed | 已修复 |
| SEC-003 | HTTP webhook 默认无认证(webhook-secret 空=接受任意 POST) | transport/httppush/push.go:404(门控)+:201-203(告警) | 安全 | P1 | L404 if t.webhookSecret != "" 才校验 HMAC；空时接受任意 POST→数据投毒。已 Warn | webhook-addr 非空且 webhook-secret 空时 fail-closed 拒绝启动（D9=变更行为） | 变更行为 | Phase 4 批次 8 | webhook_auth_test.go 已覆盖有密路径；新增测试断言无密拒绝启动 | 已修复 |
| SEC-004 | WebSocket 握手在 permissive 模式跳过 Origin 校验 | hub/route/common.go:34 | 安全 | P2 | L32-34 if len(wsAllowedOrigins)==0 { opts.InsecureSkipVerify = true }(跳过 Origin 检查非 TLS)。WS 端点在 authentication(secret) 组内，auth 是 query token 非 cookie CSRF 不可行，风险低 | D10=维持现状（保持 permissive）；生产部署文档建议设置 allowed-origins | 无 | 不立项（D10） | — | 已关闭 |
| SEC-005 | Demo 配置内置已知共享密钥 "demo-token" 且绑定 0.0.0.0 | demo/chained/scenario1-8/*.yaml(16 文件) | 安全 | P2 | api: { listen: "0.0.0.0:9090", secret: "demo-token" }。若运维直接拷 demo 配置上生产，API 密钥为公开已知值 | demo 改用 ${COREC_API_SECRET} 环境变量；或启动时对已知弱值在非 demo 模式告警。仅改 demo=无行为影响 | 无 | Phase 4 批次 12 | grep demo 确认无明文 "demo-token" 或改为 ${...} | 已修复 |
| SEC-006 | pprof 独立端口(PprofAddr)无认证 | hub/route/server.go:317-342 | 安全 | P2 | L337 log.Infoln "pprof (no auth)"；pMux 无 auth 中间件。默认 pprof 在主 server authenticated 组内，此为 PprofAddr 显式设置时 opt-in 路径 | 校验 PprofAddr 为回环地址，拒绝非回环绑定（D11=强制 loopback） | 变更行为 | Phase 4 批次 10 | 测试断言非 loopback PprofAddr 被拒绝 | 已修复 |

### 性能（PERF）

| ID | 标题 | 位置 | 类别 | 严重度 | 证据 | 修复建议 | 业务行为影响 | 关联批次 | 验收方式 | 状态 |
|---|---|---|---|---|---|---|---|---|---|---|
| PERF-001 | OPC UA Close/Cancel 用 context.Background() 无超时，关停可挂起+泄漏 | driver/opcua/client.go:411,652,372 | 性能 | P1 | Stop()(411)与 handleConnectionLost()(652)调 Close(context.Background())；stopSubscription()(372)调 Cancel(context.Background())。gopcua 不保证受 RequestTimeout 约束。engine.Stop() 超时后该 goroutine 永久阻塞→泄漏 | 三处改用 context.WithTimeout(context.Background(), d.timeout)；失败仅 log 不阻塞 | 修复bug | Phase 4 批次 5 | 注入永不响应 mock，断言 driver.Stop() 在 N 秒内返回且无泄漏(goleak) | 已修复 |
| PERF-002 | Modbus 重试 time.Sleep 不响应 ctx 取消，延迟关停 | driver/modbus/modbus_base.go:384,651 | 性能 | P1 | readTagWithRetry/readBatchWithRetry 失败后 time.Sleep(b.retryBackoff)。ctx 已取消时仍睡满才返回→关停延迟 | 改为 select{case <-ctx.Done(): return; case <-time.After(b.retryBackoff):}，ctx 传入 retry 路径 | 修复bug | Phase 4 批次 4 | 取消 ctx 后断言 Read 在 ≤retryBackoff 内返回 | 已修复 |
| PERF-003 | S7 驱动逐 tag 独立 PLC 往返(N+1) | driver/s7/s7.go:265-292 | 性能 | P2 | Read 内 for range batch { d.readAddress(...) }，注释明写"Each tag is still an independent gos7 call"。N tag = N 次串行 PLC 读 | 对同 DB+连续地址合并为单次 ABReadDB；非连续回退逐 tag（D6=变更行为） | 变更行为 | Phase 4 批次 6 | 基准：N tag PLC 往返数从 N 降至合并后批数；行为测试覆盖混合地址 | 延期至阶段5后 |
| PERF-004 | MQTT PublishBatch 逐点串行发布(N 次往返) | transport/mqtt/publisher.go:1109-1120 | 性能 | P2 | for range points { t.Publish(ctx, points[i]) }，每次串行 WaitTimeout。100 点 = 100 次串行 PUBLISH+ACK | 先并发 fire 所有 token 再统一 WaitTimeout(保留 firstErr)（D8=变更行为） | 变更行为 | Phase 4 批次 7 | 基准：batch 延迟从 N×RTT 降至 ~RTT；QoS/顺序测试通过 | 已修复 |
| PERF-005 | rule provider 热重载每 tick 全量重解析+重编译，无 hash 检查 | rule/provider.go:99-112 | 性能 | P2 | reloadLoop 每 tick 调 load()→ReadFile+Unmarshal+compileExpr，无变更检测。对比 tagfile.go 用 SHA-256 hash 跳过 | 仿 tagfile.go 加 SHA-256 hash，未变更则跳过 load | 修复bug | Phase 4 批次 9 | 未改文件时断言 load 次数为 0 | 已修复 |
| PERF-006 | rule provider Close 不等 reloadLoop 退出(有界泄漏) | rule/provider.go:140-142 | 性能 | P2 | Close() 仅 close(p.stopCh)，无 done channel。reloadLoop 最长再跑一个 tick 才退出 | 加 done chan，loop 退出 close(done)，Close 后 <-done。仿 tagfile.go:132-135 | 修复bug | Phase 4 批次 9 | goleak 测试：Close 后无 reloadLoop 拘留 | 已修复 |
| PERF-007 | goleak 仅覆盖 4 包，driver/* 与 transport/httppush 未强制 | driver/opcua/, driver/modbus/, driver/s7/, transport/httppush/ | 性能 | P2 | goleak.VerifyTestMain 仅在 engine/, transport/mqtt/, hub/route/, rule/。driver reconnectLoop/subscriptionLoop、httppush webhook goroutine 无机械泄漏校验 | 为上述 4 包加 main_test.go + goleak.VerifyTestMain(按需 IgnoreAnyFunction 排除 paho/gos7) | 修复bug | Phase 3 | 新增 main_test.go 后 go test goleak 通过 | 待处理 |
| PERF-008 | engine.Stop() 对 batcher.stop() 无超时(与 driver/transport 不对称) | engine/engine.go:528-531 | 性能 | P2 | for range batchers { b.stop() } 无 time.After 包裹。当前因内部 WithTimeout+有界 retry 而有界，但若 transport 忽略 ctx 或 retry 配置过大则 Stop 挂起 | 给 batcher.stop() 加超时包裹，或给 flushFinal 传带超时 ctx | 修复bug | Phase 4 批次 3 | 注入阻塞型 transport，断言 engine.Stop() 在 shutdownTimeout 内返回 | 已修复 |

---

## 四份根级分析文档处置建议（决策 D2）

| 文档 | 大小 | 判定 | 处置 |
|------|------|------|------|
| AI_HANDOVER.md | 35644B | AI-residue，部分过时（行数声明/文件树） | 归档至 docs/exec-plans/completed/ 或 docs/design-docs/；含设计 rationale 勿删 |
| IMPROVEMENTS.md | 35772B | AI-residue，行号严重过时（DOC-002/003） | 仍有效改进项迁移到本 tracker，原文归档 |
| QUALITY_ASSESSMENT.md | 27806B | AI-residue 评分快照，行号漂移（DOC-005） | 评分方法论迁移到 docs/QUALITY_SCORE.md（Phase 2），快照归档 |
| REALTIME_EVALUATION.md | 11481B | 一次性性能分析，基本准确（DOC-004 单处偏移） | 移至 docs/design-docs/ 作参考 |

> 以上四文档均位于仓库根（非 docs/），与 §2.1 目标布局不符。Phase 2 文档对齐时归档，**不删除**。
