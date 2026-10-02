# Harness 工程化改造计划

> 本计划依据《Harness 工程化规则》（`docs/HARNESS-RULES.md`）§5 五阶段流程制定，针对 **CoreC**（Go IIoT 数据采集核心）存量项目。
> **当前处于阶段 4 进行中**：批次 1（common）已完成，批次 2–12 待执行。

## 0. 元信息

- 项目 / 仓库：CoreC — `github.com/CoreC-Dev/CoreC`（IIoT 数据采集与控制核心，Go 1.27.1）
- 仓库路径：`/workspace/codespace/CoreC`
- 分支：`harnessing`（由 `main` 创建，§7.1）
- 计划版本 / 日期：v1.0 / 2026-10-02
- 状态：**阶段 4 进行中**（批次 1 完成，批次 2–12 待执行）
- 规则文档：`docs/HARNESS-RULES.md`（施工期常驻，竣工后按 §14 拆解归档）
- 框架判定：其他类型（纯 Go 后端，非 Tauri）—— 详见 `docs/CI.md`

## 1. 项目概况

- **技术栈**：Go 1.27.1，纯后端 / Headless 二进制（无 UI 强绑定）。依赖：`paho.mqtt.golang`（MQTT）、`gopcua/opcua`（OPC UA）、`simonvetter/modbus`（Modbus）、`robinson/gos7`（S7/西门子）、`expr-lang/expr`（规则表达式）、`go-chi/chi`（HTTP API）、`coder/websocket`、`tidwall/gjson`、`go.uber.org/goleak`。
- **架构**：六边形可插拔。数据流 `Driver(南向) → Engine & Rules(核心) → Transport(北向)`，支持反向控制指令下发。插件通过 `init()` 工厂注册（`driver/all`、`transport/all` 空导入触发）。
- **构建方式**：`go build -o corec ./cmd/corec`（CI 通过 ldflags 注入版本号）。
- **运行方式**：`./corec -c config.example.yaml`（YAML 配置驱动，支持 `${ENV_VAR}` 替换、tags-file 热重载）。
- **部署方式**：单二进制，多架构交叉编译（linux amd64/arm64/armv7、darwin amd64/arm64、windows amd64/arm64）。tag `v*` 触发 `release.yml` 自动构建并发布 GitHub Release。
- **顶层模块与入口点**：
  - 入口：`cmd/corec/main.go`（wiring：config→engine→hub→log + 空导入 driver/all、transport/all）
  - `core/`：核心接口与类型（`core.Driver`、`core.Transport` 插件缝隙）
  - `config/`：YAML 配置加载、env 替换、secrets、tags-file 热重载
  - `engine/`：核心引擎（scheduler、batcher、databus、discovery、offlinebuffer、driver_manager、command_manager）+ `engine/statistic`
  - `driver/{modbus,opcua,s7}`：南向设备驱动 + `driver/all` 注册聚合
  - `transport/{mqtt,httppush,parser}`：北向传输 + `transport/all` 注册聚合
  - `rule/`：规则引擎（expr 表达式求值）
  - `hub/{route,executor}`：控制面 HTTP/WS API 与执行器
  - `common/{metrics,observable,trace,util}`：共享工具（指标、可观测、追踪、reconnect/breaker 工具）
  - `log/`：结构化日志封装
  - `e2e/`：端到端测试
  - `demo/`：示例
- **代码规模**：166 个 `.go` 文件，42,120 行（非测试约 16,452 行集中于 25 个最大文件）；85 个 `_test.go` 文件。
- **当前测试与 CI 现状**：
  - CI：`ci.yml`（test `-race -short` + vet + golangci-lint v2.13.2 + 多平台 build check）、`release.yml`（tag→多架构 Release）、`deploy-docs.yml`（VitePress→GitHub Pages）。详见 `docs/CI.md`。
  - lint：`.golangci.yml` v2，启用 errcheck/govet/staticcheck/unused/gocritic/gocyclo(阈值20)/revive/misspell 等；排除 `cmd/corec/` 的 gocyclo。
  - 测试：85 个测试文件，整体覆盖率 80.1%（0 FAIL），具体覆盖率与质量见 §3 现状诊断。

## 2. 框架判定与 CI 现状（见 §3.2）

