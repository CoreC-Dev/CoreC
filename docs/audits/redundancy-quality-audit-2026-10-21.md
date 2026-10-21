# CoreC × CoreC-Dashboard 冗余消除与质量提升审计（2026-10-21）

> **目标**：对两仓消除冗余、提升代码与架构质量、优化文档、审视 Harness 工程化结构——先形成本文档，再按批次实施。
> **方法**：4 个并行审计 agent（后端冗余 / 前端冗余 / 文档质量 / Harness 工程化）+ 主会话独立交叉验证。每条发现要求 file:line 证据；关键断言由主会话二次复核（含编译验证）。
> **基线**：审计时两仓全绿——CoreC：golangci-lint v2.14.0 `0 issues`、vet/build/结构守卫 PASS、全量 -race 27 包绿；Dashboard：1229 测试全绿、lint/typecheck/structure/custom/any 门禁全过。
> **重大发现预告**：审计发现 **2 个真实的、跨仓库的功能性缺陷**（P0/P1 级——后端上一周期 #32 改造引入的前端契约漂移），在 1229 个绿测试下完全隐形。这直接验证了"测试只守护它们检查的东西"。

---

## 0. 优先级总表（全部发现汇总）

| ID | 级 | 仓库 | 发现 | 关键证据 |
|---|---|---|---|---|
| **F1** | **P0** | 跨仓 | `/logs` 帧的 `level` 契约断裂：后端 `slog.Level` 序列化为**字符串**（`"WARN"`，主会话编译验证），前端声明 `number` 并做数值比较——告警流、错误提示音、通知、未读徽标**全部静默失效**，EventLogTerminal 所有行显示 `[INFO]`。测试全绿因 mock 用的是数字 | CoreC log/log.go:19 ↔ Dashboard types/models.ts:110；AlertStreamProvider.tsx:92/102/119；EventLogTerminal.tsx:118-127 |
| **F2** | **P1** | 跨仓 | 诊断页"实时流量"组件**完全死亡**：后端 #32 改造后 `/traffic` 默认推速率帧（`read_rate/...`），该组件用累计帧 schema 校验——`safeParse` 100% 失败，永远显示等待中 | CoreC traffic.go:27-70 ↔ Dashboard types/api.ts:431-437（含错误注释）+ RealtimeStreamsPanel.tsx:87-104 |
| **F3** | **P1** | Dash | 安全文档虚假声明 ×2：(a) `SECURITY.md:9` 称密钥存 sessionStorage——实际 localStorage（instanceStore.ts:113-119），sessionStorage 助手是死代码；(b) 称 `GET /configs/raw` 密钥脱敏——实际**明文**（CoreC executor.go:98 注释明示 no redaction） | SECURITY.md:9 ↔ instanceStore.ts / lib/storage.ts:31,42；CoreC 其余文档均正确标注明文 |
| **H1** | **P0** | CoreC | **lint 事件根因是结构性的**：golangci-lint v2.14.0 只在 ci.yml:46 一处钉版；本地 `make lint` 用 PATH 上任意版本；孤儿 `scripts/pre-commit.sh`（0 引用、644 不可执行）**吞掉 lint 失败并隐藏输出**（`2>/dev/null \|\| echo "skipping"`）——git 史上 8+ 次反复 fix-lint commit 的确切机制 | ci.yml:46 / Makefile:16 / pre-commit.sh:25-28 |
| **H2** | **P0** | CoreC | `version-check` CI job **不可达死代码**（`if: tag` 但 `on.push` 只过滤 branches → 分支过滤工作流永不因 tag 触发）——D3 版本一致性不变量**无任何保护**，而 docs/CI.md:24-25 与 gates.md G9 声称已保护 | ci.yml:136 vs ci.yml:5-6 |
| **H3** | **P0** | Dash | 生产反向代理 `server.mjs`（16KB）**游离于全部门禁**：`biome check src` 不含它，实测 **8 个 lint 错误**；无任何测试 | package.json:9 vs biome.json:7 |
| **B1** | **P1** | CoreC | 结构守卫缺口：`alertManager` 与 `scheduler` 各 **13 方法 > 12 上限**，且双双不在 structure_test 的守卫名单里——CPLX-020/025 验收被两个后长的角色静默击穿 | engine/alerts.go、scheduler.go vs structure_test.go:43-60,137-142 |
| **B2** | **P1** | CoreC | `applyEngineConfig` 圈复杂度 **20 = CI 上限**（再加一个分支即红）；且 CoreCEngine 被钉死 10 字段，无法再加角色指针——真·绊网 | engine/runtime.go:458-507 |
| **B3** | **P2** | CoreC | 死代码一批：`tlsutil.BuildTLSConfig`+`TLSOptions`（零调用，两消费者只用 loader 并自建 tls.Config——语义确实不同）；`driverbase.SetLastError/RecordRead`、`log.InitBuffer/Warnln`、`core.DefaultCircuitBreakerBackoff`（与 util 的未导出孪生死 Sync 已然失效） | tlsutil.go:42,52；driverbase.go:259,270；log.go:46,254；core/types.go:377 |
| **B4** | **P2** | CoreC | `driver/settings_scan.go` 是**生产文件但只有测试导入**——还制造 archtest 看不见的 transport→driver 测试依赖；`go list -deps ./cmd/corec` 证实 `driver` 包不可达 | settings_scan.go:8-25 |
| **B5** | **P2** | CoreC | 守卫薄弱点：archtest 对未知包 **allow-by-default**（新包绕过依赖表直到手动登记）；taste 豁免无尺寸钉版（publisher.go 记 733 实 768，LWT commit 增长 35 行无人知）；route↔executor 的错误分类用**裸 int**（两边各自定义 0/1/2，重编号可静默错路由 404/400/500） | arch_test.go:87-89；taste_test.go:36-38；resource_delete.go:27-38 ↔ errors.go:44-52 |
| **B6** | **P2** | CoreC | 文档数字漂移：QUALITY_SCORE.md 称"0 生产函数 gocyclo>15"——实为 applyEngineConfig=20、parseS7Address=18、ApplyConfig=17、getWriteHistory=16 等；"37 forwarders" 实 55；"4 files >400" 实 15 | QUALITY_SCORE.md:54-56 |
| **B7** | **P3** | CoreC | 清理项：命名不一致（deadletter_store.go 文件名 vs deadLetterStore 类型 vs dead-letter-path 键）；5 处引用已删除的 engine/publish.go 的陈旧注释；`waitFor` 测试助手 e2e/engine 字节级重复；notify 手写 settings 拒绝逻辑（首键不确定序、错误格式不一致）；route 测试 9 个 engine mock 可合并；parse-duration-or-default 习语 ~15 处；死 API（RuleProvider.Initial、core.Scheduler 接口无真实消费、SpanNameFromContext、ReconnectLoopWithBreaker 测试专用） | 各 file:line（见批次 C） |
| **B8** | **P2** | CoreC | 引擎外的巨型结构：MQTTTransport **50 字段/24 方法**、HTTPTransport 25/17、BaseDriver 23/38（含导出的互斥锁方法）、OPCUADriver 21/13——10/12 上限只罩引擎。抽查 MQTT 10 字段全在用——是体量非死重；**与 CPLX-001 publisher 拆分同批处理** | publisher.go:35-127 等 |
| **D1** | **P2** | Dash | **7 个零调用的 API 端点函数**（上一周期引入的 staged surface）：`getTagsFiltered`（TagExplorerPage 仍全量拉取+客户端过滤——后端 #35 过滤器白建了）、`writeTagBatch`、`getConfigAudit`、`deleteDriver/Transport/Rule`（UI 删除走 config-PUT 整份回传——后端 #26 资源级 DELETE 白建了）、`getWriteHistoryFiltered`（合规审计全量 1000 条无过滤）。**决策项**：接线（后端功能是为此 UI 建的）或删除 | endpoints/index.ts:216-297 + 各页面现状 |
| **D2** | **P2** | Dash | 重复脚手架：MemoryChart/TrafficChart 图表样板 ~40 行重复；tracked-WS 手写序列 ×3；Drivers↔Transports 页族 62% 行相同（含 DriversPage 内部 config 区/运行区卡片近乎复制）；**6 个 500 行豁免全部仍超标**（RuleGroupEditor **890** 最甚，内置 InlineRuleEditor ~266 行/GroupCard ~252 行是天然拆分线），RuleWizard 498 距新豁免 2 行 | custom-lint.mjs:21-27 + wc -l 实测 |
| **D3** | **P3** | Dash | i18n：键位对账**完美**（1533=1533，含插值占位符测试）；但 **216 键（14%）零引用**（drivers/transports.editConfig.* 旧字段标签为主——被 settingsRegistry 取代）+ 1 处硬编码 aria-label（ConfigCenterParts.tsx:265） | node 扫描 + 10 键逐一 grep 复核 |
| **D4** | **P3** | Dash | 分层漂移：api/hooks→stores ×3（structure-lint 的 FORBIDDEN.api 漏 'stores'）+ WSStatus 类型反向边；其余**全部干净**（含跨 feature 导入为零——逐一验证过） | api/hooks/driverPause.ts:3 等 |
| **D5** | **P3** | Dash | 死导出：6 个类型别名（HealthzLiveResponse 等）、NOTIFY_CHANNELS（词汇硬编码在 configSchema.ts:251）、~40 个仅本文件使用的多余 export、~20 个 shadcn 备件；测试助手重复（createWrapper ×4、jsonRes ×5、mockFetch ×5）——setup.ts 只有 3 行 | types/api.ts:194,221,420 等 |
| **D6** | **P1** | 跨仓 | 边界纪律缺口（F1/F2 的温床）：/logs 帧与 TrafficChart 的 /traffic 帧**无 zod 校验**，RealtimeStreamsPanel 用错 schema 校验——4 个消费方 4 种契约 | types/api.ts 无 logEventSchema |
| **G1** | **HIGH** | CoreC | **两个巨型参考文档已全面过期**：`docs/api/COREC_API_CONTRACT.md`（自称"精确、源派生"）缺整批新端点（configs/audit、write/batch、drivers/paused、pause/resume、3×DELETE、全部 alerts 端点），且与代码直接矛盾（"No query params" vs #35 过滤器；"log-level only" vs 3 键；struct 行号全漂）；`docs/API_REFERENCE.md`（自称"前端重写的唯一事实源"）同样 | 两文档 vs router.go:60-112 |
| **G2** | **HIGH** | CoreC | **重复是根因**：端点清单并行存在 **5 份**（README ✓ / overview ✓ / 主题页 ✓ / 两个过期巨型参考 ✗）——上周期机械对账同步了前三者 | 文档结构 |
| **G3** | **HIGH** | CoreC | 计划的权威记录**导航不可达**：production-hardening.md 0 入链（自家 gate 报警）、审计文档只被该孤儿引用；audits/exec-plans/gates/QUALITY_SCORE/CI/design-docs 整簇零可达入链（孤儿检查是 basename 制，被 AGENTS.md 提及即算非孤儿） | check-docs.sh:96-118 + 全仓 grep |
| **G4** | **MED** | CoreC | 文档对账缺口：websocket.md 仍称"四个端点"（实 5）；/configs/audit 文档缺 `details` 字段与 delete-* 键形、400 示例仍是旧 "log-level only"；write-audit source 枚举缺 `api-batch`（4 处）；notify 传输在 guide 簇缺席（installation/introduction/transports 三页）；config 参考缺 log-format/metrics-addr 行；alerts.md 缺侧边栏条目；middleware 链缺 httpMetrics 一步 | 各 file:line |
| **G5** | **LOW** | 两仓 | AGENTS.md/README 引用不存在的 `docs/exec-plans/active/`（4 处）；Dashboard README 文档节缺 8 个目标；LogEvent 注释 "-4=debug" 应为 **-8**（slog.LevelDebug=-8，EventLogTerminal 的 `<= -4` 只是碰巧工作）；docs/diagrams 与 docs/public/diagrams **1.26MB 字节级重复** | 各 file:line |
| **H4** | **P1** | CoreC | CI 弱于本地 ×2 + 浪费：覆盖率门禁 CI 只查总 75%（本地按包查 core95/hub90/route85…）；`paths-ignore: docs/**` → **纯文档 PR 零 CI 包括 doc-gate 自身**；测试每 run 跑 3 遍（test job 已含 archtest/tastetest，gates job 再跑，coverage 再跑全套）；无 concurrency/timeout；lint 每次源码编译不缓存；**发布矩阵缺口：windows/arm64 与 linux/armv7 发布但从不 build-check**；release 只跑 test+vet 不跑全门禁 | ci.yml:34,91,94,120,123-131,7-16,55-57；release.yml:27-31,47-59 |
| **H5** | **P2** | Dash | CI 浪费：PR 双触发（push+PR 各一次）；测试双跑（test + coverage）；deploy 重建 CI 已建的 dist（workflow_call 复用了 CI 但产物弃用）；pre-commit 缺 typecheck；docs/CI.md 谎称用 lint-staged（不存在） | dash ci.yml:7-9,57-60；deploy.yml:54-58 |
| **H6** | **P3** | 两仓 | 卫生项：action 未 SHA 钉版（含第三方 contents:write 的 gh-release）；go '1.27.1' 硬编码 7 处（应 go-version-file）；Node 24 硬编码且无 engines/.nvmrc；.editorconfig 双缺；Dashboard 无 LICENSE；无 dependabot/CODEOWNERS/模板/SECURITY.md(CoreC)；.gitignore 陈旧项 | 各处 |

