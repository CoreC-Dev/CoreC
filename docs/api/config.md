---
title: 配置管理
description: CoreC 配置管理 API，包含获取配置概要（GET /configs）、配置原文（GET /configs/raw）、dry-run 校验（POST /configs/validate）、全量重载配置（PUT /configs）、增量更新配置（PATCH /configs）、配置变更审计（GET /configs/audit）与服务端配置备份/恢复（/configs/backups）。
---

# 配置管理

以下端点用于查询当前配置概要，以及对核心配置进行全量重载或增量热更新。

所有端点均需认证。

## 获取配置概要

`GET /configs` 返回当前活动配置的安全概要（脱敏），包含全局设置、驱动/传输/规则的数量与名称列表。不暴露敏感字段（如 API Secret）。

### 请求

```http
GET /configs
```

### 响应

`200 OK`

```json
{
  "global": {
    "log-level": "info",
    "api": {
      "listen": ":9090",
      "secret-set": true
    }
  },
  "drivers": [
    { "name": "plc1", "type": "modbus-tcp" },
    { "name": "plc2", "type": "opcua" }
  ],
  "transports": [
    { "name": "mqtt-out", "type": "mqtt" }
  ],
  "rules": [
    { "name": "temp-alert", "type": "value > 80", "action": "alert", "priority": 10 }
  ]
}
```

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `global.log-level` | string | 当前日志级别 |
| `global.api.listen` | string | API 监听地址 |
| `global.api.secret-set` | boolean | 是否配置了 API Secret（值本身不暴露） |
| `drivers` | array | 驱动列表，每项含 `name` 与 `type` |
| `transports` | array | 传输列表，每项含 `name` 与 `type` |
| `rules` | array | 规则列表，每项含 `name`、`type`(match 表达式)、`action`、`priority` |

::: warning 无活动配置 — `503 Service Unavailable`

核心无活动配置（如空配置启动）时返回 `{"error": "no active configuration"}` 而非空配置概要。

```json
{
  "error": "no active configuration"
}
```

:::

### 示例

```bash
curl http://localhost:9090/configs \
  -H "Authorization: Bearer corec-secret-token"
```

---

## 获取完整配置原文

`GET /configs/raw` 返回**展开后的内存配置**全文（YAML 文本，`Content-Type: application/yaml; charset=utf-8`）：配置文件中的 `${ENV_VAR}` 占位符在加载时已替换为当前值，API Secret 与驱动/传输凭据以**明文**回显，供 Dashboard 配置中心编辑器使用。该端点与 `GET /configs` 的脱敏概要不同，仅供已认证调用者访问。

```bash
curl http://localhost:9090/configs/raw \
  -H "Authorization: Bearer corec-secret-token"
```

::: warning 环境变量引用会被"材料化"
`GET /configs/raw` 返回的是内存中展开后的配置。经配置中心编辑后回存（PUT `/configs`），**磁盘上的引用将被字面明文永久替换**，此后该密钥脱离环境变量管理（轮换需改文件，且明文落在配置文件与备份中）。

- 直接管理 YAML 文件（不经 raw 往返）的部署不受影响：PUT 的 payload 原样写盘，占位符保留。
- 建议：把敏感项留在文件层用环境变量占位符管理；编辑敏感项后，在提交前将明文手工恢复为占位符，或仅通过环境变量轮换。
:::

---

## 校验配置（dry-run）

`POST /configs/validate` 对提交的配置**只做解析与校验、不应用**，用于在提交 `PUT /configs` 前发现错误。请求体与 `PUT /configs` 同构（`{"payload": "<yaml>"}`），返回校验结果与非阻塞的空转警告。

### 请求

```http
POST /configs/validate
Content-Type: application/json
```

```json
{
  "payload": "global:\n  log-level: info\ndrivers:\n  - name: plc1\n    type: modbus-tcp\n"
}
```

### 响应

#### 校验通过 — `200 OK`

`warnings` 为非阻塞的空转告警列表（如未配置 api.listen、无数据源），无告警时为 `null`：

```json
{
  "valid": true,
  "warnings": ["control plane (global.api.listen) not configured — ..."]
}
```

#### 校验失败 — `400 Bad Request`

错误原因原样返回（可由调用方修复，无需查服务端日志）：

```json
{
  "valid": false,
  "error": "config validation failed: duplicate driver name: plc1",
  "warnings": null
}
```

请求体非法（JSON 解析失败）与空 payload（`empty config payload`）同样返回 `400`。请求体同样受 1 MiB 上限约束。

