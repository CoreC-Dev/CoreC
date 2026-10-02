# 门禁清单（Gates）

> 每项门禁：检查名 · 触发时机 · 失败含义 · 修复指引 · 本地复现命令。
> 所有门禁可通过 `make gates` 一条命令本地复现。

## 门禁总览

| # | 检查名 | 不变量 | 触发时机 | 本地命令 |
|---|--------|--------|---------|---------|
| G1 | `go vet` | 静态分析 | CI / pre-commit | `make vet` |
| G2 | `golangci-lint` | errcheck/govet/staticcheck/gocyclo/revive/misspell/... | CI / pre-commit | `make lint` |
| G3 | 架构结构测试 | T7: 依赖方向与允许的边 | CI / pre-commit | `make arch` |
| G4 | 文件大小上限 | T1: ≤600 行 | CI / pre-commit | `make taste` |
| G5 | 结构化日志 | T6: 禁止 fmt.Println/Printf | CI / pre-commit | `make taste` |
| G6 | 文档校验 | T9: 死链/必填节/孤儿/包注释 | CI / pre-commit | `make docs` |
| G7 | 单元测试 | 全量 -short -race | CI / pre-commit | `make test` |
| G8 | 覆盖率门禁 | T10: ≥75% 整体 | CI (push/PR) | `make cover` |
| G9 | 版本一致性 | D3: tag == ldflags 注入点 | CI (tag push) | 见下方 |
| G10 | 交叉编译 | linux/darwin/windows × amd64/arm64 | CI (push/PR) | `make build` |

## 各门禁详情

### G1: go vet
- **失败含义**：代码有静态分析可疑问题（不可达代码、错误格式串、锁复制等）
- **修复指引**：`go vet ./...` 输出会指出文件:行号和问题类型，按提示修复
- **豁免**：无（vet 发现的问题必须修复）

### G2: golangci-lint
- **失败含义**：未检查错误返回值 / 高圈复杂度 / 命名问题 / 拼写错误 / nil 处理等
- **修复指引**：lint 输出含 linter 名 + 文件:行号 + 问题描述。按 linter 类型修复：
  - `errcheck`：检查被忽略的错误返回值
  - `gocyclo`：函数圈复杂度 >20，拆分为更小函数
  - `revive`：风格规则（exported 命名等）
  - `staticcheck`：静态分析（SA/SU/ST 类问题）
- **豁免**：`//nolint:linter名 // 原因` — 须附原因，见下方豁免机制

### G3: 架构结构测试（T7）
- **失败含义**：包的 import 违反 ARCHITECTURE.md 中的依赖方向允许边表
- **修复指引**：错误信息格式：`ARCH VIOLATION: X imports Y (not in allowed dependencies)` + `Fix: ...`
- **测试位置**：`archtest/arch_test.go`
- **豁免**：无（架构违规必须修复或更新 ARCHITECTURE.md 并经人工评审）

### G4: 文件大小上限（T1）
- **失败含义**：.go 文件超过 600 行且未在 `exemptedFiles` 中登记
- **修复指引**：按职责拆分为更小文件。若暂不能拆，在 `tastetest/taste_test.go` 的 `exemptedFiles` 中添加条目并附 tracker ID
- **测试位置**：`tastetest/taste_test.go::TestFileSizeLimit`
- **当前豁免**：10 个文件（CPLX-001..012），Phase 4 批次 3–11 将拆分

### G5: 结构化日志（T6）
- **失败含义**：生产代码中使用 `fmt.Println` 或 `fmt.Printf`（非测试/非 demo/非 cmd/非 log 包）
- **修复指引**：改用 `slog`（通过 `log` 包）。例：`log.Info("msg", "key", val)` 替代 `fmt.Printf("msg %v\n", val)`
- **测试位置**：`tastetest/taste_test.go::TestStructuredLogging`
- **豁免**：无

### G6: 文档校验（T9）
- **失败含义**：死链 / 缺必填节 / 孤儿文档 / 缺包注释
- **修复指引**：`scripts/check-docs.sh` 输出具体问题和文件。按提示修复链接/补充章节/添加引用
- **豁免**：孤儿文档为警告（不阻断）；其余为错误（阻断）

### G7: 单元测试
- **失败含义**：测试失败或 race 检测发现数据竞争
- **修复指引**：`go test -race -short ./... -run <失败测试名>` 定位并修复
- **豁免**：`t.Skip()` 须附原因

### G8: 覆盖率门禁（T10）
- **失败含义**：整体覆盖率低于 75%
- **修复指引**：`go test -cover -coverprofile=cover.out ./... && go tool cover -func=cover.out` 找出低覆盖包，补充测试
- **当前基线**：80.1%（阈值 75%，有 5.1% 余量）
- **豁免**：无

### G9: 版本一致性（D3）
- **失败含义**：git tag 不符合 semver，或 release.yml 未通过 ldflags 注入 `main.version` 和 `hub/route.Version`
- **触发**：仅在 tag push 时运行
- **修复指引**：确保 tag 格式为 `vX.Y.Z`，release.yml 含 `-ldflags "-X main.version=... -X github.com/.../hub/route.Version=..."`
- **豁免**：无

### G10: 交叉编译
- **失败含义**：某平台编译失败
- **修复指引**：`GOOS=<平台> GOARCH=<架构> go build ./cmd/corec` 定位平台特定代码
- **豁免**：无

## 豁免机制

### golangci-lint 豁免
```go
//nolint:gocyclo // CPLX-014: batcher.go will be split in Phase 4 batch 3
func (b *Batcher) runTask(...) {
```

**规则**：
- 每个 `//nolint` 必须附 `// 原因` 注释
- 原因应含 tracker ID（如 `CPLX-014`）或具体理由
- Phase 4 完成后，已拆分文件的 nolint 应被移除

### 文件大小豁免
在 `tastetest/taste_test.go` 的 `exemptedFiles` map 中登记：
```go
"engine/engine.go": "CPLX-003: 875 lines, Phase 4 batch 3 will split into lifecycle/stats/config",
```
**规则**：每条豁免含 tracker ID + 修复计划。Phase 4 拆分后移除条目。

### 测试跳过豁免
```go
func TestSomething(t *testing.T) {
    t.Skip("TEST-003: requires OPC UA simulation server, Phase 5 will add mock")
```
**规则**：每个 `t.Skip` 须附 tracker ID 或具体原因。

## 合并理念

- **短生命周期 PR**：PR 应小到可在一次审查中完成
- **最小阻塞式门禁**：门禁只拦截明确错误，不追求完美
- **偶发失败**：flaky 测试用重跑解决，不无限阻塞；连续 3 次失败则开 issue
- **门禁先行**：Phase 3 门禁必须在 Phase 4 重构前落地（R3：先护栏后动手）
