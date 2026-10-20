---
title: 配置管理
description: CoreC 配置管理 API，包含获取配置概要（GET /configs）、全量重载配置（PUT /configs）、增量更新配置（PATCH /configs）与服务端配置备份/恢复（/configs/backups）。
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

### 示例

```bash
curl http://localhost:9090/configs \
  -H "Authorization: Bearer corec-secret-token"
```

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
  "path": "/etc/corec/config.yaml",
  "payload": "global:\n  log-level: info\ndrivers:\n  - name: plc1\n    type: modbus-tcp\n"
}
```

| 字段 | 类型 | 必填 | 说明 |
|:---|:---|:---|:---|
| `path` | string | 否 | 配置文件路径。仅当 `payload` 为空时才读取该路径对应的文件；`payload` 非空时本字段被忽略 |
| `payload` | string | 否 | 配置文件内容（YAML 文本）。非空时优先使用，`path` 被忽略 |

::: tip 两者均可省略
`path` 与 `payload` 均非必填。当 `payload` 为空且 `path` 也为空时，将重载核心启动时记录的当前配置文件路径（即重载当前配置）。仅 `payload` 为空、`path` 非空时，才从指定路径加载（且会做路径穿越校验）。
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

::: tip 重载失败自动回滚
差量热更新过程中若任一步失败（驱动/传输无法重建、规则编译失败、派生标签表达式非法），引擎会把本次重载**已应用的变更全部回滚到重载前状态**（删除/新增/替换的组件与规则集均恢复），因此 `GET /configs/raw` 与运行态始终一致。回滚为尽力而为语义：个别组件恢复失败时记录服务端日志，其余组件继续回滚。
:::

::: warning 环境变量引用会被"材料化"
`GET /configs/raw` 返回的是**展开后的内存配置**：配置文件中的 <code v-pre>\${COREC_API_SECRET}</code> 等环境变量占位符在加载时已替换为其当前值。经 Dashboard 配置中心编辑后回存（PUT `/configs`），**磁盘上的引用将被字面明文永久替换**，此后该密钥脱离环境变量管理（轮换需改文件，且明文落在配置文件与备份中）。

- 直接管理 YAML 文件（不经 raw 往返）的部署不受影响：PUT 的 payload 原样写盘，占位符保留。
- 建议：把敏感项留在文件层用环境变量占位符管理；Dashboard 编辑敏感项后，在提交前将明文手工恢复为占位符，或仅通过环境变量轮换。
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
    "path": "/etc/corec/config.yaml",
    "payload": "global:\n  log-level: info\ndrivers:\n  - name: plc1\n    type: modbus-tcp\n"
  }'
```

---

## 增量更新配置

以 JSON Patch 风格的键值映射对配置进行增量热更新。更新操作调用核心 `PatchFunc`，仅修改指定字段，无需重载整个配置文件。

### 请求

```http
PATCH /configs
Content-Type: application/json
```

#### 请求体

请求体为 JSON 对象，键为配置路径，值为更新内容。当前仅支持以下键：

| 键 | 类型 | 说明 |
|:---|:---|:---|
| `log-level` | string | 运行时日志级别（`debug`/`info`/`warn`/`error`） |

```json
{
  "log-level": "debug"
}
```

::: warning 仅支持 log-level
`PATCH /configs` 当前仅支持修改 `log-level`。传入任何其他键将返回 `400 Bad Request`。API 监听地址、Secret 等字段需通过 `PUT /configs` 全量重载并重启生效。
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
  "error": "unsupported patch key(s): [global.api.secret] (supported: log-level)"
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

CoreC 在服务端持久化保留配置快照，供一键回滚：前端（Dashboard）的快照栈只在内存里、刷新即丢，而服务端备份落盘、进程重启后仍可用，保留最近 **10** 份。

**备份策略**：

- **自动快照**：每次通过 `PUT /configs`（payload 方式）成功变更配置、且活动配置文件已存在于磁盘时，在覆盖持久化**之前**，现有文件先被复制为一份备份（即"成功变更前"的时间点快照）。校验或应用失败不产生备份；复制失败仅记录告警、不阻断重载（可用性优先）。恢复动作本身也会先快照被替换的状态，因此一次恢复可以被再次恢复撤销。
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
