---
title: MCP 服务
description: corec-mcp — 把运行中的 CoreC 实例暴露为 AI 智能体可调用的工具集，默认只读。
layout: doc
outline: [2, 3]
---

# MCP 服务

`corec-mcp` 是**独立可执行文件**，把运行中的 CoreC 实例按 [Model Context Protocol](https://modelcontextprotocol.io) 暴露给 AI 智能体（Claude Desktop、各类 IDE 编程助手、自建 agent）。智能体据此能"看懂现场"：列设备、查状态、读点位值、看告警；显式开启后可下发写入。

`corec-mcp` **无状态、只做转发**：它不连接任何设备、不缓存任何点位、不持有调度状态， 每个工具调用就是一次带 API secret 的 HTTP 请求，语义与 REST 完全一致，因此**引擎侧已经生效的所有约束在这里一句不落地同样生效**：

- 点位写保护（`protect: read-only` 被拒，错误原样返回）；
- 指令限速（`write-min-interval` 撞窗返回 `429`，**不排队**）；
- 审计留痕（每次写入照常进 `GET /write/history`）；
- 缺 secret 拒绝启动（fail-closed）。

## 安装与启动

```bash
go build -o corec-mcp ./cmd/corec-mcp
```

```bash
# 只读（默认）：智能体可以看，不能动
./corec-mcp -url http://127.0.0.1:8080 -secret "$COREC_API_SECRET"

# 受控写入：额外暴露写类工具
./corec-mcp -url http://127.0.0.1:8080 -secret "$COREC_API_SECRET" -allow-write
```

| 参数 | 说明 |
|:---|:---|
| `-url` | CoreC 控制面地址，**必填**（如 `http://127.0.0.1:8080`） |
| `-secret` | API secret；省略时读环境变量 `COREC_API_SECRET`；两者都空则拒绝启动 |
| `-allow-write` | 暴露写类工具（默认只读） |
| `-timeout` | 单次控制面请求超时，默认 `30s` |

传输为 **stdio**（按行分隔、每行一个 JSON-RPC 2.0 消息），是本地 MCP 客户端的标准方式。

## 工具面

读类工具（始终可见）：

| 工具 | 映射 | 说明 |
|:---|:---|:---|
| `list_drivers` | `GET /drivers` | 驱动清单：类型、状态、点位数 |
| `driver_status` | `GET /drivers/{name}` | 单个驱动状态（连接态、读取/错误/重连计数、点位数） |
| `list_tags` | `GET /tags?driver=&tag=` | 点位最新值，可按驱动、标签名子串过滤 |
| `read_tag` | `GET /tags?driver=<d>&tag=<t>`（REST 侧 `tag` 为子串过滤，工具内再精确匹配） | 精确读一个点的最新缓存值 |
| `list_alerts` | `GET /alerts` | 活动告警及确认状态 |
| `list_paused_drivers` | `GET /drivers/paused` | 当前被暂停采集的驱动 |
| `get_configs` | `GET /configs/raw` | 运行中配置的 YAML 原文；**只读**，应用变更不是工具 |
| `validate_configs` | `POST /configs/validate` | dry-run 校验候选配置，返回 valid/invalid 与引擎原话；不改任何状态 |

写类工具（**仅** `-allow-write` 时可见；不开时连工具 definition 都不下发，智能体无法知道它们存在）：

| 工具 | 映射 | 说明 |
|:---|:---|:---|
| `write_tag` | `POST /write` | 写点位；写保护与限速在引擎侧强制，智能体绕不过 |
| `pause_driver` | `POST /drivers/{name}/pause` | 暂停采集（保持连接） |
| `resume_driver` | `POST /drivers/{name}/resume` | 恢复采集 |
| `ack_alert` | `POST /alerts/ack` | 确认告警（不立即清除，静默满窗口才清除） |

工具返回的文本是**原样的引擎响应**；非 2xx 时前缀为 `HTTP <code> <text>: <reason>`——这样做是有意：

- 智能体拿到 `HTTP 429` 就知道该退避、不要立刻重试；
- 拿到 `is read-only (protect: read-only)` 就知道是配置约束，不是自己写错了语法；
- 拿到 `HTTP 401` 就知道 secret 有问题，而不是继续猜端点。

### 文本之外的第二个通道：`isError`

文本里带原因，**并不等于**这次调用"成功返回了一句否定的话"。JSON-RPC 结果上还有一个布尔
`isError`，两者是不同的信息：

| `isError` | 含义 | 智能体的正确解读 |
|:---|:---|:---|
| `false` | 调用**发生了**，文本就是结果 | 这是答案，据此决定下一步 |
| `true` | 调用**没发生**（或无法判定其效果） | 不是答案；换条件、退避，或交给人工 |

这个区分不是形式主义。多数 MCP 客户端会把 `isError` 渲染成错误状态，并倾向重试，
而不去读文本。于是同样一段 `HTTP 400 ... valid:false`：

- 标 `true` → 模型以为**工具坏了**，重试或向人报告"CoreC 连不上"；
- 标 `false` → 模型以为**配置错了**，去改配置。

哪一个错，代价完全不同。

`validate_configs` 的 `400` 降级为 `isError=false`（校验结论"不合法"以文本返回）；其余非 2xx 一律 `true`。

写类工具：任何非 2xx（含 read-only `400`、限速 `429`、鉴权 `401`、`5xx`）都 `isError=true`，文本原样携带状态码与拒绝理由供智能体区分；仅 `validate_configs` 的 `400` 降级为 `false`。

## 客户端配置示例

以 Claude Desktop 的 `claude_desktop_config.json` 为例（MCP 标准 `mcpServers` 形状，其他客户端大同小异）：

```json
{
  "mcpServers": {
    "corec": {
      "command": "/opt/corec/bin/corec-mcp",
      "args": ["-url", "http://127.0.0.1:8080"],
      "env": { "COREC_API_SECRET": "${COREC_SECRET}" }
    }
  }
}
```

`secret` 走环境变量、不写进命令行参数：命令行会出现在进程列表（`ps`）与 shell 历史里。

## 安全边界（务必读）

1. **默认只读**。不开 `-allow-write`，写入工具既不可见、也不可调。
2. **凭证即权限**。`corec-mcp` 用你给的 API secret 说话，权限与 secret 完全相同。给智能体的 secret 若没有写保护兜底，就能改任何可写点——所以**至少用一个分配了 `protect: read-only` 点位的驱动做试点**，再谈放开写。
3. **不引入额外权限模型**。没有 MCP 侧的 ACL、审批流或白名单；需要这些，做法是给智能体一个**只授予部分驱动的 CoreC 实例**（独立部署、独立 secret），而不是在适配器里加一层谁都说不清的过滤。
4. **审计在引擎侧**。智能体写了什么点、什么值、什么时候，都在 `GET /write/history`（`source: api`），与人工 API 调用在同一条流里，不区分入口。
5. **别把它暴露到公网**。stdio 是本地传输；若要用 HTTP 传输，请把 `corec-mcp` 与 CoreC 一起放在同一个可信网络边界内，并对接入反向代理做认证。

## 与 Dashboard AI 助手的关系

官方 Dashboard（独立仓库 `CoreC-Dashboard`）在实例页内也有一个 AI 对话面板。它和 `corec-mcp` 是**同一个 REST 控制面的两个客户端**，不是两套权限：

| | `corec-mcp` | Dashboard AI 助手 |
|---|---|---|
| 运行位置 | 本地进程，stdio 给桌面 MCP 客户端 | 浏览器，`fetch` 直连你配置的 OpenAI 兼容端点 |
| 谁持 secret | 启动参数 / 环境变量 | 浏览器 localStorage（UI 明示披露） |
| 工具面 | 读类 + 写类（写类需 `-allow-write`），数量以两侧工具清单为准 | 与 MCP 同名工具；无 `read_tag`（由 `list_tags(exact)` 覆盖） |
| 写操作 | `-allow-write` 默认关 | "允许写操作"默认关、刷新即回落，且每次写需人工确认 |

**对后端的影响：没有。** 助手不新增端点、不改鉴权、不改写保护；它产生的写入与人工 API、`corec-mcp` 的写入在引擎侧不可区分。

由此，以下约束属于 Dashboard 那一侧的实现，**不在 CoreC 里做**：

- 助手只能对**已连通**的实例工作——它复用 `/corec/:id/*` 下已有的连接上下文；`/`（实例选择页）没有活跃连接，放不下能真正执行的工具调用。
- provider 密钥存浏览器 localStorage，是 Dashboard 侧的凭据披露问题。
- 两份工具定义（Go 与 TypeScript）靠测试断言工具名一致来防漂移；参数与错误语义的同步仍是人工纪律。

CoreC 侧对任何 AI 客户端的立场不变：**它只是 `source: api` 的又一个写入者**，没有特权，也没有豁免。

## 验证

```bash
# 只读握手：返回的工具清单中没有任何写类工具
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | ./corec-mcp -url http://127.0.0.1:8080
```

开 `-allow-write` 后同一命令应答中出现 `write_tag` / `pause_driver` / `resume_driver` / `ack_alert`。只读模式下调用写入工具会得到 `unknown tool`（工具根本不在清单里，而不是"调用被拒"）。

## 它不做什么

- **不缓存**：每次工具调用都是一次实时 HTTP 往返，无本地缓存；
- **不做软写入/自动重试**：非 2xx 响应原样返回，不自动重试；
- **不改变引擎语义**：不转义质量、不连点、不补心跳；
- 运维脚本、Dashboard、监控采集走 REST/WS；MCP 是同一控制面的另一个客户端。