**已验证为健康、明确不动的**（防误伤清单）：单实现角色接口是**有意的**消费侧能力模式（hub/route 类型断言 + mock 供 501 路径）——不要内联；modbus 与 s7 的分段合并是**不同谓词的平行算法**，非复制——不要盲目统一；MQTT HMAC 命令认证与 webhook bearer 认证**不同方案**；死信落盘与离线缓冲**不同设计**；config.example.yaml 有机械校验（load_test.go:453）；错误包装全仓一致（158 处 %w，9 处 %v 全是格式化值）；HTTP 错误渲染单源；全局可变状态全部是有据的破环/热载原子。CoreC README/overview/主题页端点表**全部当前准确**；Dashboard 类型矩阵与后端逐字段核对——除 F1/F2 外**全部匹配**。

---

## 1. 实施批次规划

**总原则**：每批次独立可验证（两仓门禁 + 测试全绿 + CI 绿）；重构零行为变化（F1/F2/F3 除外——它们是修 bug）；每项删除以"引用清零 + 全绿"为证；遵守 R2/B11 纪律。

### 批次 0：跨仓契约修复（F1/F2/F3 + D6）——最高优先级
1. **F1 修复**：WS 边界归一化——`logEventSchema`（z.preprocess 把 `"DEBUG"/"INFO"/"WARN"/"ERROR"`（含 `"DEBUG-4"` 形式）映射为数值，数字原样通过）；AlertStreamProvider/EventLogTerminal 改用归一化值。**决策**：归一化在前端做（blast radius 小；后端改 Event 结构会破坏既有消费者）。测试：feed 字符串 level 帧的用例。
2. **F2 修复**：`trafficFrameSchema` 改判别联合（rate \| totals）对齐 models.TrafficFrame；RealtimeStreamsPanel 直连 `read_rate`（或传 `mode:'totals'` 并修注释）；TrafficChart 帧也补校验（D6）。
3. **F3 修复**：SECURITY.md 两处改为事实（localStorage + 明文 + 认证门禁姿态）；删死 sessionStorage 助手（如维持 localStorage 决策）。
4. **D6 收口**：边界 schema 化作为防再发机制（/logs、/traffic 双模式）。

