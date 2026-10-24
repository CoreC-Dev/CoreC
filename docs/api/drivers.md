---
title: 驱动管理
description: CoreC 驱动管理 API，包含列出驱动、查询驱动状态、获取驱动最新标签值与驱动连通性测试（POST /drivers/test）等端点。
---

# 驱动管理

驱动（Driver）是 CoreC 南向层的协议适配器，负责与工业设备通信。以下端点用于查询驱动运行状态及其最新采集的标签值。

所有端点均需认证。

## 获取驱动列表

获取所有已注册驱动的运行状态。

### 请求

```http
GET /drivers
```

### 响应

`200 OK`

```json
{
  "drivers": [
    {
      "name": "plc1",
      "type": "modbus-tcp",
      "state": 2,
      "last_read": "2024-09-08T10:30:00.123456789Z",
      "last_error": "",
      "tag_count": 120,
      "read_count": 45000,
      "error_count": 3,
      "reconnect_count": 1
    },
    {
      "name": "plc2",
      "type": "s7",
      "state": 1,
      "last_read": "0001-01-01T00:00:00Z",
      "last_error": "connection refused",
      "tag_count": 64,
      "read_count": 0,
      "error_count": 1,
      "reconnect_count": 0
    }
  ]
}
```

### 字段说明 — DriverStatus

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `name` | string | 驱动实例名称 |
| `type` | string | 驱动协议类型（`modbus-tcp` / `s7` / `opcua` 等） |
| `state` | int | 连接状态枚举，见下表 |
| `last_read` | string (RFC 3339) | 最近一次成功读取时间 |
| `last_error` | string | 最近一次错误信息，无错误时为空字符串 |
| `tag_count` | int | 该驱动配置的标签总数 |
| `read_count` | uint64 | 累计成功读取次数 |
| `error_count` | uint64 | 累计错误次数 |
| `reconnect_count` | uint64 | 累计重连尝试次数 |

### 连接状态（state）

`state` 为 `ConnState`（`int`）枚举，按整数序列化：

| 值 | 说明 |
|:---|:---|
| `0` | disconnected — 已断开 |
| `1` | connecting — 连接中 |
| `2` | connected — 已连接 |
| `3` | error — 错误状态 |

### 示例

```bash
curl http://localhost:9090/drivers \
  -H "Authorization: Bearer corec-secret-token"
```

---

## 获取单个驱动状态

按名称获取指定驱动的运行状态。

### 请求

```http
GET /drivers/{name}
```

#### 路径参数

| 参数 | 类型 | 说明 |
|:---|:---|:---|
| `name` | string | 驱动实例名称 |

### 响应

#### 成功 — `200 OK`

返回单个 `DriverStatus` 对象（不含外层 `drivers` 数组）：

```json
{
  "name": "plc1",
  "type": "modbus-tcp",
  "state": 2,
  "last_read": "2024-09-08T10:30:00.123456789Z",
  "last_error": "",
  "tag_count": 120,
  "read_count": 45000,
  "error_count": 3,
  "reconnect_count": 1
}
```

#### 驱动不存在 — `404 Not Found`

```json
{
  "error": "driver not found"
}
```

### 示例

```bash
curl http://localhost:9090/drivers/plc1 \
  -H "Authorization: Bearer corec-secret-token"
```

---

## 获取驱动最新标签值

获取指定驱动下所有标签的最新采集值。数据来自核心 `LatestCache`，由 `RWMutex` 保护，读取零阻塞。

### 请求

```http
GET /drivers/{name}/tags
```

#### 路径参数

| 参数 | 类型 | 说明 |
|:---|:---|:---|
| `name` | string | 驱动实例名称 |

::: info 空名称行为
若 `name` 为空字符串，核心将返回**所有驱动**的最新标签值（与 `GET /tags` 行为一致）。
:::

### 响应

`200 OK`

返回以标签名为键、`DataPoint` 为值的映射：

```json
{
  "tags": {
    "temperature": {
      "driver": "plc1",
      "device": "",
      "group": "g1",
      "tag": "temperature",
      "value": 42.5,
      "type": "float64",
      "quality": 0,
      "timestamp": "2024-09-08T10:30:00.123456789Z"
    },
    "pressure": {
      "driver": "plc1",
      "device": "",
      "group": "g1",
      "tag": "pressure",
      "value": 101.3,
      "type": "float64",
      "quality": 0,
      "timestamp": "2024-09-08T10:30:00.123456789Z"
    }
  }
}
```