- **判定结果**：其他类型（纯 Go 后端）
- **判定证据**：`src-tauri/tauri.conf.json` 不存在；`src-tauri/Cargo.toml` 不存在；根无 `package.json` 含 `@tauri-apps/*`；反证 `go.mod` 存在。详见 `docs/CI.md` §1。
- **现有 `.github/workflows/` 清单与触发条件**：`ci.yml`（push main + PR）、`release.yml`（tag `v*`）、`deploy-docs.yml`（push main `docs/**`）。详见 `docs/CI.md` §2。
- **已有发布链路现状**：tag 触发多架构 Go 二进制正式 Release（非 draft），无签名，无版本一致性 guard job。详见 `docs/CI.md` §2。
- **§3.3 三问**：不适用（非 Tauri，按 §3.4 跳过）。

## 3. 现状诊断

> 本节由阶段 1 七路并行扫描综合而成。详细条目见 `docs/exec-plans/tech-debt-tracker.md`。

### 3.1 架构现状（含模块依赖图）

- **六边形可插拔架构**：数据流 `Driver(南向) → Engine & Rules(核心) → Transport(北向)`，支持反向控制指令下发。插件通过 `init()` 工厂注册（`driver/all`、`transport/all` 空导入触发）。
- **依赖方向实际正确**：0 环、0 跨层违规、0 跨域依赖。核心缝隙 `core.Driver`(11 方法)、`core.Transport`(10)、`core.Rule`(10)、`core.RuleEngine`(9) — 接口方法数均未超 12 阈值，属合理领域抽象。
- **问题**：ARCH-001（P1，无 depguard/结构测试，依赖方向无机械约束，退化无护栏）；ARCH-002（P2，`config.validate` 耦合全局 `core.globalRegistry` 单例，阻碍 config 独立测试）。
- **允许边表**（Phase 3 结构测试依据）：`core` ← {config, driver, transport, engine, rule, hub, log, common}；`engine` ← {driver, transport, rule, config, common}；`driver`/`transport` ← {core, common}；`hub` ← {engine, config, core, common}。

### 3.2 复杂度热点

- **13** 个非测试 `.go` 文件超 T1 的 400 行阈值，**3** 个超 800 行。
- **Top-10 文件**：`publisher.go` 1186 · `modbus_base.go` 953 · `engine.go` 875 · `s7.go` 727 · `server.go` 701 · `client.go` 662 · `rule/engine.go` 659 · `batcher.go` 512 · `push.go` 480 · `config.go` 467。
- **圈复杂度 >20 的函数 4 个**，全部 `//nolint:gocyclo` 显式抑制（`runTask`=27、`engine.Start`=26、`modbus.readTag`=26、`ApplyConfig`=22）— **gocyclo 门禁"绿"是因热点被抑制而非已重构**。候选带 16–20 的函数 8 个。
- **深嵌套 >4 层 13 个函数**，最深 `scheduler.runTask` 7 层。
- **God Object 7 个**：`CoreCEngine`(43 字段/59 方法)、`MQTTTransport`(46/20)、`OPCUADriver`(35/18)、`modbusBase`(26/22)、`S7Driver`(27 字段)、`HTTPTransport`(25 字段)、`transportBatcher`(16/13)。
- **高参数 >5 的函数 3 个**；**bool 参数地狱 0**（正面信号）。

### 3.3 重复代码

- **~740 重复行**，全部纯重构（业务行为影响=无）。
- **DUP-001**（P1）：driver 生命周期方法跨 modbus/opcua/s7 重复 ~176 行。**DUP-002**（P1）：`reconnect_count_test.go` 跨 modbus/s7 近乎逐行重复 ~350 行。
- **DUP-003..009**（P2）：TLS 证书加载、reconnect 设置解析、数值转换、modbus 地址解析、duration 取值、engine 配置应用、统计计算。
- 共享 `util.ReconnectLoopWithBreakerCounted` 已复用，重复在外层 wrapper。

### 3.4 文档缺口

- `config.example.yaml` 字段/类型与 `core.Config` 基本一致，但 **DOC-001**（P1）主动误导 env 替换机制为"字节级"（实际 tree-based 防注入）。
- **四份根级分析文档**（AI_HANDOVER/IMPROVEMENTS/QUALITY_ASSESSMENT/REALTIME_EVALUATION）均为 AI 生成一次性快照，含不可能/偏移的行号引用（DOC-002..007）— 与 `docs/` 站点及未来 `ARCHITECTURE.md`/`QUALITY_SCORE.md`/`tech-debt-tracker.md` 职责重叠。
- **18/27 Go 包缺 `// Package` 包文档注释**（DOC-010）。
- `AGENTS.md` 与 `ARCHITECTURE.md` 均不存在（harness 必需项，阶段 2 创建）— DOC-008/009。
- VitePress 站点**无死链**，但 2 份孤儿文档未被索引链接（DOC-011/012）。
- **TODO/FIXME/XXX/HACK 计数 0**（正面信号）。

