---
title: 安装
description: CoreC 的前置条件、源码构建、二进制获取与配置文件位置说明
---

# 安装

CoreC 是一个纯 Go 单二进制程序，部署非常简单。本章介绍前置条件、三种获取方式以及配置文件约定。

## 前置条件

### 运行时要求

| 项目 | 要求 | 说明 |
|:---|:---|:---|
| 操作系统 | Linux / macOS / Windows | 推荐 Linux 部署于边缘网关 |
| 架构 | amd64 / arm64（Linux 另发布 armv7） | 树莓派等 ARM 设备同样支持，见 Release 构建矩阵 |
| 内存 | 无硬性下限 | 占用随点位规模、DataBus 容量与缓冲配置而定 |
| 网络 | 能访问设备协议端口与北向端点 | Modbus 502、S7 102、OPC UA 4840 等 |

### 构建要求（仅源码构建需要）

| 项目 | 要求 |
|:---|:---|
| Go | **≥ 1.27.3**（`go.mod` 声明的工具链版本） |
| Git | 任意版本 |
| Make（可选） | 用于便捷构建命令 |

::: warning Go 版本要求
CoreC 需要 Go ≥ 1.27.3 工具链。请通过 `go version` 确认。
:::

## 方式一：从源码构建（推荐）

从源码构建可以获得最新特性，且便于二次开发。

```bash
# 1. 克隆仓库
git clone https://github.com/CoreC-Dev/CoreC.git
cd CoreC

# 2. 构建二进制（输出到当前目录的 corec）
go build -o corec ./cmd/corec

# 3. 验证
./corec -h
```

`-h` 输出命令行参数说明（不打印启动横幅）：

```text
Usage of ./corec:
  -c string
    	path to configuration file (shorthand) (default "config.yaml")
  -config string
    	path to configuration file (default "config.yaml")
  -log-level string
    	override log level (debug, info, warn, error)
  -version
    	print version and exit
```

> 启动横幅在正常启动时打印（`-h` 只输出上面的参数说明）。版本号由构建时 ldflags 注入（`-X main.version=x.y.z`），横幅末尾显示注入的版本，未注入时默认显示 `dev`。

### 交叉编译

CoreC 是纯 Go 项目，所有依赖均支持交叉编译。为边缘设备构建 ARM64 二进制：

```bash
# Linux ARM64（如树莓派 4、工业网关）
GOOS=linux GOARCH=arm64 go build -o corec-arm64 ./cmd/corec

# Linux AMD64
GOOS=linux GOARCH=amd64 go build -o corec-amd64 ./cmd/corec

# Windows
GOOS=windows GOARCH=amd64 go build -o corec.exe ./cmd/corec
```

::: tip 减小二进制体积
使用 `-ldflags "-s -w"` 去除调试信息与符号表，实测可将约 22 MB 的二进制压缩到约 15 MB（14.7 MB）。Release 流水线发布的就是这种剥离形态（见 `.github/workflows/release.yml`）：

```bash
go build -ldflags "-s -w" -o corec ./cmd/corec
```
:::

### 构建标签

CoreC 通过空白导入 `driver/all` 与 `transport/all` 注册所有内置驱动与传输：

```go
import (
    _ "github.com/CoreC-Dev/CoreC/driver/all"    // 注册 modbus-tcp/rtu/rtuovertcp/udp/rtuoverudp/tls, s7, opcua
    _ "github.com/CoreC-Dev/CoreC/transport/all" // 注册 mqtt, http, notify
)
```

如果只需要部分协议以减小体积，可以在自己的 `main.go` 中按需导入对应子包，而非 `all` 聚合包。

## 方式二：使用预编译二进制

GitHub Release 页面提供预编译二进制（推送 `v*` tag 时由 `.github/workflows/release.yml` 自动构建，覆盖 Linux amd64/arm64/armv7、macOS amd64/arm64、Windows amd64/arm64），打包为 `corec-vX.Y.Z-<os>-<arch>.tar.gz`（Windows 为 `.zip`），内含二进制、`config.example.yaml` 与 README：

```bash
# 解压发布包后，赋予执行权限
chmod +x corec

# 验证
./corec -h
```

