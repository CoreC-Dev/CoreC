# 门禁清单（Gates）

> 每项门禁：检查名 · 触发时机 · 失败含义 · 修复指引 · 本地复现命令。
> G1–G8 可通过 `make gates` 一条命令本地复现；G9（版本一致性）与 G10（交叉编译矩阵）在 CI 中触发（见 `docs/CI.md`）。

## 门禁总览

| # | 检查名 | 不变量 | 触发时机 | 本地命令 |
|---|--------|--------|---------|---------|
| G1 | `go vet` | 静态分析 | CI / pre-commit | `make vet` |
| G2 | `golangci-lint` | bodyclose/errcheck/gocritic/gocyclo/govet/ineffassign/misspell/nilerr/nilnil/revive/staticcheck/unused | CI / pre-commit | `make lint` |
| G3 | 架构结构测试 | T7: 依赖方向与允许的边 | CI / pre-commit | `make arch` |
| G4 | 文件大小上限 | T1: ≤600 行 | CI / pre-commit | `make taste` |
| G5 | 结构化日志 | T6: 禁止 fmt.Println/Printf | CI / pre-commit | `make taste` |
| G6 | 文档校验 | T9: 死链/必填节/孤儿/包注释 | CI / pre-commit | `make docs` |
| G7 | 单元测试 | 全量 -short -race | CI | `make test` |
| G8 | 覆盖率门禁 | T10: 整体 ≥75% + 按包下限 | CI (push/PR) | `make cover-gate` |
| G9 | 版本一致性 | D3: tag == ldflags 注入点 | CI (tag push) | 见下方 |
| G10 | 交叉编译 | linux/darwin/windows × amd64/arm64(+armv7) | CI (push/PR) | 见下方 |

## 各门禁详情

### G1: go vet
- **失败含义**：代码有静态分析可疑问题（不可达代码、错误格式串、锁复制等）
- **修复指引**：`go vet ./...` 输出会指出文件:行号和问题类型，按提示修复
- **豁免**：无（vet 发现的问题必须修复）

### G2: golangci-lint
- **失败含义**：未检查错误返回值 / 高圈复杂度 / 命名问题 / 拼写错误 / nil 处理等（启用集见 `.golangci.yml`：bodyclose/errcheck/gocritic/gocyclo/govet/ineffassign/misspell/nilerr/nilnil/revive/staticcheck/unused）
- **修复指引**：lint 输出含 linter 名 + 文件:行号 + 问题描述。按 linter 类型修复：
  - `errcheck`：检查被忽略的错误返回值
  - `gocyclo`：函数圈复杂度 >20，拆分为更小函数（cmd/corec 因 wiring 特性被豁免）
  - `revive`：风格规则（exported 命名已关闭，见 `.golangci.yml` exclusions）
  - `staticcheck`：静态分析（SA/SU/ST 类问题）
- **钉版**：`GOLANGCI_VERSION ?= v2.15.0` 定义于 Makefile（唯一版本源），`make lint` 缺失时自装该版本，与 CI 完全一致
- **豁免**：`//nolint:linter名 // 原因` — 须附原因，见下方豁免机制

### G3: 架构结构测试（T7）
- **失败含义**：包的 import 违反 `archtest/arch_test.go` 中 `allowedDeps` 的允许边表（与 ARCHITECTURE.md 的分层规则对应），或命中跨域禁止边（driver 之间互不依赖、transport/mqtt ↔ transport/httppush 互不依赖）。未在表中登记的新包默认拒绝一切内部依赖（default-deny），新包必须显式归类
- **修复指引**：错误信息格式：`ARCH VIOLATION: X imports Y (not in allowed dependencies)` / `(cross-domain forbidden)` + `Fix: ...`
- **测试位置**：`archtest/arch_test.go`
- **豁免**：无（架构违规必须修复，或更新 archtest 允许边表并经人工评审）

### G4: 文件大小上限（T1）
- **失败含义**：.go 文件超过 600 行且未在 `exemptedFiles` 中登记（豁免条目还须在 `exemptedFileCaps` 钉上限：超过钉死的行数同样失败）
- **修复指引**：按职责拆分为更小文件。若暂不能拆，在 `tastetest/taste_test.go` 的 `exemptedFiles` 中添加条目并附 tracker ID
- **测试位置**：`tastetest/taste_test.go::TestFileSizeLimit`
- **当前豁免**：**0 个**（`exemptedFiles` 与 `exemptedFileCaps` 均为空表，保持为空除非确实无法拆分）

