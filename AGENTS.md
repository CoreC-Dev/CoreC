# AGENTS.md

## 这是什么项目

CoreC — 工业物联网（IIoT）数据采集与控制核心，Go 实现。南向连接 PLC/设备（Modbus/OPC UA/S7），北向推送数据（MQTT/HTTP）与告警通知（Slack/Teams/Email/Webhook），中间经规则引擎变换与控制指令下发。

## 如何开始

- 架构总览：见 `ARCHITECTURE.md`
- CI 与发布：见 `docs/CI.md`

## 硬性约束（不可违反）

- **分层依赖**（由 `archtest/` 结构测试机械强制，允许边以 `archtest/arch_test.go` 为准）：`core` 与 `common` 零内部依赖；`engine` ← {core, common, rule, log}；`hub` ← {core, common, config, log}；`driver/*` ← {core, common, driverbase}；`transport/*` ← {core, common, transport/parser}；`rule` / `config` ← {core, common}；`log` ← {core, common/observable}。引擎经 `core` 接口解耦驱动/传输、hub 经 `route.SetEngine` 拿到引擎实例，编译期装配只发生在 `cmd/corec`。只能单向，禁止反向或跨域（`driver/*` 互引、`transport/mqtt` ↔ `transport/httppush` 互引均禁止）。
- **插件注册**：驱动/传输通过 `init()` 工厂注册，由 `driver/all`、`transport/all` 空导入触发。新增插件须在 `all` 包注册。
- **边界解析**：外部输入（配置 YAML、MQTT 命令、HTTP webhook）必须经校验/解析后才能进入核心，禁止信任原始输入。
- **配置密钥**：`${ENV_VAR}` 替换为 tree-based（parse→expand→re-marshal），非字节级；`config/config.go` 保证 API 永不回显明文。
- 违反以上约束会被结构测试（`archtest/`）机械拦截。

## 常用命令

- 构建：`go build -o corec ./cmd/corec`
- 运行：`./corec -c config.example.yaml`
- 测试：`go test -race -short ./...`
- 全量测试：`go test -race ./...`
- lint：`golangci-lint run`
- 覆盖率：`go test -cover -coverprofile=cover.out ./... && go tool cover -func=cover.out`
- 交叉编译：`go build -ldflags "-X main.version=$(git describe --tags)" -o corec ./cmd/corec`

## 目录地图

| 路径 | 职责 |
|------|------|
| `cmd/corec/` | 入口，wiring + 信号处理 |
| `core/` | 核心接口与类型（Driver/Transport/Rule 缝隙） |
| `config/` | YAML 加载、env 替换、secrets、tags-file 热重载 |
| `engine/` | 核心引擎（scheduler/batcher/discovery/databus/offlinebuffer） |
| `driver/{modbus,opcua,s7}` | 南向设备驱动 |
| `transport/{mqtt,httppush,notify,parser}` | 北向传输（数据推送 + 告警通知通道） |
| `rule/` | 规则引擎（expr 表达式求值） |
| `hub/{route,executor}` | 控制面 HTTP/WS API 与执行器 |
| `common/` | 共享工具（metrics/observable/trace/util） |
| `log/` | 结构化日志封装 |
| `e2e/` | 端到端集成测试 |
| `demo/` | 示例配置与场景 |

## 规范索引

- 架构设计：`docs/architecture/`
- 配置参考：`docs/config/`
- API 参考：`docs/api/`

## 客户端生态

CoreC 的控制面是 REST/WS，下面三个客户端**共用同一套端点与同一套引擎侧约束**（写保护、限速、审计），本仓不为任何一个开特例：

| 客户端 | 位置 | 与 CoreC 的关系 |
|---|---|---|
| Dashboard（含 AI 对话助手） | 独立仓库 `CoreC-Dashboard` | 经 REST/WS 读写；助手是又一个 `source: api` 写入者 |
| `corec-mcp` | 本仓 `cmd/corec-mcp/` | 把控制面适配成 MCP 工具，默认只读，见 `docs/guide/mcp.md` |
| 运维脚本 / 监控采集 | 外部 | 同 REST/WS |

改动控制面契约（端点、字段、错误码、写保护语义）时，**这三个客户端都在影响面内**。

## 工作方式

- 以代码为唯一事实来源；仓库内不记录决策、理由、过程与临时状态。
- 提交粒度单一职责；版本声明点（`main.version`、`hub/route.Version`、git tag）必须一致。

## 注释与文档

- 注释与文档只写当前为真、能在代码中验证、且不随实现漂移的内容。
- 保留已固化的协议与标准、安全与并发硬约束、编译期分层约束、跨仓机械约束，以及门禁要求的每包一处 `// Package` 包注释。
- 不写日期、版本、阶段、批次、审计编号、提交号、进度、状态、待办标记、计划与路线图。
- 不写决策记录、选型、权衡、理由与「为什么这么做」。
- 不写会因代码变动而反复更新的引用：文件与行号、复制自代码的阈值/计数/版本号。
- 不写与代码不符的描述、复述代码的句子、空泛标题。
- 不把删掉的内容改写成新的解释；不为通过门禁补说明。
- 文档文件增删时同步维护入站链接、索引与门禁检查项。
