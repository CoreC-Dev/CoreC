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
| 架构 | amd64 / arm64 | 树莓派等 ARM 设备同样支持 |
| 内存 | ≥ 64 MB | 核心本身极轻量，余量供数据缓冲 |
| 网络 | 能访问设备协议端口与北向端点 | Modbus 502、S7 102、OPC UA 4840 等 |

### 构建要求（仅源码构建需要）

| 项目 | 要求 |
|:---|:---|
| Go | **≥ 1.27.1** |
| Git | 任意版本 |
| Make（可选） | 用于便捷构建命令 |

::: warning Go 版本要求
CoreC 需要 Go ≥ 1.27.1 工具链。请通过 `go version` 确认。
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

预期输出：

```text
   ____                  ____
  / ___|___  _ __ ___   / ___|
 | |   / _ \| '__/ _ \ | |
 | |__| (_) | | |  __/ | |___
  \____\___/|_|  \___|  \____| {version}

  IIoT Data Collection and Distribution Core

Usage of ./corec:
  -c string
        path to configuration file (shorthand)
  -config string
        path to configuration file
  -log-level string
        override log level (debug, info, warn, error)
```

> 版本号由构建时 ldflags 注入（`-X main.version=x.y.z`），未注入时默认显示 `dev`。

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
使用 `-ldflags "-s -w"` 去除调试信息与符号表，可将 ~16 MB 的二进制压缩到约 11 MB：

```bash
go build -ldflags "-s -w" -o corec ./cmd/corec
```
:::

### 构建标签

CoreC 通过空白导入 `driver/all` 与 `transport/all` 注册所有内置驱动与传输：

```go
import (
    _ "github.com/CoreC-Dev/CoreC/driver/all"    // 注册 modbus-tcp/rtu/rtuovertcp/udp/rtuoverudp/tls, s7, opcua
    _ "github.com/CoreC-Dev/CoreC/transport/all" // 注册 mqtt, http
)
```

如果只需要部分协议以减小体积，可以在自己的 `main.go` 中按需导入对应子包，而非 `all` 聚合包。

## 方式二：使用预编译二进制

仓库根目录或 Release 页面提供预编译二进制 `corec`，可直接下载使用：

```bash
# 赋予执行权限
chmod +x corec

# 验证
./corec -h
```

预编译二进制已包含全部内置驱动（`modbus-tcp`、`modbus-rtu`、`modbus-rtuovertcp`、`modbus-udp`、`modbus-rtuoverudp`、`modbus-tls`、`s7`、`opcua`）与传输（`mqtt`、`http`），开箱即用。

## 方式三：Go install

如果已配置 Go 工具链，也可以直接安装：

```bash
go install github.com/CoreC-Dev/CoreC/cmd/corec@latest
```

安装后的二进制位于 `$GOPATH/bin`（或 `$GOBIN`）目录下。

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
└── logs/              # 日志（若外部采集）
```

::: warning 离线缓冲已废弃
早期版本的 `data/buffer/` 离线缓冲目录与 `global.buffer` 配置段已**废弃且被忽略**——离线缓冲不再支持。配置中残留 `buffer` 段会触发一条弃用警告，删除该段即可消除。无需再创建 `data/buffer/` 目录。
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

## 命令行参数

| 参数 | 简写 | 默认值 | 说明 |
|:---|:---|:---|:---|
| `-config` | `-c` | `config.yaml` | 配置文件路径 |
| `-log-level` | — | 配置文件中的值 | 覆盖日志级别：`debug` / `info` / `warn` / `error` / `silent` |

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

# 启动（示例配置中的设备地址需要改为你的实际设备）
./corec -c config.yaml
```

如果看到如下日志，说明核心与所有插件已成功加载：

```text
INFO registered drivers types=[modbus-tcp modbus-rtu modbus-rtuovertcp modbus-udp modbus-rtuoverudp modbus-tls s7 opcua]
INFO registered transports types=[mqtt http]
INFO CoreC engine starting drivers=3 transports=2 rules=4
INFO CoreC engine started successfully
INFO CoreC is running config=config.yaml
```

> `types=` 列表包含全部 8 个内置驱动类型；具体顺序取决于 `init()` 注册顺序，可能因构建而异。

::: tip 下一步
安装验证通过后，前往 [快速上手](./quickstart.md) 编写你的第一份最小配置。
:::