预编译二进制已包含全部内置驱动（`modbus-tcp`、`modbus-rtu`、`modbus-rtuovertcp`、`modbus-udp`、`modbus-rtuoverudp`、`modbus-tls`、`s7`、`opcua`）与传输（`mqtt`、`http`、`notify`——多通道告警通知：Slack/Teams/Email/通用 webhook，见 [传输配置](../config/transports.md#notify-传输-告警通知)），开箱即用。

## 方式三：Go install

如果已配置 Go 工具链，且模块能通过 `GOPROXY` 公开拉取，也可以直接安装：

```bash
go install github.com/CoreC-Dev/CoreC/cmd/corec@latest
```

安装后的二进制位于 `$GOPATH/bin`（或 `$GOBIN`）目录下。`cmd/corec` 通过空白导入 `driver/all` 与 `transport/all` 注册全部内置插件，`go install` 构建的二进制同样包含全部驱动与传输。

## 配置文件位置

CoreC 启动时通过命令行参数指定配置文件路径，支持两种等价写法：

```bash
# 完整参数
./corec -config /path/to/config.yaml

# 简写
./corec -c /path/to/config.yaml
```

### 默认路径

若未指定任何参数，CoreC 会在**当前工作目录**下寻找 `config.yaml`：

```bash
./corec   # 等价于 ./corec -config config.yaml
```

::: warning 配置文件必须存在
CoreC 不会自动生成配置文件。如果配置文件不存在或路径错误，启动会直接报错退出：

```text
ERROR failed to load config path=config.yaml error="failed to read config file config.yaml: open config.yaml: no such file or directory"
```
请确保运行目录下有配置文件，或通过 `-c` 显式指定路径。
:::

### 推荐的部署目录结构

生产环境推荐如下目录布局：

```text
/opt/corec/
├── corec              # 二进制
├── config.yaml        # 配置文件
├── buffer/            # 离线缓冲目录（启用 global.buffer 时使用）
└── logs/              # 日志（若外部采集）
```

::: tip 离线缓冲
启用 `global.buffer` 后，传输失败时数据会写入 `buffer.path` 指定目录，待传输恢复后自动回放。生产环境建议将缓冲目录放在独立磁盘分区以避免空间不足。
:::

对应的 systemd 服务单元示例：

```ini
# /etc/systemd/system/corec.service
[Unit]
Description=CoreC Connect Collect Control
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/corec
ExecStart=/opt/corec/corec -c /opt/corec/config.yaml
Restart=on-failure
RestartSec=5
KillSignal=SIGTERM

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now corec
```

## 生产部署的 CORS 姿势

CoreC 管理 API 默认对浏览器开放 CORS：未配置 `global.api.allowed-origins` 时输出 `Access-Control-Allow-Origin: *`。这是刻意设计，服务于 Dashboard 的**直连部署形态**（静态 SPA——如 GitHub Pages——直接访问远程 CoreC），否则浏览器无法跨域直连后端。CORS 不影响认证：所有业务端点仍需 `Authorization: Bearer <secret>`，无 token 的跨域页面只能拿到 401。

两种部署形态的推荐姿势：

| 形态 | 说明 | CORS 建议 |
|:---|:---|:---|
| **同源反向代理**（生产首选） | Dashboard 部署形态的 `server.mjs` 将 `/corec-api/*` 反代到 CoreC，浏览器只访问同源（见 [ADR-011](../architecture/decisions.md)） | 无需 CORS，也无需配置 |
| **直连** | 浏览器直接访问 CoreC API 地址 | **公网部署务必**配置 `global.api.allowed-origins` 为 Dashboard 域名 allowlist |

::: warning 公网直连务必收紧
默认 `*` 的残余风险是：一旦 API secret 泄露，**任意网页**都能跨域读写该 API——带 `Authorization` 头的请求在 CORS 语义里不算 credentialed request，`*` 下响应体可被读取，比"只能写不能读"更宽。生产环境公网直连部署，请显式配置 [`global.api.allowed-origins`](../config/global.md) 收紧来源；同时配合 TLS 与高熵 secret。
:::

完整决策依据与备选方案评估见[架构决策 ADR-011](../architecture/decisions.md)。

## 命令行参数

| 参数 | 简写 | 默认值 | 说明 |
|:---|:---|:---|:---|
| `-config` | `-c` | `config.yaml` | 配置文件路径 |
| `-log-level` | — | 配置文件中的值 | 覆盖日志级别：`debug` / `info` / `warn` / `warning` / `error` / `silent` |
| `-version` | — | — | 打印注入的版本号并退出（版本一致性校验用） |

`-log-level` 参数会覆盖配置文件中 `global.log-level` 的设置，便于临时调试：

```bash
# 临时开启调试日志
./corec -c config.yaml -log-level debug
```

## 验证安装

完成安装后，使用仓库自带的示例配置快速验证：

```bash
# 复制示例配置
cp config.example.yaml config.yaml

# 启动（示例配置是仅含 API 的最小引导配置，需通过环境变量提供 secret）
COREC_API_SECRET=change-me-please ./corec -c config.yaml
```

`config.example.yaml` 当前是一份**最小引导配置**：不配置任何驱动、传输与规则，CoreC 以空闲模式启动，操作者随后通过 Dashboard 或 `PUT /configs` 补全配置；文件内还附有注释掉的全量示例（1 个 Modbus 驱动、2 个传输、2 条规则），取消注释即可启用。

空闲启动时看到如下日志即说明核心与所有插件已成功加载：

```text
WARN no data source configured — running in idle mode; add a driver, an inbound transport (mqtt data-topic / http webhook-addr), or enable auto-discovery with node.subscribe via the Dashboard
WARN no transport configured — running in idle mode; add a northbound transport via the Dashboard
INFO registered drivers types=[modbus-tcp modbus-rtu modbus-rtuovertcp modbus-udp modbus-rtuoverudp modbus-tls s7 opcua]
INFO registered transports types=[mqtt http notify]
INFO CoreC engine starting drivers=0 transports=0 rules=0
INFO CoreC engine started successfully
INFO CoreC is running config=config.yaml
```

两条 WARN 是预期行为：空管道是一个合法的启动状态（详见 [快速上手](./quickstart.md)）。

> `types=` 列表包含全部 8 个内置驱动类型；列表来自注册表的 map 遍历，每次启动的顺序不固定。

::: tip 下一步
安装验证通过后，前往 [快速上手](./quickstart.md) 编写你的第一份最小配置。
:::
