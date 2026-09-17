# 贡献指南与契约 — CoreC

> 本文件是 **CoreC** 的贡献硬约束。人和 AI 都按它来；CI 也只认它。
> 原则：**少而锋利**——只列必须项，每条都能被一条命令或一次 review 验证。

---

## 0. 契约速查（TL;DR）

提交前，本地必须全绿：

```bash
go build ./...                                  # 全包编译
CGO_ENABLED=0 go build -o /dev/null ./cmd/corec # 交叉编译入口（CI build-check 跑 linux/darwin/windows × amd64/arm64，排除 windows/arm64）
go vet ./...                                    # 静态检查
go test -v -race -short -timeout 120s ./...        # 测试（-race 需 cgo；无 C 编译器时去掉 -race）
golangci-lint run --timeout 5m                  # v2，配置见 .golangci.yml（默认 errcheck/govet/ineffassign/staticcheck/unused + bodyclose/gocritic/gocyclo/misspell/nilerr/nilnil/revive；格式化 gofmt/goimports）
```

**任何一项不过，不要提交。** CI 会原样跑这些，CI 红了 PR 不合。

---

## 1. 编译与静态检查（硬门）

- **Go 版本锁 `1.27.1`**（见 `go.mod`）。不要用更高版本才有的语法，也不要降级。
- `go build ./...` 必须通过；`cmd/corec` 必须能在 `CGO_ENABLED=0` 下交叉编译（工业场景常部署到无 libc 的 ARM 边缘设备）。
- `go vet ./...` 必须通过。
- `golangci-lint run` 必须零 issue。配置在 `.golangci.yml`，**不要为了过 lint 而改配置去放宽规则**——改代码。
- `coverage.out` 是生成物，已纳入 `.gitignore` 范围的语义；不要手工提交覆盖率文件。

---

## 2. 架构契约（六边形 + 工厂注册）

这是本项目最核心的约束。**`core/` 是纯契约层，零外部依赖**——任何 `import` 了第三方库的代码都不准进 `core/`。

### 2.1 新增南向驱动（Driver）

1. 在 `driver/<协议>/` 下实现 `core.Driver` 接口（`Init/Start/Stop/Restart/Read/Write/Subscribe/Name/Type/Status/Capabilities`）。
2. 在该包 `register.go` 的 `init()` 里调用 `core.RegisterDriver("<type>", NewXxxDriver)`。
3. 在 `driver/all/all.go` 加一行空白导入 `_ "github.com/CoreC-Dev/CoreC/driver/<协议>"`。
4. 不支持订阅的驱动，`Subscribe` 返回 `core.ErrSubscribeNotSupported`，不要返回裸 `nil` channel。

### 2.2 新增北向传输（Transport）

1. 在 `transport/<名>/` 下实现 `core.Transport` 接口（`Init/Start/Stop/Publish/PublishBatch/OnCommand/OnData/Name/Type/Status`）。
2. `init()` 里 `core.RegisterTransport("<type>", NewXxxTransport)`。
3. `transport/all/all.go` 加空白导入。
4. 无反向指令通道时 `OnCommand()` 返回 `nil`，不要返回会阻塞的 channel。

### 2.3 不破坏公开接口

`core.Driver` / `core.Transport` 及 `core` 下所有类型是**稳定契约**。改动接口（加方法、改签名）属于破坏性变更，必须先开 issue 讨论，并在 PR 描述里显式标注 `BREAKING CHANGE`。

---

## 3. 配置契约

- 驱动/传输的可调参数走 `Settings map[string]any`，**不要**给 `core.DriverConfig` / `core.TransportConfig` 加**协议特有**的类型字段——那是契约层，不该认识具体协议。跨协议的通用字段（如 `TransportConfig` 的 `BatchSize`/`FlushInterval`/`RetryCount`）可以保留为类型字段，但新增要谨慎。
- 业务语义校验（唯一性、目标存在性等）集中在 `config/config.go` 的 `Load` 里做，配置解析失败必须返回明确 error，不要 panic。
- 新增可配置项，**同步**在 `config.example.yaml` 加注释示例，否则用户无从知晓。

---

## 4. 测试契约

- **新功能必须带测试。** 本项目 60+ 测试文件，测试是默认预期，不是可选。
- 测试必须 `-short` 友好：依赖真实 PLC / 外部 broker / 串口的测试，用 `if testing.Short() { t.Skip(...) }` 跳过，或用 `//go:build` tag 隔离。`go test -short ./...` 必须在无外设环境下全绿。
- **不准引入 flaky 测试**：不要用 `time.Sleep` 等固定时序去对齐并发（等 goroutine 启动、等消息到达），用 channel/`WaitGroup`/轮询+超时 模式。轮询辅助函数见 `engine` 包的 `waitFor` 和 `e2e` 包的同名 helper。
- 不准在测试里 `os.Exit`（除 `main`）。用 `t.Fatal` / `t.Skip`。