### 批次 H：Harness 修复——第二优先（保护后续所有批次）
1. **H1**：Makefile `GOLANGCI_VERSION ?= v2.14.0` + `make lint` 自装钉版；ci.yml 改 `run: make lint`；删孤儿 pre-commit.sh；`make install-hooks` 装唯一钩子（lint 硬失败，保 `--no-verify` 逃生口）。
2. **H2**：version-check 迁 release.yml 首个 job，行为断言（ldflags 注入构建后 `./corec -version == $TAG`）；同步修 docs/CI.md:24-25 与 gates.md G9 的虚假声明。
3. **H3**：Dashboard lint 脚本改 `biome check .`；修 server.mjs 8 错误；加冒烟测试（启动 + GET / + WS 代理探测）。
4. **H4**：coverage job 调 `scripts/check-coverage.sh`；新增 docs 路径触发工作流跑 check-docs.sh；gates 并入 test job；concurrency + timeout-minutes；发布矩阵对齐 build-check；release 跑全门禁。

### 批次 C：后端清理（B1-B8）
1. **B1**：alertManager/scheduler 入守卫名单并各减一方法（fireCounts/activeCount 移入消费侧或助手文件）。
2. **B2**：applyEngineConfig 按角色拆分（applyProcessing/Lifecycle/Writes/ReadsConfig）。
3. **B3+B4**：删死代码（tlsutil.BuildTLSConfig+TLSOptions、5 个死导出符号、RuleProvider.Initial 等）；settings_scan.go 移 common/testutil、删 `driver` 根包。
4. **B5**：archtest default-deny；taste 豁免加尺寸钉版；route↔executor 错误类改导出常量。
5. **B6**：QUALITY_SCORE 数字重算或改机械生成。
6. **B7**：命名/git mv、陈旧注释、waitFor 入 testutil、notify 用共享 RejectUnknownSettings、mock 合并、ParseDurationOr 助手。
7. **B8（大项，与 CPLX-001 合并）**：publisher.go 拆分时同步分解 MQTTTransport（settings/TLS/commands 子结构）；BaseDriver 的导出互斥锁方法收敛。

