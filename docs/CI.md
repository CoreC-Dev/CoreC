# CI 与发布

> 本文件描述 CoreC 现行的 CI 工作流、门禁与发布链路（现状文档，非决策历史）。
> 门禁定义见 `docs/gates.md`；本地一键复现：`make gates`。

## CI 工作流

| 工作流 | 触发 | 内容 |
|---|---|---|
| `ci.yml` | push/PR 到 `main`（`paths-ignore`: `docs/**`、`README.md`、`.github/workflows/deploy-docs.yml`） | `go test -race -short`（含 archtest/tastetest）、`go vet`、`make lint`（golangci-lint 钉版于 Makefile，CI 自装）、文档门禁（`check-docs.sh`）、actionlint 校验、按包覆盖率下限检查（`check-coverage.sh`，与本地 `make cover-gate` 同一脚本）、build-check 矩阵 |
| `docs-gate.yml` | push/PR **仅文档路径**（`docs/**`、`README.md`、`AGENTS.md`、`ARCHITECTURE.md`、`CONTRIBUTING.md`） | 文档门禁——补齐 `paths-ignore` 造成的纯文档提交零校验缺口 |
| `release.yml` | push tag `v*` | test + vet + lint + 文档门禁 → version-check（真实行为断言：以 tag ldflags 构建后 `-version` 输出必须等于 tag）→ 多架构构建 → tar.gz/zip + sha256 checksums → GitHub Release（`generate_release_notes: true`） |
| `deploy-docs.yml` | push 到 `main` 且 `docs/**` 变更 | VitePress 构建 → GitHub Pages |

四个工作流均带 concurrency 组与每 job `timeout-minutes`；`ci.yml` 与 `docs-gate.yml` 对同引用取消旧跑（`cancel-in-progress: true`），`release.yml` 与 `deploy-docs.yml` 排队不取消（发布/部署不中途打断）。

## 构建矩阵

build-check 与 release 矩阵对齐：linux{amd64,arm64,armv7}、darwin{amd64,arm64}、windows{amd64,arm64}。

## 版本声明点（3 处必须一致）

1. Git tag（`v*`，版本来源）
2. `cmd/corec/main.go` — `version = "dev"`（ldflags `-X main.version=<tag>` 注入）
3. `hub/route/server.go` — `Version = "dev"`（ldflags `-X github.com/CoreC-Dev/CoreC/hub/route.Version=<tag>` 注入）

`release.yml` 的 version-check job 从 tag 注入两处并断言 `-version` 输出等于 tag。Release 资产含 sha256 checksums，**无签名密钥**（已接受的残余风险，见 `SECURITY.md`）。

## 约定

- Go 版本经 `go-version-file: go.mod` 解析（当前 `go 1.27.3`），全部 workflow 不在 YAML 里硬编码版本；golangci-lint 钉版于 Makefile（`GOLANGCI_VERSION ?= v2.15.0`），`make lint` 自装同版本。
- 文档提交（`docs/**`、`README.md`）不触发主 CI（test 矩阵），但触发文档门禁——文档仍被校验，只是不跑测试矩阵。
- 本地与 CI 用同一套门禁脚本（`make gates` / `check-coverage.sh` / `check-docs.sh`），防止本地与 CI 漂移。
- `make install-hooks` 安装 git pre-commit hook，提交前本地先跑门禁（vet + lint + arch + taste + docs 的快速子集；race 测试与覆盖率留给 CI/push）。
