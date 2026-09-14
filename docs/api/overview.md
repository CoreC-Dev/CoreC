---
title: API 总览
description: CoreC RESTful API 与 WebSocket 接口总览，包含基础地址、认证方式、CORS、错误格式及全部端点索引。
---

# API 总览

CoreC 核心内置一套 RESTful API 与 WebSocket 实时流接口，用于驱动管理、数据读写、配置热更新、规则控制及运行时监控。API 服务通过配置项 `global.api.listen` 启动；该配置项未设置（空字符串）时不启动任何 API 服务，**没有默认监听地址**。

## 基础信息

| 项目 | 说明 |
|:---|:---|
| 协议 | HTTP/1.1（RESTful）+ WebSocket；配置 `global.api.tls-cert` 与 `global.api.tls-key` 后启用 HTTPS（WebSocket 对应 `wss://`） |
| 基础地址 | `http://<host>:<port>`，由 `global.api.listen` 决定 |
| 内容类型 | `application/json`（所有请求体与响应体） |
| 字符编码 | UTF-8 |
| 请求体上限 | 1 MiB（`maxRequestBody`，超出将返回 `400 Bad Request`） |
| 鉴权方式 | Bearer Token（Header 或 Query 参数） |

## 认证

当配置项 `global.api.secret` 非空时，除公开端点外的所有端点均需鉴权。支持两种传递 Token 的方式：

::: code-group

```bash [Authorization Header（推荐）]
curl http://localhost:9090/drivers \
  -H "Authorization: Bearer corec-secret-token"
```

```bash [Query 参数]
curl "http://localhost:9090/drivers?token=corec-secret-token"
```

:::

Token 比较使用恒定时间比较（`crypto/hmac.Equal`），可防止时序攻击。

::: warning 未配置 Secret
若 `global.api.secret` 为空，核心将跳过鉴权中间件，**所有端点均可公开访问**。日志会输出警告：

```
API server starting WITHOUT authentication — secret is empty.
This is insecure for production. All endpoints will be publicly accessible.
```

生产环境务必配置非空 Secret。

> **注**：该无鉴权状态在正常配置加载下**不可达**。配置校验规定：当 `global.api.listen` 非空时，`global.api.secret` 必须非空且不少于 8 个字符，否则配置加载直接失败（参见 `config.validate`）。因此通过配置文件正常启动时不会进入此分支；该警告仅在绕过校验、直接构造 `Config` 并调用 `ReCreateServer` 时才可能出现。
:::

### 公开端点

以下端点无需认证，始终可访问：

| 方法 | 路径 | 说明 |
|:---|:---|:---|
| `GET` | `/` | 健康检查，返回 `{"name": "corec", "version": "...", "status": "ok", "time": "...", "uptime": "..."}` |
| `GET` | `/version` | 版本信息，返回 `{"version": "dev"}` |

## CORS

API 内置 CORS 中间件，用于支持浏览器端仪表盘跨域访问。行为取决于 `global.api.allowed-origins` 配置：

- **未配置 `allowed-origins`（默认）**：返回 `Access-Control-Allow-Origin: *`，允许任意来源跨域访问，便于本地开发与快速接入仪表盘。
- **配置了 `allowed-origins`**：仅对列表中的来源反射其 `Origin` 值，其余来源不返回 CORS 头（限制模式）。

| 响应头 | 值 |
|:---|:---|
| `Access-Control-Allow-Origin` | 默认 `*`；或白名单中匹配的 Origin 原值 |
| `Access-Control-Allow-Methods` | `GET, POST, PUT, PATCH, OPTIONS` |
| `Access-Control-Allow-Headers` | `Content-Type, Authorization` |
| `Access-Control-Max-Age` | `86400`（24 小时） |

`OPTIONS` 预检请求直接返回 `204 No Content`。

::: tip 生产环境建议
默认配置下 CORS 策略为 `*`（宽松）。生产环境建议显式配置 `global.api.allowed-origins` 为指定仪表盘域名，或通过反向代理（如 Nginx）进一步收紧。
:::

## 错误格式

所有错误响应统一使用以下 JSON 结构，HTTP 状态码与错误语义对应：

```json
{
  "error": "unauthorized"
}
```

### 状态码约定