### 批次 F：前端清理（D1-D5 + 剩余）
1. **D1 决策**：staged API 逐个定夺（推荐：getTagsFiltered 接入 TagExplorerPage——千 tag 性能收益且代码已写好；getWriteHistoryFiltered 接入合规表格；3×DELETE 接入 DeleteEntityConfirm（替代整份 config-PUT 回传）；getConfigAudit 建 Config Center 审计视图；writeTagBatch 视 BatchWritePanel 是否切换）。未接线的删除。
2. **D2**：`useLiveChartStream` hook + 共享轴/tooltip 常量；`createTrackedStream` 工厂；RuleGroupEditor 拆 InlineRuleEditor/GroupCard（890→达标）；DriversPage 拆 DriverCard；页族脚手架组件抽取（EntityListPageLayout 等）——**豁免表逐项清空**。
3. **D3**：216 死键删除（双包同删保对齐）+ 1 硬编码 aria-label 补 t() + 未用键扫描脚本入 CI（agent 留有现成扫描器）。
4. **D4**：api/hooks 的 react-query 钩子上移 src/hooks（~40 调用点）或 structure-lint 显式例外——二选一并记录 ADR。
5. **D5**：删死类型别名/NOTIFY_CHANNELS 统一/configSchema 词汇单源化；去多余 export；测试助手入 src/test/helpers.tsx。