---

## 全量重载配置

通过指定配置文件路径和内容，触发核心配置热更新。重载操作调用核心 `ReloadFunc`（`executor.ApplyConfig`），以**差量方式**更新驱动、传输和规则——仅对发生变更的组件执行添加/移除/修改，未变更的组件不受影响。

### 请求

```http
PUT /configs
Content-Type: application/json
```

#### 请求体

```json
{
  "path": "config.yaml",
  "payload": "global:\n  log-level: info\ndrivers:\n  - name: plc1\n    type: modbus-tcp\n"
}
```

| 字段 | 类型 | 必填 | 说明 |
|:---|:---|:---|:---|
| `path` | string | 否 | 配置文件路径，**必须是相对当前活动配置文件所在目录的相对路径**：绝对路径与越出该目录的路径（`../` 等）会被拒绝。仅当 `payload` 为空时才读取该路径对应的文件；`payload` 非空时本字段被忽略。从该路径加载成功后，它成为新的活动配置文件路径 |
| `payload` | string | 否 | 配置文件内容（YAML 文本）。非空时优先使用，`path` 被忽略 |

::: tip 两者均可省略
`path` 与 `payload` 均非必填。当 `payload` 为空且 `path` 也为空时，将重载核心启动时记录的当前配置文件路径（即重载当前配置）。仅 `payload` 为空、`path` 非空时，才从指定路径加载（并做路径穿越校验）。
:::

::: warning 非法 path 返回脱敏 500
`path` 未通过校验（绝对路径、越出配置目录、或未设置活动配置路径）时返回 `500` 与通用错误 `internal server error`，真实原因仅记录在服务端日志（`invalid config path: ...`）。
:::

::: warning 请求体限制
请求体最大 1 MiB。大型配置文件请通过文件挂载 + `path` 字段配合使用，或分批使用 `PATCH` 增量更新。
:::

::: warning 部分配置项需重启生效
`PUT /configs` 差量热更新支持驱动、传输、规则、**派生标签（derived-tags）**和日志级别的运行时变更。派生标签热更新为原子换装：新表达式先编译，编译失败时旧标签集继续运行；更新成功后旧的求值协程即停止。

以下配置项变更会被**检测并记录警告日志**，但不会热生效，需重启进程：

- `global.api`（监听地址、Secret、TLS）— HTTP 服务启动时绑定，无法运行时重新绑定
- `rule-providers`（外部规则集提供者）— 需重启加载
- `rule-groups`（子规则组）— 需重启加载
- `global.engine`（引擎调优参数）— 需重启应用
- `node`（自动发现/心跳组网）— 引擎启动时接线
- `global.buffer`（离线缓冲）— 引擎启动时接线
:::

### 响应

#### 重载成功 — `204 No Content`

无响应体。

#### 配置无效 — `400 Bad Request`

提交的配置**解析失败或未通过校验**（可由调用方修复）时，响应携带真实原因，操作者无需再查服务端日志或先跑一遍 `POST /configs/validate`：

```json
{
  "error": "config validation failed: duplicate driver name: plc1"
}
```

#### 应用失败 — `500 Internal Server Error`

配置校验通过、但应用到运行引擎时失败（如驱动/传输无法初始化）。此类错误可能包含内部细节（连接地址等），响应体返回脱敏的通用错误，真实原因仅记录在服务端日志：

```json
{
  "error": "internal server error"
}
```

#### 请求格式错误 — `400 Bad Request`

```json
{
  "error": "invalid character 'x' looking for beginning of value"
}
```

### 示例

```bash
curl -X PUT http://localhost:9090/configs \
  -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{
    "payload": "global:\n  log-level: info\ndrivers:\n  - name: plc1\n    type: modbus-tcp\n"
  }'
```

::: tip 重载失败自动回滚
差量热更新过程中若任一步失败（驱动/传输无法重建、规则编译失败、派生标签表达式非法），引擎会把本次重载**已应用的变更全部回滚到重载前状态**（删除/新增/替换的组件与规则集均恢复），因此 `GET /configs/raw` 与运行态始终一致。回滚为尽力而为语义：个别组件恢复失败时记录服务端日志，其余组件继续回滚。
:::

---

## 配置变更审计 — `GET /configs/audit`

每次**成功**的配置变更（PUT `/configs` 重载、PATCH `/configs` 补丁、备份恢复、资源级 DELETE）都会记入有界审计轨迹（内存态，最近 500 条，新→旧，重启清零——磁盘事实以配置文件与 `/configs/backups` 快照为准）。引擎未接线审计时返回 `501 Not Implemented`（`config audit not supported by this engine`）。