| 状态码 | 含义 | 触发场景 |
|:---|:---|:---|
| `200 OK` | 请求成功 | 所有 `GET` 端点、`POST /write` 成功 |
| `204 No Content` | 操作成功，无响应体 | `PUT /configs`、`PATCH /configs`、`PATCH /rules/disable` |
| `400 Bad Request` | 请求格式错误 | JSON 解析失败、规则索引越界等 |
| `401 Unauthorized` | 认证失败 | Token 缺失或不匹配 |
| `429 Too Many Requests` | 请求频率超限 | 触发速率限制中间件（`global.api.rate-limit-per-sec`，超限按客户端 IP 拒绝） |
| `404 Not Found` | 资源不存在 | 驱动/传输名称未找到 |
| `500 Internal Server Error` | 内部错误 | 写入失败、配置重载失败等 |

## 端点索引

### RESTful 端点

| 方法 | 路径 | 鉴权 | 说明 |
|:---|:---|:---|:---|
| `GET` | `/` | 否 | 健康检查 |
| `GET` | `/version` | 否 | 版本信息 |
| `GET` | `/configs` | 是 | 获取当前配置概要（脱敏） |
| `PUT` | `/configs` | 是 | 全量重载配置 |
| `PATCH` | `/configs` | 是 | 增量更新配置 |
| `GET` | `/drivers` | 是 | 列出所有驱动状态 |
| `GET` | `/drivers/{name}` | 是 | 获取指定驱动状态 |
| `GET` | `/drivers/{name}/tags` | 是 | 获取指定驱动最新标签值 |
| `GET` | `/transports` | 是 | 列出所有传输状态 |
| `GET` | `/transports/{name}` | 是 | 获取指定传输状态 |
| `GET` | `/tags` | 是 | 获取所有驱动最新标签值 |
| `POST` | `/write` | 是 | 向设备写入标签值 |
| `GET` | `/write/failed` | 是 | 查询死信队列中失败的写入指令 |
| `GET` | `/rules` | 是 | 列出所有规则统计 |
| `PATCH` | `/rules/disable` | 是 | 启用/禁用指定规则 |
| `GET` | `/stats` | 是 | 获取引擎运行统计 |

::: tip 传输管理端点
`GET /transports` 与 `GET /transports/{name}` 用于查询传输（transport）运行状态，返回 `TransportStatus`（含 `name`、`type`、`state`（连接状态）、`published`、`failed`、`received`、`last_publish`、`queue_size`）。`{name}` 不存在时返回 `404 Not Found`。当前未提供独立的传输管理文档页，相关字段以代码中 `core.TransportStatus` 为准。
:::

### WebSocket 端点

| 路径 | 鉴权 | 说明 |
|:---|:---|:---|
| `/tags/stream` | 是 | 实时数据点流（支持 `?driver=` 过滤） |
| `/logs` | 是 | 实时日志流 |
| `/traffic` | 是 | 实时流量统计（默认每秒推送，支持 `?interval=`） |
| `/memory` | 是 | 实时内存统计（默认每秒推送，支持 `?interval=`） |

::: info WebSocket 鉴权
WebSocket 端点同样受认证中间件保护。由于浏览器 WebSocket API 无法自定义 Header，建议通过 Query 参数传递 Token：`ws://host:port/tags/stream?token=<secret>`。
:::

## 中间件链

所有请求依次经过以下中间件处理：

1. **RequestID** — 生成唯一请求 ID
2. **SafeRequestLogger** — 计算耗时并记录访问日志（自动脱敏 `token` 查询参数，防止凭证泄露）
3. **Recoverer** — 捕获 panic，返回 `500` 防止进程崩溃
4. **CORS** — 跨域处理
5. **RateLimit**（仅当 `global.api.rate-limit-per-sec > 0` 时启用）— 按客户端 IP 令牌桶限速，超限返回 `429 Too Many Requests`
6. **Authentication**（仅受保护路由，且 `global.api.secret` 非空时启用）— Bearer Token 校验

## 快速验证

启动核心后，可用以下命令快速验证 API 是否可用：

```bash
# 健康检查（无需认证）
curl http://localhost:9090/

# 获取驱动列表
curl http://localhost:9090/drivers \
  -H "Authorization: Bearer corec-secret-token"

# 获取引擎统计
curl http://localhost:9090/stats \
  -H "Authorization: Bearer corec-secret-token"
```
