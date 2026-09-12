---
title: 配置管理
description: CoreC 配置管理 API，包含获取配置概要（GET /configs）、全量重载配置（PUT /configs）和增量更新配置（PATCH /configs）。
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

通过指定配置文件路径和内容，触发核心全量重载。重载操作调用核心 `ReloadFunc`，会重建驱动、传输和规则。

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

### 响应

#### 重载成功 — `204 No Content`

无响应体。

#### 重载失败 — `500 Internal Server Error`

具体错误原因（如 YAML 解析失败、驱动重建失败等）仅记录在服务端日志中，响应体始终返回脱敏的通用错误，避免泄露内部路径与细节：

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
