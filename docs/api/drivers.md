---
title: 驱动管理
description: CoreC 驱动管理 API，包含列出驱动、查询驱动状态、获取驱动最新标签值等端点。
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
      "error_count": 3
    },
    {
      "name": "plc2",
      "type": "s7",
      "state": 1,
      "last_read": "0001-01-01T00:00:00Z",
      "last_error": "connection refused",
      "tag_count": 64,
      "read_count": 0,
      "error_count": 1
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
  "error_count": 3
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
      "type": 10,
      "quality": 0,
      "timestamp": "2024-09-08T10:30:00.123456789Z"
    },
    "pressure": {
      "driver": "plc1",
      "device": "",
      "group": "g1",
      "tag": "pressure",
      "value": 101.3,
      "type": 10,
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
| `type` | int | 数据类型枚举（`DataType` 为 `int`，按整数序列化），见下表 |
| `quality` | int | 数据质量枚举（`Quality` 为 `int`，按整数序列化），见下表 |
| `timestamp` | string (RFC 3339) | 采集时间戳 |
| `is_stale` | bool | 数据是否陈旧（超过引擎 `stale-threshold` 未更新），仅在启用陈旧检测时出现 |
| `metadata` | object | 附加元数据键值对（可选，存在时才出现） |

### 数据类型（type）

`type` 为 `DataType`（`int`）枚举，按整数序列化：

| 值 | Go 类型 | 说明 |
|:---|:---|:---|
| `0` | bool | 布尔值 |
| `1` / `2` / `3` / `4` | int8 / int16 / int32 / int64 | 有符号整数 |
| `5` / `6` / `7` / `8` | uint8 / uint16 / uint32 / uint64 | 无符号整数 |
| `9` / `10` | float32 / float64 | 浮点数 |
| `11` | string | 字符串 |
| `12` | []byte | 字节序列 |

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