### G5: 结构化日志（T6）
- **失败含义**：生产代码中使用 `fmt.Println` 或 `fmt.Printf`（跳过：测试文件、demo/、cmd/corec/、log/、archtest/、tastetest/、vendor）
- **修复指引**：改用结构化日志：`slog.Info("msg", "key", val)`（引擎/驱动等生产代码的实际写法），或 log 包的 printf 风格 `log.Infoln("msg %v", val)`；勿用 `fmt.Printf("msg %v\n", val)`
- **测试位置**：`tastetest/taste_test.go::TestStructuredLogging`
- **豁免**：无

### G6: 文档校验（T9）
- **失败含义**：死链 / 缺必填节 / 孤儿文档 / 缺包注释
- **修复指引**：`scripts/check-docs.sh` 输出具体问题和文件。按提示修复链接/补充章节/添加引用
- **豁免**：孤儿文档为警告（不阻断）；其余为错误（阻断）

### G7: 单元测试
- **失败含义**：测试失败或 race 检测发现数据竞争
- **修复指引**：`go test -race -short ./... -run <失败测试名>` 定位并修复
- **本地命令**：`make test`（= `go test -race -short -timeout 120s ./...`，race 经 Makefile `GOFLAGS` 注入；`make test-all` 为不带 `-short` 的全量长测）
- **豁免**：`t.Skip()` 须附原因

### G8: 覆盖率门禁（T10）
- **失败含义**：整体覆盖率低于 75%，或任一登记包低于其按包下限（`scripts/check-coverage.sh`：core 95 / hub 90 / hub/route 85 / config 80 / rule 80 / engine 75 …）
- **修复指引**：`go test -cover -coverprofile=cover.out ./... && go tool cover -func=cover.out` 找出低覆盖包，补充测试
- **本地命令**：`make cover-gate`（判定，与 CI 同一脚本）；`make cover` 仅生成报告不做判定
- **当前基线**：81.9% 整体（阈值 75%；复现：`make cover-gate`，包级下限全部通过）
- **豁免**：无

### G9: 版本一致性（D3）
- **失败含义**：git tag 不符合 semver，或以 tag ldflags 构建出的二进制 `-version` 输出与 tag 不符
- **触发**：tag push 时的 `release.yml` version-check job——真实行为断言：以 tag ldflags 注入两处版本声明点构建后，`./corec -version` 必须等于 tag（tag 前缀 v 由脚本剥去）
- **修复指引**：确保 tag 格式为 `vX.Y.Z`；检查 `cmd/corec/main.go` 的 `version` 变量与 `hub/route/server.go` 的 `Version` 注入点未被重命名
- **豁免**：无

### G10: 交叉编译
- **失败含义**：某平台编译失败（CI build-check 矩阵：linux/amd64、linux/arm64、linux/armv7、darwin/amd64、darwin/arm64、windows/amd64、windows/arm64，与 release 矩阵一致）
- **本地复现**：`GOOS=<平台> GOARCH=<架构> CGO_ENABLED=0 go build ./cmd/corec` 逐平台验证（`make build` 仅构建本机平台，不覆盖矩阵）
- **豁免**：无

## 豁免机制

### golangci-lint 豁免
```go
//nolint:gocyclo // CPLX-XXX: <reason>, split planned in <tracker>
func (b *Batcher) runTask(...) {
```

**规则**：
- 每个 `//nolint` 必须附 `// 原因` 注释
- 原因应含 tracker ID（如 `CPLX-014`）或具体理由
- 触发豁免的问题修复后，对应的 nolint 应被移除

### 文件大小豁免
在 `tastetest/taste_test.go` 的 `exemptedFiles` map 中登记（同时在 `exemptedFileCaps` 钉上限）：
```go
"path/to/file.go": "CPLX-XXX: N lines over the cap, <reason>, split planned in <tracker>",
```
**规则**：每条豁免含 tracker ID + 修复计划；`exemptedFileCaps` 中的行数上限一旦被超过即失败（豁免不允许无声增长）。拆分落地后删除条目。

### 测试跳过豁免
```go
func TestSomething(t *testing.T) {
    t.Skip("TEST-XXX: requires OPC UA simulation server, mock planned in <tracker>")
```
**规则**：每个 `t.Skip` 须附 tracker ID 或具体原因。

## 合并理念

- **短生命周期 PR**：PR 应小到可在一次审查中完成
- **最小阻塞式门禁**：门禁只拦截明确错误，不追求完美
- **偶发失败**：flaky 测试用重跑解决，不无限阻塞；连续 3 次失败则开 issue
- **先护栏后动手**：改动受保护不足的模块时，先补测试/门禁再重构（R3）