### 3.5 测试缺口

- **整体覆盖率 80.1%**（85 `_test.go`，0 FAIL）。
- **P0**：TEST-001 `engine/discovery.go` 11 函数全 0.0% — 运行时拓扑自动发现完全未测（`discovery_test.go` 只测纯辅助函数，从未启动真实 Discovery 心跳/协调 goroutine）。
- **P1**：MQTT `Publish()` 8.3%（TEST-002）、OPC UA 订阅/重连 0%（TEST-003）、Modbus 重连/读 0%/10.5%（TEST-004）、engine `rollbackStart` 0%（TEST-005）、`cmd/corec` 24.6%（TEST-006）、`transport/parser` 66.2%（TEST-007）。
- **P2**：log/demo/hub 离线缓冲/engine cache 覆盖不足（TEST-008..011）。
- **积极信号**：goleak 真正启用（4 包 `TestMain`+`VerifyTestMain`）；e2e 是真实集成测试（6 场景，无 skip/环境依赖）；`*_bug_test.go`/`*_fix_test.go` 均为实质回归测试；config 校验表驱动覆盖 86.9%。

### 3.6 安全与性能风险

**安全**（基线整体强于同类 IIoT 项目）：
- API 在 `api.secret` 空时 **fail-closed 拒绝启动**；认证 SHA256+`hmac.Equal` 常量时间比较；token 查询参数在访问日志中脱敏。
- `${ENV}` 替换 parse→expand→re-marshal 防 YAML 注入；`secrets.go` `Redact()`/`MergeSentinels()` 保证 GET /configs/raw 永不回显明文。
- TLS（mqtt/httppush/modbus）均 MinVersion TLS1.2+mTLS+ServerName 校验；**生产 `crypto/tls.InsecureSkipVerify` = 0**。
- expr-lang/expr 求值环境固定 DataPoint 字段 map，未注册危险 `expr.Function`，`DisableBuiltin("type")` — 非 RCE 向量；无 `os/exec`/`plugin.Open`/`unsafe.Pointer`。
- **无 P0**。3 个 P1（SEC-001 OPC UA 无加密/匿名默认**且无告警**、SEC-002 MQTT 命令转发默认无认证、SEC-003 webhook 默认无认证）— 均为"opt-in 认证默认关闭"的入站路径。3 个 P2 硬化（SEC-004 WS origin 跳过、SEC-005 demo 弱密 "demo-token"、SEC-006 pprof 独立端口无认证）。