### 字段说明 — DataPoint

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `driver` | string | 采集该值的驱动名称 |
| `device` | string | 设备标识。当前始终为空字符串 `""`（配置无 Device 来源） |
| `group` | string | 采集分组名称 |
| `tag` | string | 标签名称 |
| `value` | any | 标签值（类型由 `type` 决定） |
| `type` | string | 数据类型枚举（`DataType` 实现自定义 `MarshalJSON`，按字符串序列化），见下表 |
| `quality` | int | 数据质量枚举（`Quality` 为 `int`，按整数序列化），见下表 |
| `timestamp` | string (RFC 3339) | 采集时间戳 |
| `is_stale` | bool | 数据是否陈旧（超过引擎 `stale-threshold` 未更新），仅在启用陈旧检测时出现 |
| `metadata` | object | 附加元数据键值对（可选，存在时才出现） |

### 数据类型（type）

`type` 为 `DataType` 枚举，实现了自定义 `MarshalJSON`/`UnmarshalJSON`：序列化为可读字符串，反序列化同时接受字符串和历史整数形式。

| 序列化值 | 整数 | Go 类型 | 说明 |
|:---|:---|:---|:---|
| `"bool"` | `0` | bool | 布尔值 |
| `"int8"` / `"int16"` / `"int32"` / `"int64"` | `1` / `2` / `3` / `4` | int8 / int16 / int32 / int64 | 有符号整数 |
| `"uint8"` / `"uint16"` / `"uint32"` / `"uint64"` | `5` / `6` / `7` / `8` | uint8 / uint16 / uint32 / uint64 | 无符号整数 |
| `"float32"` / `"float64"` | `9` / `10` | float32 / float64 | 浮点数 |
| `"string"` | `11` | string | 字符串 |
| `"bytes"` | `12` | []byte | 字节序列 |

> 写命令（`/write`）的 `type` 字段同时接受上表的字符串值和整数形式（向后兼容）。

### 数据质量（quality）

`quality` 为 `Quality`（`int`）枚举，按整数序列化：

| 值 | 说明 |
|:---|:---|
| `0` | good — 数据有效 |
| `1` | bad — 数据无效（设备错误、通信失败等） |
| `2` | uncertain — 数据不确定 |

### 示例

```bash
curl http://localhost:9090/drivers/plc1/tags \
  -H "Authorization: Bearer corec-secret-token"
```

---

## 驱动连通性测试

用一份**尚未保存**的驱动配置创建临时驱动实例，按配置驱动的真实生命周期（创建 → 初始化 → 启动连接）试连一次，测出连接耗时，然后**必定关闭**。用于在保存驱动配置前验证 host/port/endpoint 等参数是否可达——此前配置错误只能保存后靠生产流量暴露。

### 请求

```http
POST /drivers/test
Content-Type: application/json
```

#### 请求体

```json
{
  "type": "modbus-tcp",
  "settings": {
    "host": "192.168.1.100",
    "port": 502,
    "slave-id": 1,
    "timeout": "3s"
  },
  "name": "plc-line-2-probe"
}
```

| 字段 | 类型 | 必填 | 说明 |
|:---|:---|:---|:---|
| `type` | string | 是 | 已注册的驱动类型：`modbus-tcp`、`modbus-rtu`、`modbus-rtuovertcp`、`modbus-udp`、`modbus-rtuoverudp`、`modbus-tls`、`s7`、`opcua`。空值返回 `400`；未注册类型返回 `400 "unknown driver type: <type>"` |
| `settings` | object | 否 | 协议特有连接参数，结构与 YAML 配置中 `drivers[].settings` 完全一致（如 Modbus 的 `host`/`port`/`slave-id`，OPC UA 的 `endpoint`） |
| `name` | string | 否 | 临时实例的日志标签，缺省 `"driver-test"`。仅用于日志，不代表任何注册或持久化 |

::: warning 字段名是 json tag，不是 yaml 字段名
请求字段 `type`/`settings`/`name` 是 handler 内定义的**显式 json tag**——`core.DriverConfig` 只有 yaml tag（它是 YAML 配置契约），API 层不直接反序列化它，因此请求字段名与 YAML 字段名无关，按本表使用。
:::

::: warning 请求体限制
请求体最大 1 MiB。
:::

### 响应