### 批次 G：文档（G1-G5 + 各仓零散）
1. **G1/G2 结构决策**：巨型参考二选一——(a) 从 router.go 重新生成并加 CI 同步检查；**(b) 推荐：退役**——overview 为索引、主题页为细节的单一来源已成，两个巨型参考移出侧边栏并加"历史快照"标注（同 QUALITY_ASSESSMENT 处理）。
2. **G3**：新增"工程/质量"侧边栏组（audits/exec-plans/gates/QUALITY_SCORE/CI/design-docs）；README 或首页链入 production-hardening + 审计文档。
3. **G4**：逐项对账修复（websocket 五端点、audit 的 details/delete-键/source 枚举 api-batch ×4、notify 入 guide 簇、config 参考补行、alerts 入侧边栏、middleware 补步）。
4. **G5**：AGENTS/README 死引用、LogEvent 注释 -8、diagrams 去重。
5. CoreC 端 QUALITY_SCORE/孤儿对账（H6 中 doc-gate 部分）。

### 批次 W：仓库卫生（H6）
.editorconfig×2、Dashboard LICENSE、dependabot×2、action SHA 钉版、go-version-file、Node pinning（engines/.nvmrc）、.gitignore 陈旧项清理、CODEOWNERS/模板/SECURITY.md（可选）。

---

## 2. 验收标准

1. **每批次**：CoreC `make gates` + 全量 -race 绿；Dashboard lint/typecheck/structure/custom/any + 1229+ 测试全绿；push 后 CI 绿（含新对齐的更强门禁）。
2. **批次 0 特殊验收**：F1/F2 修复必须新增"字符串 level 帧"与"rate 帧"的回归测试（防再发）；修复后 EventLogTerminal 与实时流量面板在真实后端下工作（源级验证）。
3. **重构零行为**：既有测试不改语义全过；F1/F2/F3 是 bug 修复，登记 R2。
4. **豁免只减不增**：Dashboard SIZE_EXEMPTIONS 批次 F 结束时应为 0 或有记录的余量；CoreC taste 豁免加钉版。
5. **守卫收紧**：B1/B5 完成后，任何新超标/新包/新未登记角色都会 CI 失败（这是目的）。

## 3. 审计方法附注（可复算性）

- 死代码：golangci-lint unused + AST 全量声明-vs-引用交叉核对（293 文件）+ 逐一 grep 复核（假阳性率实测高——主会话第一轮普查 12 个 lib"未用"候选 11 个为假阳性）。
- 契约漂移：编译验证（slog.Level 序列化）+ 前端逐消费点阅读；**未做活体验证**（agent 明示：P0/P1 为源级证明，建议修复前 10 分钟活体确认）。
- 相似度百分比（62%/52%）是 rename 归一化行 diff 启发式，非语义等价。
- VitePress 生产构建未跑（docs/node_modules 缺失）；静态审计 26 个侧边栏目标全部存在。
- 未覆盖：大结构体全字段读写分析（抽查 10/50 全在用）、demo/chained 9 个 README 深审、外链检查。