**性能与可靠性**（基线整体扎实）：
- goleak 4 包强制；24 个 goroutine 启动全部绑定 context 取消或 WaitGroup；DataBus/OfflineBuffer/replayCache/deadLetterQueue/commandSem/flushBatches 均有上界+驱逐；reconnect 熔断器指数退避+抖动+熔断-open。
- `engine.Stop()` 先 `cancel()` 再按序 scheduler→discovery→drivers/transports(带超时)→dataBus→ruleProviders→tagWatchers→`wg.Wait()`；`cmd/corec` 用 `signal.Notify` 协调关停。
- **无 P0**（无死锁/泄漏/无界内存）。2 个 P1（PERF-001 OPC UA `Close/Cancel` 用 `context.Background()` 无超时→关停挂起+goroutine 泄漏；PERF-002 Modbus `time.Sleep` 不响应 ctx→延迟关停）。
- P2：S7 N+1 逐 tag 往返（PERF-003，变更行为）、MQTT `PublishBatch` 串行（PERF-004，变更行为）、rule provider 热重载无 hash 检查（PERF-005）、rule provider Close 不等 goroutine 退出（PERF-006）、goleak 未覆盖 driver/* 与 httppush（PERF-007）、engine.Stop batcher 无超时（PERF-008）。

### 3.7 度量基线（改造前）

| 指标 | 值 |
|------|-----|
| 代码规模 | 166 `.go` / 42,120 行 / 85 `_test.go` |
| 整体覆盖率 | 80.1% |
| 超 400 行文件 | 13（超 800 行：3） |
| gocyclo >20 函数 | 4（全 nolint 抑制）；16–20 候选：8 |
| 深嵌套 >4 层 | 13（最深 7） |
| God Object | 7；高参数 >5：3；bool 地狱：0 |
| 重复行 | ~740 |
| 生产 InsecureSkipVerify | 0；硬编码密钥：0 |
| TODO/FIXME | 0；死链：0；孤儿文档：2 |
| 缺包注释 | 18/27 |
| goroutine 总数 | 24（全 ctx/WG 绑定）；无界 channel：0 |
| 问题总条目 | 75（P0×1, P1×27, P2×47） |

## 4. 改造批次划分

> 按业务域分批推进（§1.4.2）。每批独立完成「建地图→建不变量→重构→补测试」，含独立回滚方案（红线 R8）。
> **前置条件**：Phase 3 门禁（ARCH-001 结构测试 + depguard、PERF-007 goleak 扩展、T1–T10 品味 linter）须先于 Phase 4 批次落地，为重构提供机械护栏（红线 R3：先护栏后动手）。
> **回滚通用方案**：每批由若干单一职责小提交组成（C6）；回滚 = `git revert` 该批提交区间或 `git reset` 到批前 tag。因每批独立完成且不跨域，回滚不影响其他域。行为守恒由门禁+测试保证（R3）；行为变更/bug 修复单独提交并显式标注（R2）。

| 批次 | 域 | 涵盖问题 | 改造内容 | 风险 | 回滚 |
|------|-----|---------|---------|------|------|
| **1** | common（共享基础设施） | DUP-001,002,003,004,005,007 · CPLX-026 | 提取 `BaseDriver` 生命周期骨架、`testutil.ReconnectTestHarness`、`common/tlsutil.BuildTLSConfig`、`ParseReconnectSettings`、`ReconnectOpts` 参数结构体 | 低 — 纯提取，调用点机械替换 | revert 提取提交，恢复原内联 |
| **2** | config（配置层） | CPLX-010 · ARCH-002 | 拆 `config.go` → validate.go + env_expand.go；注入 DriverRegistry 接口消除全局单例耦合 | 低 — config 已有 86.9% 测试覆盖 | revert 拆分提交 |
| **3** | engine（核心引擎） | CPLX-003,008,013,014,015,019,020,025 · DUP-008,009 · PERF-008 | 拆 `engine.go`→lifecycle/stats/config；拆 `batcher.go`→retry_buffer；拆 `driver_manager.go`→tagfile_watcher；CoreCEngine God Object 拆组合结构；`runTask`/`Start` 降复杂度+降嵌套；batcher.stop 加超时 | **高** — 核心路径，须先补 TEST-001/005 护栏 | revert 各子提交；engine 已有 e2e+goleak 兜底 |
| **4** | driver/modbus | CPLX-002,016,023 · DUP-006 · PERF-002 | 拆 `modbus_base.go`→read/write/address/lifecycle；`readTag` 分派表降复杂度；modbusBase God Object 拆组合；`time.Sleep`→`select+ctx` | 中 — 驱动有 reconnect_count_test 兜底 | revert 拆分提交 |
| **5** | driver/opcua | CPLX-006,022 · SEC-001 · PERF-001 | 拆 `client.go`→subscription/read/write/lifecycle；OPCUADriver God Object 拆组合；`Close/Cancel` 加 `WithTimeout`；空 security-policy/username 加 `slog.Warn` | 中 | revert 拆分提交；告警修复单独提交 |
| **6** | driver/s7 | CPLX-004,024 · PERF-003 | 拆 `s7.go`→address/codec/lifecycle；S7Driver 字段收敛子结构体；**合并连续地址 tag 为单次 ABReadDB**（D6=变更行为，单独提交） | 中 | revert 拆分提交；N+1 合并单独提交 |
| **7** | transport/mqtt | CPLX-001,021 · SEC-002 · PERF-004 | 拆 `publisher.go`→replay_window/command_handler/tls_config/publisher；MQTTTransport God Object 拆组合；**command-secret 空 fail-closed**（D7=变更行为）；**PublishBatch 改并发**（D8=变更行为） | 中高 — 最大文件，须先补 TEST-002 护栏 | revert 拆分提交；行为变更单独提交 |
| **8** | transport/httppush + parser | CPLX-009 · SEC-003 | 拆 `push.go`→webhook/push_config；**webhook-secret 空 fail-closed**（D9=变更行为） | 低 | revert 拆分提交；行为变更单独提交 |
| **9** | rule（规则引擎） | CPLX-007 · PERF-005,006 | 拆 `rule/engine.go`→engine/build/match；reloadLoop 加 SHA-256 hash 跳过未变更；Close 加 done channel 等待 | 低 | revert 拆分提交 |
| **10** | hub/route（控制面 API） | CPLX-005,011 · SEC-004 · SEC-006 | 拆 `server.go`→lifecycle/middleware/router；拆 `metrics.go` 按指标族；WS 保持 permissive（D10）；**pprof PprofAddr 强制 loopback**（D11=变更行为） | 中 | revert 拆分提交；loopback 校验单独提交 |
| **11** | hub/executor | CPLX-012,017 | 拆 `executor.go`→diff/apply；`ApplyConfig` 按子系统拆降复杂度 | 低 | revert 拆分提交 |
| **12** | demo | SEC-005 | demo 配置改用 `${COREC_API_SECRET}` 环境变量替代明文 "demo-token" | 极低 | revert |

> **跨域条目**：CPLX-018（候选带 8 函数）、CPLX-019（深嵌套 13 函数）在各域批次内处理本域函数，不单设批次。
> **P0 处理**：TEST-001（discovery 运行时 0% 覆盖）在 Phase 5 最先补全，为批次 3 engine 重构提供护栏。
> **变更行为条目**（D6 S7 N+1 合并、D7 MQTT fail-closed、D8 MQTT 并发、D9 webhook fail-closed、D11 pprof loopback）均已决策，归入对应批次，单独提交（R2）。D5（OPC UA 告警）= 修复bug。D10（WS permissive）= 维持现状不立项。

## 5. 五阶段任务拆解

> 每阶段验收标准逐条勾完才进入下一阶段（契约 C2）。总验收见规则文档 §11。

### 阶段 1 · 全量扫描与问题清单
- **子任务**：12 项（规则文档 §5 阶段1）。本计划即其产出。
- **产出物**：`docs/exec-plans/active/harness-migration.md`（本文件）、`docs/exec-plans/tech-debt-tracker.md`、`docs/CI.md`、`docs/HARNESS-RULES.md`（规则副本）。
- **验收标准**（§5 阶段1）：
  - [x] 五阶段全部拆解完成，每个子任务有负责人角色 / 产出物路径 / 可机械检查的验收标准
  - [x] 框架判定已完成，命中证据已写入 `docs/CI.md`
  - [x] 问题清单每条含 ID / 位置 / 类别 / 严重度 / 证据 / 修复建议 / 业务行为影响
  - [x] 「修复bug」与「变更行为」与纯重构严格分离
  - [x] 每批改造有独立回滚方案
  - [x] 计划文档无只有标题、无分析的条目
  - [x] 未修改任何业务代码（`git diff --stat` 自证）—— 阶段 1 仅新增 docs/ 下文件，无 .go 变更

### 阶段 2 · 文档对齐
- **子任务**（§5 阶段2）：重写 `AGENTS.md`（≤100 行地图形态，附录 E 骨架）；编写 `ARCHITECTURE.md`（领域地图 + 包分层 + 依赖方向规则）；建立 `docs/` 结构（裁剪 §2.1，与现有 VitePress 站点共存而非覆盖）；沉淀 `docs/design-docs/core-beliefs.md`（附录 F）；清理过时内容（含根级四份大分析文档的处置）；建立 `docs/QUALITY_SCORE.md`；建立 `docs/exec-plans/` 标准位置；补全工程化说明；建立文档防腐（文档 lint + 园丁任务）。
- **产出物**：`AGENTS.md`（67 行）、`ARCHITECTURE.md`、`docs/design-docs/core-beliefs.md`、`docs/design-docs/index.md`、`docs/QUALITY_SCORE.md`、`scripts/check-docs.sh`（文档校验脚本）。
- **验收标准**（§5 阶段2）：
  - [x] `AGENTS.md` ≤ 200 行（实测 67 行），且不含教程式长文；能在 100 行内把智能体引到正确的下一站
  - [x] `ARCHITECTURE.md` 的依赖方向规则与代码实际结构一致（抽样验证 ≥ 3 处：core←driver、engine←{driver,transport,rule}、hub←engine）
  - [x] `docs/index.md` 中的每个链接都可解析（`scripts/check-docs.sh` 死链检查通过）
  - [x] 随机抽取 3 份旧文档，与代码逐条比对，无遗留错误描述（四份根级文档已归档至 `docs/design-docs/`，有效条目迁移到 tracker/QUALITY_SCORE.md）
  - [x] `QUALITY_SCORE.md` 对每个业务域给出分数与差距，且分数有可复算的依据
  - [x] 文档校验 CI 在缺少必填节或出现死链时能失败（`scripts/check-docs.sh` 实测通过）

### 阶段 3 · 门禁审计与自动化质量校验
- **子任务**（§5 阶段3）：§3.3 三问不适用（非 Tauri）；按 §3.4 在现有 CI 上增量改造（**不删现有配置**）；盘点现有门禁；补齐基础门禁（build/type/lint/format/test/依赖扫描）；加提交前 hook；编写架构结构测试（§4.1 依赖方向）；编写自定义 linter 与品味不变量 T1–T10（§4.2，错误信息含修复指令）；加文档门禁；加覆盖率门禁；建立豁免机制；固化合并理念。
- **产出物**：更新后的 `docs/CI.md`、门禁清单文档、CI/hook/linter/结构测试配置、覆盖率配置。
- **验收标准**：干净环境「安装→构建→测试」一次成功；故意越层依赖/超长文件/非结构化日志/死链门禁**实测**失败；每个失败信息含可执行修复指引；门禁可本地一条命令复现；豁免项有原因与到期时间。
- **完成清单**：
  - [x] 架构结构测试 `archtest/arch_test.go`（T7: 依赖方向，`go list -json` 解析 import 图，对照 ARCHITECTURE.md 允许边表校验，含跨域禁止规则）
  - [x] 品味不变量测试 `tastetest/taste_test.go`（T1: 文件 ≤600 行 + 豁免登记；T6: 禁止 fmt.Println/Printf 生产代码）
  - [x] `Makefile`（`make gates` 一条命令复现全部门禁：vet+lint+arch+taste+docs+test）
  - [x] `scripts/pre-commit.sh` 提交前 hook（`make install-hooks` 安装）
  - [x] CI 增量改造 `.github/workflows/ci.yml`：新增 `gates`/`doc-gate`/`coverage`/`version-check` 四个 job，**未删任何现有 job**；修复 paths-ignore 过期条目
  - [x] `docs/gates.md` 门禁清单（10 项门禁 G1–G10，每项含触发/失败含义/修复指引/本地命令）
  - [x] 豁免机制文档化（nolint 须附原因+tracker ID；文件大小豁免含修复计划；t.Skip 须附原因）
  - [x] 违规实测：注入跨域 import → T7 拦截 ✅；注入超长文件 → T1 拦截 ✅；注入 fmt.Println → T6 拦截 ✅；每条失败信息含可执行修复指引
  - [x] 干净环境全绿：vet ✅ build ✅ arch ✅ taste ✅ docs ✅ test ✅

### 阶段 4 · 问题修复与重构落地
- **子任务**（§5 阶段4）：按 §4 批次推进；拆分臃肿模块；落实单一职责；降低复杂度；抽离公共可复用逻辑到共享工具包；修复问题清单缺陷（P0 优先）；同步更新文档与 `QUALITY_SCORE.md`；维护计划进度日志；更新技术债台账。
- **产出物**：每批一组小粒度提交；同步更新的文档与计划；更新后的 `tech-debt-tracker.md`。
- **验收标准**：每批后门禁全绿、测试全过；行为守恒证据（同输入同输出）；行为变更/bug 修复显式标注且单独提交；重复度/复杂度/热点文件数下降（给前后数值）；进度日志与 commit 可逐条对应。

### 阶段 5 · 测试补全与门禁接入
- **子任务**（§5 阶段5）：补关键路径端到端测试；补边界/异常测试（空值/超限/并发/超时/部分失败/幂等）；补阶段 1 标记「无测试」模块；建立测试分层；接入覆盖率门禁（整体 + 关键模块下限）；接入结构测试；消除 flaky；建立测试可维护性规则；测试纳入同一套门禁。
- **产出物**：测试代码 + fixture；覆盖率配置与报告；结构测试；测试策略文档。
- **验收标准**：覆盖率达标且关键模块单独达标；新增测试在故意破坏实现时失败（抽样≥3）；P0 修复有回归测试；全量测试约定时长内跑完且连续 3 次无失败；覆盖率/结构测试接入门禁（实测拦截）；测试文件纳入版本管理。
- **阶段 5 通过后**：执行 §14 收官拆解（归档施工图，长效契约迁移至 `AGENTS.md` / `core-beliefs.md` / `ARCHITECTURE.md`）。

## 6. 人工决策清单（已全部决策）

| 编号 | 问题 | 决策结果 | 影响范围 |
|---|---|---|---|
| D1 | 阶段 1 改造计划是否确认，进入阶段 2 | ✅ **确认，进入阶段 2** | 全局 |
| D2 | 根级四份大分析文档处置 | ✅ **归档至 `docs/design-docs/`**（不删除，有效条目迁移到 tracker/QUALITY_SCORE.md） | 阶段 2 |
| D3 | 是否增加版本一致性 guard job | ✅ **增加**（校验 tag == 代码版本声明） | 阶段 3 |
| D4 | hub/route API 认证是否维持现状 | ✅ **维持现状**（已 fail-closed + HMAC + token 脱敏） | 阶段 4 批次 10 |
| D5 | SEC-001 OPC UA 空安全策略处理 | ✅ **仅加告警**（slog.Warn，修复bug，不破坏现有部署） | 阶段 4 批次 5 |
| D6 | PERF-003 S7 N+1 是否合并连续地址 | ✅ **合并连续地址**（变更行为，单次失败影响多 tag，需行为测试覆盖） | 阶段 4 批次 6 |
| D7 | SEC-002 MQTT 命令转发是否 fail-closed | ✅ **fail-closed 拒绝启动**（command-topic 非空且 command-secret 空时拒绝启动，变更行为） | 阶段 4 批次 7 |
| D8 | PERF-004 MQTT PublishBatch 是否并发 | ✅ **改为并发**（fire-all + 统一 WaitTimeout，变更行为，需 QoS/顺序测试） | 阶段 4 批次 7 |
| D9 | SEC-003 HTTP webhook 是否 fail-closed | ✅ **fail-closed 拒绝启动**（webhook-addr 非空且 webhook-secret 空时拒绝启动，变更行为） | 阶段 4 批次 8 |
| D10 | SEC-004 WS permissive 模式是否改默认 | ✅ **保持 permissive**（改默认破坏 dashboard 跨域；WS 已在 authentication 组内） | 阶段 4 批次 10 |
| D11 | SEC-006 pprof 独立端口加固方式 | ✅ **强制 loopback**（校验 PprofAddr 为回环地址，拒绝非回环绑定） | 阶段 4 批次 10 |

## 7. 进度与决策日志

| 日期 | 阶段 | 动作 | 关联 commit | 决策与理由 |
|---|---|---|---|---|
| 2026-10-02 | 1 | 创建 `harnessing` 分支 | — | §7.1 禁止在主干改造 |
| 2026-10-02 | 1 | 放置 `docs/HARNESS-RULES.md`（规则副本，sha256 与附件一致） | — | 施工期常驻（§14.1） |
| 2026-10-02 | 1 | 框架判定 = 其他类型，写入 `docs/CI.md` | — | §3.2/§3.4 |
| 2026-10-02 | 1 | 七路并行扫描（ARCH/CPLX/DUP/DOC/TEST/SEC/PERF）完成 | — | §5 阶段1 子任务 3–9；75 条问题（P0×1, P1×27, P2×47）；原始扫描结果见 `scripts/analysis/phase1-findings.md` |
| 2026-10-02 | 1 | 综合扫描结果，填充本计划 §3/§4 与 `tech-debt-tracker.md`（75 条全量） | — | §5 阶段1 验收标准全勾 |
| 2026-10-02 | 1 | 人工决策 D1–D11 全部确认 | — | D1=进入阶段2；D2=归档 design-docs；D3=加 guard job；D4=维持 API 现状；D5=OPC UA 仅告警；D6=S7 合并；D7=MQTT fail-closed；D8=MQTT 并发；D9=webhook fail-closed；D10=WS permissive；D11=pprof loopback |
| 2026-10-02 | 2 | 阶段 2 文档对齐启动 | — | §5 阶段2 |
| 2026-10-02 | 2 | 创建 AGENTS.md(67行)、ARCHITECTURE.md、core-beliefs.md、QUALITY_SCORE.md、design-docs/index.md | — | 附录 E/F 骨架；§2.1 裁剪 |
| 2026-10-02 | 2 | 归档四份根级 AI 分析文档至 docs/design-docs/（D2） | — | git mv；不删除（R5） |
| 2026-10-02 | 2 | 修复 DOC-001(config.example.yaml 描述)、DOC-013(scale/offset 示例) | — | 文档与代码一致 |
| 2026-10-02 | 2 | 补全 10 包 // Package 注释（DOC-010）；链接孤儿文档 DOC-011/012 | — | doc.go 文件；VitePress sidebar |
| 2026-10-02 | 2 | 创建 scripts/check-docs.sh 文档防腐脚本 | — | 死链+必填节+孤儿+包注释检查；实测通过 |
| 2026-10-02 | 2 | 阶段 2 验收 6/6 通过 | — | check-docs.sh 全绿；AGENTS.md 67行；tracker DOC-001..013 状态更新 |
| 2026-10-03 | 3 | 阶段 3 门禁审计启动 | — | §5 阶段3；R3 先护栏后动手 |
| 2026-10-03 | 3 | 编写架构结构测试 `archtest/arch_test.go`（T7） | — | `go list -json` 解析 import 图；对照 ARCHITECTURE.md 允许边表；跨域禁止（driver/* 互禁、transport/mqtt↔httppush 互禁）；修复 ARCHITECTURE.md log 依赖条目 |
| 2026-10-03 | 3 | 编写品味不变量测试 `tastetest/taste_test.go`（T1+T6） | — | T1: ≤600 行 + 11 个豁免登记（CPLX-001..014）；T6: 禁止 fmt.Println/Printf 生产代码 |
| 2026-10-03 | 3 | 创建 Makefile + pre-commit hook | — | `make gates` 一命令复现；`make install-hooks` 安装 git hook |
| 2026-10-03 | 3 | CI 增量改造 `.github/workflows/ci.yml` | — | 新增 gates/doc-gate/coverage/version-check 四 job；未删现有 job；修复 paths-ignore |
| 2026-10-03 | 3 | 编写 `docs/gates.md` 门禁清单 + 豁免机制文档 | — | 10 项门禁 G1–G10；每项含触发/失败/修复/命令；nolint 须附原因+tracker ID |
| 2026-10-03 | 3 | 违规实测验证 | — | 注入跨域 import→T7 拦截✅；注入超长文件→T1 拦截✅；注入 fmt.Println→T6 拦截✅；干净环境全绿✅ |
| 2026-10-03 | 3 | 阶段 3 验收通过 | — | 10 项门禁全绿；违规实测拦截；失败信息含修复指引；本地一命令复现；豁免项有原因 |
| 2026-10-03 | 4 | 批次 1（common）启动 | — | §4 批次划分；DUP-001/002/003/005/007 + CPLX-026 |
| 2026-10-03 | 4 | DUP-005: 提取 `util.ToFloat64OK` | `fd2d59c` | engine/publish.go 删除本地 numericValue，委托 util |
| 2026-10-03 | 4 | CPLX-026: 提取 `util.ReconnectOpts` + `ReconnectLoopOpts()` | `fd2d59c` | 参数对象替代长参数列表 |
| 2026-10-03 | 4 | DUP-001: 创建 `driverbase.BaseDriver` 生命周期骨架 | `0232389` | ~296 行共享生命周期状态+方法+hook setter；modbus 首个迁移 |
| 2026-10-03 | 4 | DUP-001: opcua 迁移至 BaseDriver | `99d725e` | client.go 662→556 行；hooks 移至构造函数 |
| 2026-10-03 | 4 | DUP-001: s7 迁移至 BaseDriver | `7dd5a08` | s7.go 727→621 行；writeMu 保留（协议特定） |
| 2026-10-03 | 4 | opcua hooks 移至构造函数（Restart 安全） | `62d3055` | Restart-without-Init 不再 panic |
| 2026-10-03 | 4 | DUP-002: 提取 `common/testutil` 重连测试辅助 | `664d95a` | PollReconnectCount/PollReconnectCountGrowing/PollUntilConnected；modbus+s7 复用 |
| 2026-10-03 | 4 | DUP-003: 提取 `common/tlsutil` TLS 配置辅助 | `38db04a` | LoadCertPool/LoadClientCert/BuildTLSConfig；mqtt+modbus 复用 |
| 2026-10-03 | 4 | DUP-007: 归档（util.GetDurationSetting 已是单点实现） | `9327593` | 各调用点无额外重复验证逻辑 |
| 2026-10-03 | 4 | 批次 1 验收通过 | — | 全量 build+test+vet+archtest+tastetest+docs 全绿；行为守恒（R2） |
| 2026-10-03 | 4 | 批次 2（config）: CPLX-010 拆 config.go→config+env_expand+validate | `a00e24e` | 467→124+120+248 行；ARCH-002 注入 DriverRegistry/TransportRegistry 接口 |
| 2026-10-03 | 4 | 批次 2 验收通过 | — | 全量 build+test+vet+archtest+tastetest 全绿；行为守恒（R2） |