**连通与不通都是 `200 OK`**——测试跑完拿到否定结果也是成功的测试（`ok:false` 表示"连接失败"，而非"测试失败"）。

#### 连接成功 — `200 OK`

```json
{
  "ok": true,
  "connected": true,
  "latency_ms": 12,
  "driver_type": "modbus-tcp"
}
```

#### 连接失败 — `200 OK`

```json
{
  "ok": false,
  "connected": false,
  "error": "dial tcp 192.168.1.100:502: connect: connection refused",
  "latency_ms": 3,
  "driver_type": "modbus-tcp"
}
```

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `ok` | bool | 生命周期是否以连通结束（等价于 `connected`） |
| `connected` | bool | `Init` + `Start`（连接）是否成功 |
| `error` | string | 失败信息（仅失败时出现）；超时为 `"driver test timed out after 5s"` |
| `latency_ms` | int | 连接尝试耗时（初始化+启动，不含 teardown），毫秒 |
| `driver_type` | string | 回显请求的 `type` |

#### 请求非法 — `400 Bad Request`

```json
{
  "error": "unknown driver type: no-such-type"
}
```

其余 `400` 场景：JSON 解析失败、`type` 为空（`"type is required"`）、驱动工厂拒绝该 settings（如非法参数键）。

### 生命周期与安全保证

- **临时实例不落配置**：实例绝不注册进引擎或插件注册表，请求绝不写入当前配置；`name` 仅出现在日志中。
- **连接即关**：所有路径（成功、失败、超时）都会 `Stop()` 临时实例，不泄漏连接或设备句柄。
- **5 秒硬超时**：整个生命周期在 `context.WithTimeout`（5s）之下运行，并对不感知 context 的驱动提供 goroutine+select 兜底，handler 必定返回。
- **只依赖插件注册表**：不经过引擎，引擎状态（运行/挂起）不影响本端点。

### 示例

```bash
# 保存前先试连：用计划写入配置的 settings 原样探测
curl -X POST http://localhost:9090/drivers/test \
  -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"type": "modbus-tcp", "settings": {"host": "192.168.1.100", "port": 502, "slave-id": 1}}'
```

### 典型用法

```bash
# Dashboard 驱动编辑器中：
# 1. 填写新驱动的 type 与 settings
# 2. POST /drivers/test 验证参数可达性
# → {"ok": true, "connected": true, ...} 后再写入配置并 PUT /configs
# → {"ok": false, "error": "connection refused", ...} 时修正 host/port 再试
```

---

## 暂停与恢复驱动采集

设备维修、更换传感器等场景需要在**不动配置**的情况下临时停止某驱动的采集（此前只能整份改配置，走一次完整的删除+重建）。暂停只标记调度器：任务保留、驱动连接保持、恢复后从下一 tick 继续。

### 暂停 — `POST /drivers/{name}/pause`

```bash
curl -X POST http://localhost:9090/drivers/plc1/pause \
  -H "Authorization: Bearer corec-secret-token"
```

| 状态码 | 说明 |
|:---|:---|
| `204 No Content` | 已暂停 |
| `400 Bad Request` | 请求体不是合法 JSON（该端点无请求体，传空或不传即可） |
| `404 Not Found` | 驱动不存在 |
| `500 Internal Server Error` | 暂停操作内部失败 |
| `501 Not Implemented` | 引擎不支持暂停 |

暂停期间：该驱动的所有采集任务跳过 tick（不读、不产生数据点）；驱动连接**不关闭**（恢复即读，无重连延迟）；`/tags` 中的既有缓存值随 `stale-threshold` 标记为陈旧。

### 恢复 — `POST /drivers/{name}/resume`

| 状态码 | 说明 |
|:---|:---|
| `204 No Content` | 已恢复（下一 tick 起采集） |
| `404 Not Found` | 驱动不存在 |
| `500 Internal Server Error` | 恢复操作内部失败 |
| `501 Not Implemented` | 引擎不支持暂停/恢复 |

### 查询暂停列表 — `GET /drivers/paused`

```json
{
  "paused": ["plc1", "plc3"]
}
```

引擎不支持暂停角色时同样返回 `501 Not Implemented`。

::: info 与配置删除的区别
暂停是**调度器级**开关：配置、连接、缓存全部保留，恢复零成本。删除驱动（改配置）则拆除连接、清除任务，恢复需完整重建与重连。注意：暂停状态不落盘，进程重启后清零。
:::
