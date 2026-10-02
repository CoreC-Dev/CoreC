# CI 配置决定

> 本文件记录 CoreC 项目的框架判定结果、现有 CI 现状与门禁决策。
> 依据：《Harness 工程化规则》§3（项目框架识别与条件式 GitHub CI 配置）。
> 本项目为 **Go 后端**，非 Tauri 桌面应用，故 §3.3 三步交互询问不适用（见 §3.4）。

## 1. 框架判定

- **判定结果**：其他类型（纯 Go 后端 / IIoT 数据采集核心）
- **判定证据**（按 §3.2 机械判定，均不命中 Tauri）：
  - `src-tauri/tauri.conf.json` —— 不存在
  - `src-tauri/Cargo.toml` —— 不存在
  - 根 `package.json` 含 `@tauri-apps/cli` / `@tauri-apps/api` —— 不存在（仓库根无 `package.json`）
  - 反证：`go.mod` 存在（`module github.com/CoreC-Dev/CoreC`，`go 1.27.1`），确认为 Go 项目
- **判定日期**：2026-10-02

## 2. 现有 CI 现状（存量项目必填）

| 文件 | 触发条件 | 覆盖平台 | 是否自动发布 | 备注 |
|---|---|---|---|---|
| `.github/workflows/ci.yml` | push 到 `main` + PR 到 `main`（`paths-ignore: docs/**, README.md, deploy-docs.yml`） | ubuntu-latest（test/vet/lint/gates/doc-gate/coverage/version-check）；build-check matrix: linux/darwin/windows × amd64/arm64（排除 windows/arm64） | 否 | `go test -race -short`、`go vet`、`golangci-lint v2.13.2`、多平台 `go build`；**阶段 3 新增**：archtest(T7)/tastetest(T1/T6)/doc-gate(T9)/coverage(T10 ≥75%)/version-check(D3) |
| `.github/workflows/release.yml` | push tag `v*` | ubuntu-latest；build matrix: linux{amd64,arm64,armv7}、darwin{amd64,arm64}、windows{amd64,arm64} | **是**（直接正式 Release，非 draft） | test→build（ldflags 注入版本号到 `main.version` 与 `hub/route.Version`）→打包 tar.gz/zip+checksums→`softprops/action-gh-release@v3`（`generate_release_notes: true`） |
| `.github/workflows/deploy-docs.yml` | push 到 `main` 且 `paths: docs/**`；`workflow_dispatch` | ubuntu-latest | 否（部署 GitHub Pages） | VitePress 构建（`docs/package.json`，Node 24）→ GitHub Pages |

**已有发布链路现状**：
- 发布方式：tag `v*` 触发 `release.yml`，构建多架构 Go 二进制并创建**正式** GitHub Release（含 `checksums.txt` 与由 `.github/scripts/changelog.sh` 从 Conventional Commits 生成的 BODY.md）。
- 版本声明点（§6.1 相关）：共 3 处——
  1. Git tag（`v*`，版本来源）
  2. `cmd/corec/main.go:24` — `version = "dev"`（ldflags `-X main.version=<tag>` 注入）
  3. `hub/route/server.go:119` — `Version = "dev"`（ldflags `-X github.com/CoreC-Dev/CoreC/hub/route.Version=<tag>` 注入）
- **签名密钥**：未配置（Release 资产无签名/校验密钥；仅有 sha256 checksums）。
- **版本一致性 guard**：`release.yml` 从 tag 提取版本并注入两处 ldflags 变量。**阶段 3 已新增 `version-check` guard job**（仅 tag push 触发）：校验 tag 符合 semver + release.yml 含 ldflags 注入 `main.version` 与 `hub/route.Version`（D3 决策落地）。

## 3. 交互确认记录（仅 Tauri 填写）

> **不适用。** §3.2 判定为「其他类型」，按 §3.4 跳过 §3.3 三步交互询问（是否生成 GitHub CI / 目标平台多选 / Release 发布方式）。本项目不生成 Tauri 式多平台桌面打包与 Release 流水线。

## 4. 落地说明

- **框架判定 = 其他类型**：本项不适用 Tauri 多平台桌面打包与 Release 流水线（§3.4）。
- **基础门禁照建**（§5 阶段 3 落地）：lint（golangci-lint）/ test（`go test -race`）/ build（多平台 `go build`）三件套——其中 lint/test/build 已由现有 `ci.yml` 覆盖；阶段 3 已在此基础上补齐：结构测试、品味不变量测试、文档门禁、覆盖率门禁、版本一致性 guard、提交前 hook。
- **现有 CI 处置**：按 §3.4「若项目已有 CI 配置：只做审计……不得因为『这段不适用』就顺手删掉现有配置」。现有 `ci.yml` / `release.yml` / `deploy-docs.yml` **均保留**，阶段 3 仅增量新增 4 个 job（gates/doc-gate/coverage/version-check），**未删任何现有 job**。
  - `release.yml` 是**合法的 Go 多架构二进制发布**，**不是** §3.4 所警告的「给非桌面项目配多平台打包流水线」反模式（该反模式特指 Tauri 桌面打包）。不删除、不降级。
  - `deploy-docs.yml` 是 VitePress 文档站部署，与门禁无关，保留。
- **阶段 3 新增 CI job**：
  | Job | 触发 | 内容 | 对应门禁 |
  |---|---|---|---|
  | `gates` | push/PR | 架构结构测试 + 品味不变量测试 | T7/T1/T6 |
  | `doc-gate` | push/PR | `scripts/check-docs.sh` 文档校验 | T9 |
  | `coverage` | push/PR | `go test -cover` + 阈值检查 ≥75% | T10 |
  | `version-check` | tag push | semver 校验 + ldflags 注入校验 | D3 |
- **本地复现**：`make gates` 一条命令复现全部门禁（vet+lint+arch+taste+docs+test）；`make install-hooks` 安装 git pre-commit hook。

## 5. 变更历史

| 日期 | 变更 | 原因 |
|---|---|---|
| 2026-10-02 | 新建 `docs/CI.md`，记录框架判定（其他类型）与现有 CI 现状 | Harness 改造阶段 1（§3.2/§3.4） |
| 2026-10-03 | 阶段 3：CI 增量新增 gates/doc-gate/coverage/version-check 四 job；修复 paths-ignore 过期条目 | Harness 改造阶段 3（§5 门禁审计） |
