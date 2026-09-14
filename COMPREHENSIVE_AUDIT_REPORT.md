# CoreC 全面审计报告 (Comprehensive Audit Report)

**项目:** CoreC (Connect · Collect · Control) — 工业物联网数据采集与控制核心  
**规模:** 116 Go 文件, ~26,192 行代码, Go 1.27.1  
**审计维度:** 8 个并行 Agent 审计  
**构建状态:** ✅ 编译通过 · `go vet` 通过 · 全部测试通过 (315 tests, 16 benchmarks)  
**测试覆盖率:** 22.7% ~ 100% (各包差异大)

---

## 目录

1. [执行摘要](#1-执行摘要)
2. [严重性统计](#2-严重性统计)
3. [Critical 级别发现](#3-critical-级别发现)
4. [High 级别发现](#4-high-级别发现)
5. [Medium 级别发现](#5-medium-级别发现)
6. [Low/Info 级别发现](#6-lowinfo-级别发现)
7. [积极发现](#7-积极发现)
8. [优先修复路线图](#8-优先修复路线图)
9. [各维度详细审计索引](#9-各维度详细审计索引)

---

## 1. 执行摘要

CoreC 是一个架构良好的工业 IoT 数据采集核心，采用六边形插件化架构，代码质量整体达到 **B+** 水平。`core` 包零内部依赖，工厂注册模式清晰，Modbus 驱动族的模板方法设计堪称典范。项目具备生产级容错机制（断路器、离线缓冲、死信队列、调度器自动降级）和全面的测试套件。

然而，审计发现了 **5 个 Critical** 和 **23 个 High** 级别问题，主要集中在：
- **安全防线**：MQTT 命令通道无认证、Webhook 无认证且无 body 大小限制、空密钥禁用认证
- **数据正确性**：OPC UA 写入结果索引错位、Modbus 非布尔类型在线圈区静默读错数据区
- **可靠性**：WebSocket 被 30s 超时强制断开、标签文件监视器 goroutine 泄漏、优雅退出使用 abrupt Close
- **配置验证**：示例配置损坏、多个配置字段未验证

---

## 2. 严重性统计

| 严重性 | 数量 | 说明 |
|--------|------|------|
| 🔴 **Critical** | 5 | 必须立即修复 — 安全漏洞或数据正确性 bug |
| 🟠 **High** | 23 | 应在下一个版本修复 — 安全、可靠性、数据完整性 |
| 🟡 **Medium** | ~50 | 计划修复 — 健壮性、性能、代码质量 |
| 🟢 **Low** | ~35 | 改进项 — 代码规范、文档、次要优化 |
| ℹ️ **Info** | ~20 | 积极发现或信息性说明 |

---

## 3. Critical 级别发现

### C1. MQTT 命令通道无消息级认证 — 远程物理执行器控制
- **文件:** `transport/mqtt/publisher.go:258-290`
- **维度:** Security
- **描述:** 当配置 `command-topic` 时，MQTT 传输订阅该主题并将每条消息直接反序列化为 `WriteCommand` 下发到驱动器执行物理写入。无任何消息级认证、授权或签名检查。
- **攻击场景:** 工业环境中写入命令控制物理执行器（阀门、电机、泵）。若 MQTT broker ACL 配置不当或使用公共 broker（如示例中的 `tcp://broker.emqx.io:1883`），攻击者可发布 `{"driver":"plc1","tag":"emergency_stop","value":true}` 触发物理安全事件。
- **修复:** 要求 broker 端 ACL 限制；添加可选 HMAC 签名验证；配置校验时警告无 TLS 的命令通道；考虑可写 `{driver, tag}` 白名单。

### C2. OPC UA Write 结果索引错位 — 写入成功/失败状态错误
- **文件:** `driver/opcua/client.go:583-592`
- **维度:** Drivers / Error Handling
- **描述:** `Write` 方法跳过无效命令（标签未找到、变体错误）后仅追加有效命令到 `writeValues`。但响应循环 `for i, code := range resp.Results { results[i] = ... }` 用 `resp.Results` 的索引（对应 `writeValues`）直接写入 `results`（对应 `commands`）。当有命令被跳过时，后续结果写入错误位置。
- **影响:** 批量写入中部分标签无效时，调用者收到错误的成功/失败状态。在工业控制场景中，操作员可能误认为 PLC 设定值已写入而实际未写入，或反之。
- **修复:** 维护平行 `validIndices []int` 切片，映射 `results[validIndices[i]] = ...`。

### C3. WebSocket 被 Server ReadTimeout/WriteTimeout 强制断开
- **文件:** `hub/route/server.go:130-132`
- **维度:** Transport & API
- **描述:** `http.Server` 配置了 `ReadTimeout: 30s` 和 `WriteTimeout: 30s`。这些是底层 `net.Conn` 的绝对截止时间。`coder/websocket` 库劫持连接但不清除这些截止时间。30 秒后服务器施加的读取截止时间触发，WebSocket 连接被强制终止。
- **影响:** 所有四个流式端点（`/tags/stream`, `/logs`, `/traffic`, `/memory`）在 30 秒后被强制断开。完全破坏超过 30 秒的实时流式会话。测试未捕获此问题因为测试在 <3s 内完成。
- **修复:** 设置 `ReadTimeout: 0` 和 `WriteTimeout: 0`，依赖 `ReadHeaderTimeout` 防止 header DoS，依赖 `wsWriteTimeout` 控制写入超时。

### C4. DataBus 订阅过滤器被静默忽略 — 所有订阅者收到所有数据
- **文件:** `engine/databus.go:128`
- **维度:** Engine & Rules
- **描述:** `Subscribe(filter string)` 存储 filter 但 `Broadcast()` 从不检查 `sub.filter`，所有订阅者收到所有数据点。WebSocket 处理器 `streamTags` 传递 `?driver=` 作为过滤器，但实际无效果。
- **影响:** 误导性 API，静默忽略参数。在高频多驱动器场景中，每个 WebSocket 客户端收到所有驱动器的数据，造成不必要的带宽和 CPU 消耗。
- **修复:** 在 `Broadcast` 中实现过滤器匹配（比较 `point.Driver` 与 `sub.filter`），或移除参数并文档说明订阅不过滤。

### C5. 示例配置损坏 — 活跃规则引用已注释的传输
- **文件:** `config.example.yaml:370-417`, `config/load_test.go:206-222`
- **维度:** Config & Testing
- **描述:** 示例配置有两个未注释的规则（`high-temp-alert` → `cloud-mqtt`, `default-catch-all` → `mes-http-push`），但所有驱动器和传输都被注释掉。加载它会导致验证失败。测试 `TestLoadExampleConfig` 掩盖了此问题，接受任何 `"config validation failed"` 错误。
- **影响:** 新用户复制示例作为起始配置会立即遇到验证失败。CI 认为"通过"但示例不可用。
- **修复:** 注释掉两条活跃规则，或取消注释一个最小驱动器 + 两个引用的传输。加强测试断言示例完全验证通过。

---

## 4. High 级别发现

### 安全类

| ID | 文件 | 问题 |
|----|------|------|
| H1 | `transport/httppush/push.go:287` | **Webhook 无认证 + 无 body 大小限制** — 任意网络可达客户端可注入数据点；`io.ReadAll` 无限制导致 OOM DoS |
| H2 | `hub/route/server.go:269-278` | **速率限制器信任可伪造的 `X-Real-IP`** — 攻击者每请求设唯一 IP 完全绕过限速；buckets map 无限增长导致 OOM |
| H3 | `hub/route/stream.go:12` (+3) | **无 WebSocket 连接数限制** — 数千连接耗尽 goroutine/文件描述符/内存 |
| H4 | `transport/httppush/push.go:163` | **Webhook 入站服务器使用明文 HTTP** — 无 TLS 选项，数据在传输中暴露 |
| H5 | `config/config.go:49` | **`tags-file` 路径未验证** — 认证后可通过配置重载读取任意文件（`/etc/passwd` 等） |
| H6 | `hub/route/server.go:175-177` | **空密钥禁用所有认证（fail-open）** — `api.secret: ""` 时所有端点公开可访问，包括 `POST /write` 和 `PUT /configs` |

### 可靠性类

| ID | 文件 | 问题 |
|----|------|------|
| H7 | `engine/engine.go:643` + `tagfile.go:88` | **标签文件监视器 goroutine 泄漏** — 每次 tags-file 变更泄漏一个 goroutine，N 次变更后 N+1 个监视器并发轮询同一文件 |
| H8 | `engine/engine.go:1113,1212` vs `:429` | **WaitGroup Add/Wait 竞态** — `AddTransport` 中的 `wg.Add(1)` 与 `Stop()` 中的 `wg.Wait()` 可能并发执行 |
| H9 | `hub/route/server.go:151-158` | **CloseServer 使用 abrupt Close()** — 杀死进行中的请求，可能留下引擎不一致状态 |
| H10 | `transport/mqtt/publisher.go:239-252` | **MQTT Start() 连接失败时静默成功** — auto-reconnect 禁用时传输永远卡在 StateConnecting |
| H11 | `transport/httppush/push.go:210-269` | **HTTP Push RetryCount 配置被忽略** — 瞬态故障无重试，数据永久丢失 |

### 数据正确性类

| ID | 文件 | 问题 |
|----|------|------|
| H12 | `driver/modbus/modbus_base.go:341-346` | **Modbus 非布尔类型在线圈/离散输入区静默读写错误数据区** — `uint16` 在地址 `00001`（线圈）静默读取保持寄存器 0 |
| H13 | `driver/modbus/modbus_base.go:235` | **Modbus 声称 BatchRead=true 但逐标签请求** — 100 个标签产生 100 次网络往返而非 ≤1 次 |
| H14 | `driver/s7/s7.go:600` | **S7 `decodeS7Buffer` 在地址类型/数据类型大小不匹配时 panic** — `DB1.DBB0` 配 `type: uint16` 读取 2 字节从 1 字节缓冲区 |
| H15 | `engine/engine.go:78` | **死信队列仅内存** — 重启/崩溃后所有失败写入丢失 |
| H16 | `engine/engine.go:1191` | **死信队列无重放/重试/清除机制** — 条目永久卡住 |

### 架构类

| ID | 文件 | 问题 |
|----|------|------|
| H17 | `hub/route/server.go:58-78`, `hub/executor/executor.go:17-22` | **Hub 包级全局变量阻止多实例嵌入** — `var engine core.Engine` 等全局变量使同一进程无法运行两个 CoreC 实例 |
| H18 | 3 个驱动文件 | **`handleConnectionLost` / `startReconnectLoop` / `Restart` 重复 3×** — 所有驱动族中结构完全相同 |

### 测试覆盖类

| ID | 文件 | 问题 |
|----|------|------|
| H19 | `driver/opcua/`, `driver/s7/` 测试 | **OPC UA 和 S7 无集成测试** — 核心 Read/Write 路径零测试覆盖 |
| H20 | `transport/mqtt/publisher_test.go` | **MQTT 覆盖率 45.4%** — publish/subscribe 路径未测试 |
| H21 | `.github/workflows/` | **CI 无安全扫描** — 无 govulncheck/CodeQL/gosec/Dependabot |
| H22 | `.github/workflows/ci.yml:35` | **CI 无代码覆盖率收集/报告/阈值** |
| H23 | `hub/route/tags.go:33-37` 等 | **所有列表端点无分页** — 10,000+ 标签生成多 MB JSON 响应 |

---

## 5. Medium 级别发现

### 安全

| ID | 文件 | 问题 |
|----|------|------|
| M1 | `hub/route/server.go:329` | API token 通过查询参数传递 — URL/代理日志/浏览器历史中暴露 |
| M2 | `hub/route/server.go:218` | CORS 默认 `ACAO: *` — 允许任意源跨域请求 |
| M3 | `driver/opcua/client.go:107` | OPC UA 默认 `None` 安全/匿名 — 未设置时无加密无认证 |
| M4 | `transport/mqtt/publisher.go:187` | MQTT TLS 不可配置 — 无 CA/mTLS/验证选项 |
| M5 | `hub/route/traffic.go:22` | WS `interval` 参数无下限 — `1ns` ticker CPU-spin DoS |
| M6 | `hub/route/server.go:174` | 速率限制默认禁用 |
| M7 | `transport/mqtt/publisher.go:buildTopic` | MQTT 主题模板注入 — 攻击者控制的 DataPoint 字段可注入 `/`, `+`, `#` |

### 可靠性

| ID | 文件 | 问题 |
|----|------|------|
| M8 | `common/util/util.go:233` | "断路器"非真正断路器 — 仅增加退避到 5min，永不停止尝试，无 open/half-open 状态 |
| M9 | 各驱动 | 退避无 jitter — 多设备同时故障时 thundering herd 风险 |
| M10 | `driver/modbus/modbus_base.go:264` | Modbus 重试用 `time.Sleep`（不可取消）— 阻塞优雅退出 |
| M11 | 全代码库 | `errors.Is`/`errors.As` 使用零次 — 错误分类依赖脆弱的字符串匹配 |
| M12 | 引擎处理循环 | 处理循环或告警处理器无 `recover()` — panic 永久杀死 worker 无重启 |
| M13 | 批量重试 | 批量重试重发整个批次 — 部分成功时导致下游重复数据 |

### 驱动

| ID | 文件 | 问题 |
|----|------|------|
| M14 | `driver/modbus/modbus_base.go:559` | 地址解析器 fallback 静默映射未知范围到保持寄存器 |
| M15 | `driver/opcua/client.go:452` | OPC UA Read 静默丢弃未知标签（与 Modbus 不一致） |
| M16 | `driver/opcua/client.go:443` | OPC UA Read 不按 maxBatchSize 分块 — 大请求可能超出服务器限制 |
| M17 | `driver/s7/s7.go:257` | S7 "批量"是表面分块，非协议级批量读取 |
| M18 | `driver/modbus/modbus_base.go:355` | Modbus 硬编码大端字节序 — 无法配置（许多设备使用小端或字交换） |
| M19 | `common/util/util.go:192` | 缩放对 int64/uint64/int8/uint8/bool/string 类型静默忽略 |
| M20 | `driver/s7/s7.go:626` | S7 支持 int64/uint64 写但不支持读（类型不对称） |
| M21 | 各驱动 | `lastRead` 即使所有读取失败也更新 — 掩盖持续故障 |
| M22 | `driver/s7/s7.go:389` | S7 位写入非原子读-改-写 — 并发下丢失更新 |

### 传输 & API

| ID | 文件 | 问题 |
|----|------|------|
| M23 | `transport/mqtt/publisher.go:362` | MQTT 断连期间无发布重试/队列 — QoS 0 消息丢失 |
| M24 | `transport/mqtt/publisher.go:408` | PublishBatch 串行化所有发布 — 无流水线 |
| M25 | `transport/httppush/push.go:206` | BatchSize/FlushInterval 配置字段未使用 |
| M26 | `transport/httppush/push.go:200` | Stop() 关闭 commandCh 但不关闭 dataCh — goroutine 泄漏 |
| M27 | `hub/route/stream.go:12` | WS origin 检查与配置的 AllowedOrigins 不一致 |
| M28 | `hub/route/stream.go:27` | 事件驱动流无 keepalive — 死连接上 goroutine 泄漏 |
| M29 | `transport/parser/parser.go:196` | 畸形时间戳静默替换为 "now" |
| M30 | `transport/parser/parser.go:333` | parseScalar 静默返回零值 — 数据损坏 |
| M31 | `hub/route/configs.go:83` | updateConfigs 对客户端配置错误返回 500 而非 400 |

### 配置

| ID | 文件 | 问题 |
|----|------|------|
| M32 | `config/config.go` (validate) | 引擎持续时间（shutdown-timeout 等）加载时未验证 |
| M33 | `config/config.go` (validate) | `on-bad-quality` 未验证 — 无效值静默默认 |
| M34 | `config/config.go` (validate) | `node.role` 未验证 |
| M35 | `config/config.go` (validate) | `rule-providers` 和 `rule-groups` 未验证 |
| M36 | `.golangci.yml:6` | 缺少 `gosec`（安全）和 `errorlint` linter |
| M37 | 12 个测试文件 | `time.Sleep` 在 30 处使用 — CONTRIBUTING 禁止固定时序并发对齐 |

### 引擎

| ID | 文件 | 问题 |
|----|------|------|
| M38 | `rule/engine.go:314` | 死规则索引 — `byTag`/`byDriver`/`allRules`/`other` 在 SetRules 中构建但 Match 总是线性扫描 `e.rules` |
| M39 | `engine/engine.go` (Reload) | 非原子 Reload — 无回滚机制 |
| M40 | `engine/engine.go` | 无发现节点驱逐 — 离线节点永远留在发现表中 |
| M41 | `engine/engine.go:927` | `Stats()`/`Subscribe()` 在 Start() 前调用 nil panic |

---

## 6. Low/Info 级别发现

### 关键 Low 级别发现

| ID | 文件 | 问题 |
|----|------|------|
| L1 | `config.example.yaml` | 弱/可猜测的示例密钥 `corec-secret-token` |
| L2 | 22 个文件 | **gofmt 格式不一致** — 22 个文件有格式问题（主要是 const 块对齐） |
| L3 | `core/driver.go:29` | `Capabilities()` 定义但从未被引擎消费 |
| L4 | `core/types.go:124` | `DataPoint.Device` 从未填充 — 每个发布 JSON 中 `"device":""` |
| L5 | `core/scheduler.go:18` | `Scheduler.ResumeDriver()` 声明但从未调用 |
| L6 | `common/util/util.go:223` | `ReconnectLoop()`（非断路器变体）从未调用 — 死代码 |
| L7 | `engine/engine.go` (1522 行) | God struct — 10+ 关注点在一个文件/结构体中 |
| L8 | `core/engine.go:9` | Engine 接口 20+ 方法 — 违反接口隔离原则 |
| L9 | `go.mod:12` | `gos7` 使用伪版本 — 无标签发布 |
| L10 | `driver/opcua/client.go:14` | 层级违反 — 驱动器导入 `engine/statistic` |
| L11 | `config/config.go` | 无环境变量替换 — 容器化部署不便 |
| L12 | `AI_HANDOVER.md` §7 | 测试命令与 CONTRIBUTING 不一致 |

### Info 级别（积极发现）

| ID | 说明 |
|----|------|
| I1 | `core` 包零内部依赖 — 纯端口包，六边形架构正确 |
| I2 | 工厂注册模式清晰 — `init()` + 全局注册表 + RWMutex 线程安全 |
| I3 | Modbus 模板方法设计 — `modbusBase` + 注入钩子，消除 6 变体重复 |
| I4 | 结构化日志 `log/slog` 全程使用 — 无 `fmt.Println` |
| I5 | `context.Context` 在所有生命周期和 I/O 方法中传播 |
| I6 | 编译时接口断言一致使用 — `var _ core.Engine = (*CoreCEngine)(nil)` |
| I7 | 离线缓冲使用原子 temp+rename 写入 — 崩溃安全 |
| I8 | 引擎 Start 有正确的部分初始化回滚 |
| I9 | 调度器在持续错误时自动降级轮询频率 |
| I10 | 传输 fallback（故障切换）正确实现 |
| I11 | Config 错误消息优秀 — 包含字段路径、有效值列表、%w 包装 |
| I12 | E2E 测试使用真实协议 mock（Modbus 服务器、httptest）— 非桩 |
| I13 | CI/CD 多平台发布 — 7 目标含 Raspberry Pi ARMv7 |
| I14 | HMAC 常量时间认证 — `hmac.Equal` on SHA-256 |
| I15 | REST 写端点 body 限制 1 MiB — `http.MaxBytesReader` |
| I16 | HTTP 服务器设置 slowloris 防护超时 |
| I17 | 全代码库无 `InsecureSkipVerify: true` |
| I18 | expr-lang 表达式沙箱化 — 禁用 `type` 内建，配置控制非用户注入 |
| I19 | CONTRIBUTING.md 和 AI_HANDOVER.md 高质量 — 硬约束、架构契约、扩展指南 |

---

## 7. 积极发现

### 架构亮点
- **六边形架构正确实现** — `core` 零内部依赖，依赖方向正确：`cmd → hub → engine → core ← driver/transport`
- **插件注册模式** — `init()` 自动工厂注册，`driver/all` 和 `transport/all` 空导入触发
- **模板方法模式** — Modbus `modbusBase` + 注入 `initFunc`/`connectFunc`，6 变体各 ~100 行
- **装饰器模式** — `ruleWrapper` 嵌入 `core.Rule` 添加原子计数器 + 禁用标志
- **策略模式** — 规则引擎基于匹配前缀分派 `simpleRule`/`ruleSetRule`/`subRuleRef`

### 工程亮点
- 全部 315 测试通过，16 个基准测试
- `go vet` 清洁，编译零警告
- `log/slog` 结构化日志全程使用
- `sync/atomic` 无锁计数器一致使用
- 泛型 `Observable[T]` 现代 Go 特性应用
- 所有导出标识符有文档注释

### 运维亮点
- 多平台 CI/CD — 7 目标交叉编译含 ARMv7
- SHA256 校验和发布
- 常规提交自动生成 Release Changelog
- VitePress 文档站自动部署

---

## 8. 优先修复路线图

### 🔴 P0 — 立即修复（安全 + 数据正确性）

| 优先级 | ID | 修复 | 工作量 |
|--------|----|------|--------|
| 1 | C1 | MQTT 命令通道添加消息级认证（HMAC 签名） | 中 |
| 2 | C2 | OPC UA Write 结果索引修复（`validIndices` 映射） | 小 |
| 3 | C3 | WebSocket 超时修复 — `ReadTimeout: 0, WriteTimeout: 0` | 一行 |
| 4 | C4 | DataBus 过滤器实现或参数移除 | 小 |
| 5 | C5 | 修复示例配置 + 加强测试 | 小 |
| 6 | H1 | Webhook 添加认证 + body 大小限制 | 小 |
| 7 | H6 | 空密钥 fail-closed | 小 |
| 8 | H12 | Modbus 非布尔类型在线圈区拒绝或正确处理 | 中 |

### 🟠 P1 — 下一版本修复（可靠性 + 安全加固）

| 优先级 | ID | 修复 | 工作量 |
|--------|----|------|--------|
| 9 | H2 | 速率限制器不信任 `X-Real-IP` + bucket 驱逐 | 中 |
| 10 | H3 | WebSocket 连接数限制 | 小 |
| 11 | H5 | `tags-file` 路径沙箱化 | 小 |
| 12 | H7 | 标签文件监视器 goroutine 泄漏修复 | 中 |
| 13 | H9 | `CloseServer` 改用 `Shutdown(ctx)` | 小 |
| 14 | H10 | MQTT Start() 连接失败返回错误 | 小 |
| 15 | H11 | HTTP Push 实现 RetryCount 重试 | 中 |
| 16 | H13 | Modbus 批量读取实现（连续寄存器合并） | 大 |
| 17 | H21 | CI 添加 govulncheck + Dependabot + gosec | 中 |
| 18 | H23 | 列表端点添加分页 | 中 |

### 🟡 P2 — 计划改进（健壮性 + 配置验证）

- 配置验证：引擎持续时间、`on-bad-quality`、`node.role`、`rule-providers/groups`
- 驱动改进：上下文传播、字节序可配置、S7 位写入原子性
- 传输改进：MQTT QoS 验证、发布流水线、Webhook TLS
- 测试改进：OPC UA/S7 集成测试、MQTT broker 测试、`-race` CI
- 代码质量：gofmt 修复 22 文件、Hub 全局变量重构、引擎分解

---

## 9. 各维度详细审计索引

| # | 审计维度 | Agent | 关键发现数 | 状态 |
|---|----------|-------|-----------|------|
| 1 | 架构 & 代码质量 | workflow agent | 35 findings (2H, 8M, 12L, 4Info) | ✅ |
| 2 | 安全 | workflow agent | 17 findings (1C, 5H, 7M, 4L) + 16 positive | ✅ |
| 3 | 并发 & goroutine 安全 | workflow agent | H1 (goroutine leak), H2 (WaitGroup race) + 更多 | ✅ |
| 4 | 错误处理 & 弹性 | subagent | 32 findings (1C, 3H, 10M, 12L, 6Info) | ✅ |
| 5 | 驱动实现 | subagent | 30 findings (1C, 3H, 13M, 8L, 5Info) | ✅ |
| 6 | 引擎 & 规则引擎 | subagent | 18 findings (1C, 3H, 6M, 5L, 3Info) | ✅ |
| 7 | 传输 & Hub/API | subagent | 47 findings (1C, 9H, 25M, 12L) | ✅ |
| 8 | 配置 & 测试 & CI/CD | subagent | 35 findings (1C, 6H, 8M, 11L, 5Info) | ✅ |

### 构建验证

```
go build ./...          ✅ 通过
go vet ./...            ✅ 通过
go test ./...           ✅ 全部通过 (315 tests)
gofmt -l .              ⚠️ 22 文件有格式问题
```

### 测试覆盖率

| 包 | 覆盖率 | 评价 |
|----|--------|------|
| core | 100.0% | ✅ 优秀 |
| common/util | 99.1% | ✅ 优秀 |
| hub | 96.4% | ✅ 优秀 |
| common/observable | 95.1% | ✅ 优秀 |
| engine/statistic | 92.6% | ✅ 优秀 |
| hub/route | 90.6% | ✅ 优秀 |
| config | 87.9% | ✅ 良好 |
| hub/executor | 86.5% | ✅ 良好 |
| transport/httppush | 86.3% | ✅ 良好 |
| rule | 85.5% | ✅ 良好 |
| log | 83.0% | ✅ 良好 |
| driver/modbus | 74.6% | ⚠️ 可改进 |
| transport/parser | 67.6% | ⚠️ 可改进 |
| engine | 65.6% | ⚠️ 可改进 |
| transport/mqtt | 45.4% | 🔴 不足 |
| driver/s7 | 44.2% | 🔴 不足 |
| driver/opcua | 40.9% | 🔴 不足 |
| cmd/corec | 22.7% | 🔴 不足 |

---

## 附录：CI/CD 工作流

| 工作流 | 触发 | 内容 |
|--------|------|------|
| `ci.yml` | push/PR to main | test -race -short, vet, golangci-lint, 5-target build matrix |
| `release.yml` | tag `v*` | test → 7-target build → package → SHA256 → GitHub Release |
| `deploy-docs.yml` | push to main (docs/**) | VitePress build → GitHub Pages |

**缺失：** govulncheck, CodeQL, gosec, Dependabot, SBOM, 代码覆盖率, 文档构建检查 (PR)

---

*报告生成时间: 2025-09-14*  
*审计方法: 8 个并行 AI Agent 静态代码审查 + 构建验证 + 测试执行 + 覆盖率分析*