```json
{
  "changes": [
    { "at": "2026-10-20T12:00:00Z", "action": "patch", "remote": "192.168.1.10:53412", "keys": ["log-level"] },
    { "at": "2026-10-20T11:45:00Z", "action": "reload", "remote": "192.168.1.10:53400", "keys": ["payload"] }
  ],
  "count": 2
}
```

| 字段 | 说明 |
|:---|:---|
| `at` | 变更生效时间 |
| `action` | `reload`（PUT `/configs`、备份恢复、资源级 DELETE——三者都走完整重载链）/ `patch`（PATCH `/configs`） |
| `remote` | 调用方网络地址（来自服务端 RemoteAddr；备份恢复不携带调用方地址，字段省略） |
| `keys` | 变更摘要：PUT 为 `payload`（载荷方式）或 `path:<文件>`（指定路径方式）或 `active-file`（两者均省略、重载当前配置文件）；PATCH 为被应用的键（`log-level` / `log-format` / `stale-threshold`），**一次多键补丁每个键各记一条**；资源级 DELETE 为 `delete-<kind>:<name>`（kind ∈ driver/transport/rule） |
| `details` | 人类可读的变更说明（资源级 DELETE 记录 `"<kind> <name> removed via resource endpoint"`；可缺省） |

---

## 增量更新配置

以 JSON Patch 风格的键值映射对配置进行增量热更新。更新操作调用核心 `PatchFunc`，仅修改指定字段，无需重载整个配置文件。

### 请求

```http
PATCH /configs
Content-Type: application/json
```

#### 请求体

请求体为 JSON 对象，键为配置路径，值为更新内容。支持以下运行时键（roadmap #23）：

| 键 | 类型 | 说明 |
|:---|:---|:---|
| `log-level` | string | 运行时日志级别，接受 `debug` / `info` / `warn` / `warning` / `error` / `silent`；空串为 no-op，未知值返回 `400` |
| `log-format` | string | 运行时日志输出格式（`text`/`json`）；空串为 no-op。只影响 stdout 渲染，`/logs` WS 流的负载不受影响 |
| `stale-threshold` | string (duration) | 运行时数据陈旧阈值（如 `"45s"`；`"0s"` 或 `"0"` 禁用，空串返回 `400`）——`/tags` 的 `is_stale` 标注随之生效，无需重启。引擎不支持运行时阈值调整时返回 `400`（`stale-threshold patching not supported by this engine`） |

```json
{
  "log-level": "debug",
  "log-format": "json",
  "stale-threshold": "45s"
}
```

