# AGENTS.md

> 本文件是**内容目录**，不是百科全书。需要细节时，按下方指引跳转。

## 这是什么项目

CoreC — 工业物联网（IIoT）数据采集与控制核心，Go 实现。南向连接 PLC/设备（Modbus/OPC UA/S7），北向推送数据（MQTT/HTTP），中间经规则引擎变换与控制指令下发。

## 如何开始

- 架构总览：见 `ARCHITECTURE.md`
- 设计理念与操作原则：见 `docs/design-docs/core-beliefs.md`
- 当前执行计划：见 `docs/exec-plans/active/`
- CI 与发布决策：见 `docs/CI.md`
- 已知技术债：见 `docs/exec-plans/tech-debt-tracker.md`
- 质量评分：见 `docs/QUALITY_SCORE.md`

## 硬性约束（不可违反）

- **分层依赖**：`core`（接口）← {config, driver, transport, engine, rule, hub, common}；`engine` ← {driver, transport, rule, config, common}；`driver`/`transport` ← {core, common}；`hub` ← {engine, config, core, common}。只能单向，禁止反向或跨域。
- **插件注册**：驱动/传输通过 `init()` 工厂注册，由 `driver/all`、`transport/all` 空导入触发。新增插件须在 `all` 包注册。
- **边界解析**：外部输入（配置 YAML、MQTT 命令、HTTP webhook）必须经校验/解析后才能进入核心，禁止信任原始输入。
- **配置密钥**：`${ENV_VAR}` 替换为 tree-based（parse→expand→re-marshal），非字节级；`secrets.go` 保证 API 永不回显明文。
- 违反以上约束会被 depguard / 结构测试 / 自定义 linter 拦截（阶段 3 建立）。

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
| `transport/{mqtt,httppush,parser}` | 北向传输 |
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
- 质量评分：`docs/QUALITY_SCORE.md`
- 设计决策：`docs/design-docs/`

## 工作方式

- 变更前先读相关 `docs/`，变更后同步更新文档
- 提交粒度单一职责；版本声明点（`main.version`、`hub/route.Version`、git tag）必须一致
- 计划写在 `docs/exec-plans/active/`，完成后移入 `completed/`
- 重构不改变业务行为；行为变更必须显式登记并单独提交（R2）
- 任何交互式决策确认后必须写入仓库文档（B11）
