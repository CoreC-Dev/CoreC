---
title: 数据读写
description: CoreC 数据读写 API，包含获取所有标签最新值（GET /tags）和向设备写入标签值（POST /write）。
---

# 数据读写

以下端点用于读取核心缓存的最新标签值，以及向设备下发写入指令（反向控制）。

所有端点均需认证。

## 获取所有标签最新值

获取所有驱动下全部标签的最新采集值。数据来自核心 `LatestCache`，以标签名为键聚合所有驱动；当多个驱动存在同名标签时，后遍历到的驱动覆盖先者（`DataPoint` 自身携带 `driver` 字段，来源不会丢失）。

### 请求

```http
GET /tags
```

### 响应

`200 OK`

返回以标签名为键、`DataPoint` 为值的映射，包含所有驱动的数据（以下示例展示单个 `DataPoint` 的序列化结构）：

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
    "motor_speed": {
      "driver": "plc2",
      "device": "",
      "group": "g1",
      "tag": "motor_speed",
      "value": 1500,
      "type": 3,
      "quality": 0,
      "timestamp": "2024-09-08T10:30:00.987654321Z"
    }
  }
}
```

::: tip 按驱动过滤
如需仅获取单个驱动的标签值，使用 `GET /drivers/{name}/tags`，详见[驱动管理](./drivers)。
:::

### DataPoint 字段说明

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `driver` | string | 采集该值的驱动名称 |
| `device` | string | 设备标识。当前配置无 Device 来源（`DriverConfig`/`TagConfig` 均无 Device 字段），始终为空字符串 `""` |
| `group` | string | 采集分组名称 |
| `tag` | string | 标签名称 |
| `value` | any | 标签值 |
| `type` | int | 数据类型枚举（`DataType` 为 `int`，无自定义 `MarshalJSON`，按整数序列化）：`0=bool, 1=int8, 2=int16, 3=int32, 4=int64, 5=uint8, 6=uint16, 7=uint32, 8=uint64, 9=float32, 10=float64, 11=string, 12=bytes` |
| `quality` | int | 数据质量枚举（`Quality` 为 `int`，按整数序列化）：`0=good, 1=bad, 2=uncertain` |
| `timestamp` | string (RFC 3339) | 采集时间戳 |
| `is_stale` | bool | 数据是否陈旧（超过引擎 `stale-threshold` 未更新），仅在启用陈旧检测时出现 |
| `metadata` | object | 附加元数据（可选） |

### 示例

```bash
curl http://localhost:9090/tags \
  -H "Authorization: Bearer corec-secret-token"
```

---

## 写入标签值

向指定设备的指定标签写入值。写入指令通过核心 `WriteTag` 方法转发至对应驱动的 `Write` 接口，不阻塞上行采集管道。

### 请求

```http
POST /write
Content-Type: application/json
```

#### 请求体 — WriteCommand

```json
{
  "driver": "plc1",
  "device": "192.168.1.10",
  "tag": "setpoint",
  "value": 50.0,
  "type": 10
}
```

| 字段 | 类型 | 必填 | 说明 |
|:---|:---|:---|:---|
| `driver` | string | 是 | 目标驱动实例名称 |
| `device` | string | 否 | 目标设备标识（当前未使用，保留字段） |
| `tag` | string | 是 | 目标标签名称 |
| `value` | any | 是 | 待写入的值 |
| `type` | int | 是 | 值的数据类型枚举（`DataType` 为 `int`，同 `DataPoint.type`）：`0=bool, …, 10=float64, 11=string, 12=bytes` |

::: warning 请求体限制
请求体最大 1 MiB。超出限制时，JSON 解码将失败并返回 `400 Bad Request`。
:::

### 响应

#### 写入成功 — `200 OK`

```json
{
  "success": true
}
```

#### 写入失败 — `500 Internal Server Error`

具体错误原因（如驱动写入失败、驱动未找到等）仅记录在服务端日志中，响应体始终返回脱敏的通用错误，避免泄露设备地址等内部细节：

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

### WriteResult 字段说明

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `success` | bool | 写入是否成功 |
| `error` | string | 错误信息（仅失败时出现） |

### 示例

::: code-group

```bash [curl]
curl -X POST http://localhost:9090/write \
  -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{
    "driver": "plc1",
    "device": "192.168.1.10",
    "tag": "setpoint",
    "value": 50.0,
    "type": 10
  }'
```

```bash [写入布尔值]
curl -X POST http://localhost:9090/write \
  -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{
    "driver": "plc1",
    "device": "192.168.1.10",
    "tag": "motor_enable",
    "value": true,
    "type": 0
  }'
```

```bash [写入整数]
curl -X POST http://localhost:9090/write \
  -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{
    "driver": "plc2",
    "device": "192.168.1.20",
    "tag": "target_speed",
    "value": 3000,
    "type": 3
  }'
```

:::

### 反向控制流程

```
API POST /write  →  Engine.WriteTag(cmd)  →  Driver.Write([]cmd)  →  设备
```

写入操作通过引擎转发至对应驱动的 `Write` 方法，在独立 goroutine 中执行，不影响采集 goroutine 的上行数据流。

写入失败时会按 `engine.write-retry-count` 进行指数退避重试，全部失败后进入死信队列。可通过 `GET /write/failed` 查询死信队列中的失败指令。

---

## 查询失败写入指令

查询死信队列中所有重试耗尽后仍失败的写入指令。死信队列在内存中维护，上限 1000 条，超出时驱逐最旧条目。

### 请求

```http
GET /write/failed
```

### 响应

`200 OK`

```json
{
  "entries": [
    {
      "command": {
        "driver": "plc1",
        "device": "192.168.1.10",
        "tag": "setpoint",
        "value": 50.0,
        "type": 10
      },
      "error": "modbus: connection refused",
      "failed_at": "2024-09-08T10:30:05.123456789Z",
      "attempts": 4
    }
  ]
}
```

### DeadLetterEntry 字段说明

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `command` | object | 原始 `WriteCommand`（driver, device, tag, value, type） |
| `error` | string | 最后一次失败的错误信息 |
| `failed_at` | string (RFC 3339) | 进入死信队列的时间 |
| `attempts` | int | 总尝试次数（含首次 + 重试） |

### 示例

```bash
curl http://localhost:9090/write/failed \
  -H "Authorization: Bearer corec-secret-token"
```