三个键均为**运行时即时生效**：当前配置摘要（`currentCfg`）同步更新，后续 `GET /configs/raw` 与热重载读到的是补丁后的值；每个成功的补丁都会记入 [`GET /configs/audit`](#配置变更审计--get-configsaudit)。

::: warning 不支持的键
传入表外键将返回 `400 Bad Request`。API 监听地址、Secret 等字段需通过 `PUT /configs` 全量重载并重启生效。
:::

::: warning 请求体限制
请求体最大 1 MiB。
:::

### 响应

#### 更新成功 — `204 No Content`

无响应体。

#### 不支持的键或无效值 — `400 Bad Request`

```json
{
  "error": "unsupported patch key(s): [global.api.secret] (supported: log-format, log-level, stale-threshold)"
}
```

#### 请求格式错误 — `400 Bad Request`

```json
{
  "error": "invalid character 'x' looking for beginning of value"
}
```

### 示例

```bash
# 修改日志级别为 debug
curl -X PATCH http://localhost:9090/configs \
  -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"log-level": "debug"}'
```

---

## 配置备份与恢复

CoreC 在服务端持久化保留配置快照，供一键回滚。与前端（Dashboard）快照栈的区别在于**存放位置与生命周期**：Dashboard 的快照保存在浏览器 `localStorage` 中（只在本机本浏览器可见，清除浏览器数据、换设备或换浏览器即丢，且与 CoreC 实例无绑定）；服务端备份则落在 CoreC 主机磁盘上，随进程重启保留、与实例绑定，恢复走完整校验链。服务端保留最近 **10** 份。

**备份策略**：

- **自动快照**：每次通过 `PUT /configs`（payload 方式）成功变更配置、且活动配置文件已存在于磁盘时，在覆盖持久化**之前**，现有文件先被复制为一份备份（即"成功变更前"的时间点快照）。校验或应用失败不产生备份；复制失败仅记录告警、不阻断重载（可用性优先）。资源级 `DELETE /drivers|transports|rules/{name}` 与备份恢复内部同样以 payload 方式走重载链，因此同样先快照被替换的状态——一次恢复可以被再次恢复撤销。
- **手动快照**：`POST /configs/backups` 立即对当前磁盘配置文件做一份快照（适合手工编辑配置文件或批量改动前留档）。
- **存储**：备份存放于活动配置文件同目录的 `backups/` 子目录，文件名 `<unix-nano>.yaml`，权限 `0o600`（备份内容含 API Secret 与驱动/传输凭据，禁止组/其他用户可读）。超出保留上限时删除最旧的备份。
- **恢复语义**：恢复内容走与 `PUT /configs` 完全相同的 parse → validate → ApplyConfig → persist 链，**绝不绕过校验直接覆盖**；校验失败时当前配置（内存与磁盘）均不受影响。
- 空配置启动（未指定配置文件路径）时没有可备份的文件，备份端点返回明确的 `503` 错误而非 panic。

### 列出备份

`GET /configs/backups` 返回备份元数据列表（新→旧排序）。**只返回元数据，不返回内容**——内容走单独端点。

#### 请求

```http
GET /configs/backups
```

#### 响应

`200 OK`

```json
{
  "backups": [
    { "name": "1760000000000000000.yaml", "timestamp": "2025-10-09T08:53:20Z", "size": 1842 },
    { "name": "1759999999999999999.yaml", "timestamp": "2025-10-09T08:53:19.999999999Z", "size": 1790 }
  ]
}
```

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `backups` | array | 备份列表，新→旧；无备份时为 `[]` |
| `backups[].name` | string | 备份文件名（`<unix-nano>.yaml`），作为读取/恢复的标识 |
| `backups[].timestamp` | string | RFC3339 快照时间（由文件名推导） |
| `backups[].size` | number | 文件大小（字节） |

错误：`503`（无活动配置文件路径，空配置启动）；`401`（未认证）。

### 读取备份内容

`GET /configs/backups/{name}` 返回指定备份的 YAML 原文。

::: warning 内容含明文凭据
安全姿态与 `GET /configs/raw` 一致：备份是磁盘配置文件的逐字节副本，包含 API Secret 与驱动/传输凭据明文，仅对已认证调用者可见——不新增任何放开面。`{name}` 必须严格匹配备份列表中的名字；任何路径拼接/穿越尝试（`../`、绝对路径、多余后缀等）在拼接路径之前即被拒绝，返回 `400`。
:::

| 状态码 | 说明 |
|:---|:---|
| `200` | YAML 原文（`Content-Type: application/yaml; charset=utf-8`） |
| `400` | `{name}` 非法（不符合 `<unix-nano>.yaml` 严格格式） |
| `404` | 备份不存在 |
| `503` | 无活动配置文件路径 |

### 恢复备份

`POST /configs/backups/{name}/restore` 用指定备份内容走完整校验链后应用并持久化。无请求体。

| 状态码 | 说明 |
|:---|:---|
| `204` | 恢复成功（无响应体） |
| `400` | `{name}` 非法；或备份内容未通过校验（响应带具体错误信息，当前配置不受影响） |
| `404` | 备份不存在 |
| `500` | 校验通过但应用失败（真实错误仅记录服务端日志，响应脱敏） |
| `503` | 无活动配置文件路径 |

### 手动创建备份

`POST /configs/backups` 对当前磁盘配置文件立即做一份快照。无请求体。

| 状态码 | 说明 |
|:---|:---|
| `201` | 创建成功，返回 `{"name":"<unix-nano>.yaml"}` |
| `404` | 配置路径已设置但磁盘上尚无配置文件 |
| `503` | 无活动配置文件路径 |

### 示例

```bash
# 列出备份（新→旧）
curl http://localhost:9090/configs/backups \
  -H "Authorization: Bearer corec-secret-token"

# 读取某份备份内容
curl http://localhost:9090/configs/backups/1760000000000000000.yaml \
  -H "Authorization: Bearer corec-secret-token"

# 手动创建一份快照
curl -X POST http://localhost:9090/configs/backups \
  -H "Authorization: Bearer corec-secret-token"

# 恢复指定备份
curl -X POST http://localhost:9090/configs/backups/1760000000000000000.yaml/restore \
  -H "Authorization: Bearer corec-secret-token"
```