---

## 5. 提交信息契约（Conventional Commits）

**这是硬约束**：Release 的 Changelog 由 `.github/scripts/changelog.sh` 自动解析 Conventional Commits 生成，格式不对就进不了发版说明。

```
<type>(<scope>): <subject>
```

- **type** 必须是其一：`feat` `fix` `docs` `style` `refactor` `test` `perf` `chore` `ci` `build`（`revert` 亦合规，但 `.github/scripts/changelog.sh` 不自动归类）
- **scope**（可选但推荐）用包/模块名：`modbus` `s7` `opcua` `mqtt` `http` `engine` `rule` `route` `config` `demo` `ci` `docs` `api` …
- **subject** 用祈使句、句末不加句号。
- 破坏性变更：在 footer 加 `BREAKING CHANGE: <说明>`。

正例：`feat(modbus): support Modbus RTU over UDP`、`fix(engine): reconnect after context cancel`、`docs: add CONTRIBUTING`
反例：`update`、`Fix bug`、`wip`、`Update index.md`（历史里这类已造成 changelog 缺失，不要再犯）。

---

## 6. 依赖与许可证契约

- 本项目 **MIT**，只接受 MIT/BSD/ISC/Apache-2.0/EPL-2.0 等宽松许可证依赖。**不准引入 GPL/AGPL/LGPL**。新依赖在 PR 描述里写明许可证，并同步更新 `README.md` 的「第三方依赖许可证」表。
- `go mod tidy` 后提交，`go.sum` 必须与 `go.mod` 一致。不要提交 `vendor/`（项目策略是模块代理）。
- 能用标准库和现有依赖解决的，不引新依赖。

---

## 7. 文档同步契约

改了用户可见行为，**同一 PR 内**同步：

| 改了什么 | 要同步更新 |
|:---|:---|
| 配置项 / 驱动 / 传输 | `README.md`、`config.example.yaml`、`docs/guide/` |
| 架构 / 接口 / 扩展机制 | `AI_HANDOVER.md` |
| 公开 API 端点 | `README.md` 的 API 表 |

文档不同步的 PR 不合。`docs/**`、`README.md`、`AI_HANDOVER.md` 的改动不触发 CI（见 `.github/workflows/ci.yml` 的 `paths-ignore`），所以文档 PR 不会因测试矩阵卡住——但这不是跳过同步的借口。

---

## 8. 工业运行时契约（针对驱动/传输实现）

本项目跑在 PLC 旁边，**生产可用性高于功能新颖**。新驱动/传输必须满足：

- **优雅退出**：`Stop()` 必须可重入、不阻塞、释放全部连接与 goroutine。`context` 取消要能及时传播，不准泄漏协程。
- **断线重连**：连接丢失要后台指数退避重连，重连成功后自动恢复采集。参考 `driver/modbus/modbus_base.go` 的 `handleConnectionLost`。
- **不阻塞数据总线**：单次 `Read`/`Publish` 超时要有上限，单点故障不准拖垮整个引擎。高频错误要限频（参考 README「高频错误抑制」）。
- **日志用 `log/slog`**，不要用 `fmt.Println` / `log.Println` 做运行时日志（`fmt` 仅限 `cmd/corec` 的 Banner）。

---

## 9. PR 流程

1. 从 `main` 切分支，PR 回 `main`。
2. PR 描述写清 **改了什么 / 为什么 / 如何验证**。涉及新驱动或新传输，贴一段 `config.example.yaml` 片段证明可配置。
3. 等 CI 全绿。CI 跑：测试 + vet + golangci-lint + 三平台交叉编译 build-check。
4. 一个 PR 一个关注点。既改协议又改 UI 又改文档的大杂烩 PR 会被要求拆分。

---

## 10. 给 AI 协作者的额外约束

- **先读 `AI_HANDOVER.md` 再动手**——它有完整代码拓扑和设计动机，能避免你重复造轮子或破坏不变量。
- 改代码前先跑第 0 节的速查命令确认基线绿；改完再跑一遍。**不准提交让基线变红的改动。**
- 不要为了"看起来更通用"去抽象 `core/` 接口——六边形边界是刻意设计的，扩接口要先讨论。
- 生成的新文件要带 `// Package <名> <一句话说明>` 包注释，和现有包风格一致。
- 不要自行 `git push` 或创建 Release；完成代码 + 测试 + 文档后交由人类维护者审核合并。
