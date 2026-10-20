---
title: 数据读写
description: CoreC 数据读写 API，包含获取所有标签最新值（GET /tags）、向设备写入标签值（POST /write）、写入审计历史（GET /write/history）与死信队列（GET /write/failed）。
---

# 数据读写

以下端点用于读取核心缓存的最新标签值，以及向设备下发写入指令（反向控制）。

所有端点均需认证。

## 获取所有标签最新值

获取所有驱动下全部标签的最新采集值。数据来自核心 `LatestCache`，以标签名为键聚合所有驱动；当多个驱动存在同名标签时，后遍历到的驱动覆盖先者（`DataPoint` 自身携带 `driver` 字段，来源不会丢失）。

### 请求

```http
GET /tags
GET /tags?driver={name}
GET /tags?prefix={prefix}
GET /tags?tag={substr}
```

#### 查询参数（roadmap #35）

| 参数 | 类型 | 必填 | 说明 |
|:---|:---|:---|:---|
| `driver` | string | 否 | 只返回该驱动的最新值快照（跨驱动同名标签不受平铺冲突影响） |
| `prefix` | string | 否 | 标签名**前缀**匹配（如 `?prefix=temp_`） |
| `tag` | string | 否 | 标签名**子串**匹配（如 `?tag=hum`） |

多个参数可组合（如 `?driver=plc2&tag=hum`）。数千 tag 部署建议按驱动或前缀分片查询，避免整表拉取。

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
      "type": "float64",
      "quality": 0,
      "timestamp": "2024-09-08T10:30:00.123456789Z"
    },
    "motor_speed": {
      "driver": "plc2",
      "device": "",
      "group": "g1",
      "tag": "motor_speed",
      "value": 1500,
      "type": "int32",
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
| `type` | string | 数据类型枚举（`DataType` 实现了自定义 `MarshalJSON`/`UnmarshalJSON`）：序列化为可读字符串 `bool, int8, int16, int32, int64, uint8, uint16, uint32, uint64, float32, float64, string, bytes`。反序列化同时接受字符串（如 `"float32"`）和历史整数形式（`9` = float32） |
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
  "type": "float64"
}
```

| 字段 | 类型 | 必填 | 说明 |
|:---|:---|:---|:---|
| `driver` | string | 是 | 目标驱动实例名称 |
| `device` | string | 否 | 目标设备标识（当前未使用，保留字段） |
| `tag` | string | 是 | 目标标签名称 |
| `value` | any | 是 | 待写入的值（默认为**工程值**，见下方 scale 说明） |
| `type` | string \| int | 是 | 值的数据类型。接受可读字符串（如 `"float32"`，推荐）或历史整数形式（`10` = float64）。完整映射：`bool=0, int8=1, int16=2, int32=3, int64=4, uint8=5, uint16=6, uint32=7, uint64=8, float32=9, float64=10, string=11, bytes=12` |
| `raw` | bool | 否 | 默认 `false`。设为 `true` 时跳过 scale/offset 反演，把 `value` 按设备原始值直接编码（用于调试/原始报文下发场景） |

::: tip 与 scale/offset 的关系（写路径反演）
读取方向配置了 `scale`/`offset` 的标签，读取上报的是工程值（`原始值 × scale + offset`）。写入时默认接受**工程值**，驱动在编码前自动反演回原始值（`(value - offset) / scale`），保证读写往返一致：UI 显示 1.5（scale=0.01），下发 1.5，设备寄存器收到 150。需要直接写设备原始值的调试场景，传 `"raw": true`。`bool`/`string`/`bytes` 类型不参与变换，原样透传。
:::

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

### 批量写入 — `POST /write/batch`

配方下发、开机整定等场景一次提交一组写入指令（roadmap #37），**顺序执行、逐条返回结果**——部分失败不影响后续指令，每条尝试照常进入写入审计历史与（重试耗尽后的）死信队列，语义与单条写完全一致。

```json
{
  "writes": [
    { "driver": "plc1", "tag": "setpoint", "value": 50.0, "type": "float64" },
    { "driver": "plc1", "tag": "motor_enable", "value": true, "type": "bool" }
  ]
}
```

| 状态码 | 说明 |
|:---|:---|
| `200 OK` | 至少一条成功（含部分失败）；`results` 按请求顺序逐条返回，`succeeded`/`failed` 汇总 |
| `422 Unprocessable Entity` | 全部失败（请求合法、设备层面全部拒绝）——与 2xx 部分成功区分 |
| `400 Bad Request` | 空数组、超过 128 条、或请求体非法 |

**响应示例**：

```json
{
  "results": [
    { "success": true },
    { "success": false, "error": "modbus: device rejected" }
  ],
  "succeeded": 1,
  "failed": 1
}
```

::: warning 顺序性与体积上限
指令**严格按数组顺序执行**（不并行——保持与写信号量调度一致的可预测性）；单批上限 128 条（请求体同时受 1 MiB 全局上限约束），更大批次拆分为多个请求。
:::

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
    "type": "float64"
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
    "type": "bool"
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
    "type": "int32"
  }'
```

:::

### 反向控制流程

```
API POST /write  →  Engine.WriteTag(cmd)  →  Driver.Write([]cmd)  →  设备
```

写入操作通过引擎转发至对应驱动的 `Write` 方法，在独立 goroutine 中执行，不影响采集 goroutine 的上行数据流。

写入失败时会按 `engine.write-retry-count` 进行指数退避重试，全部失败后进入死信队列。可通过 `GET /write/failed` 查询死信队列中的失败指令；每次写尝试（成功与失败）均会记入写入审计历史，可通过 [`GET /write/history`](#查询写入审计历史) 查询。

---

## 查询失败写入指令

查询死信队列中所有重试耗尽后仍失败的写入指令。死信队列在内存中维护，上限 1000 条，超出时驱逐最旧条目。

### 请求

```http
GET /write/failed
GET /write/failed?source=ingress
```

`?source=` 选择要查询的死信来源（默认 `engine`）：

| `source` | 说明 |
|:---|:---|
| `engine`（默认） | 引擎死信队列——重试耗尽后仍失败的写指令 |
| `ingress` | **入站丢弃**——命令到达传输时因命令通道满而被丢弃的指令（如 MQTT command-topic 消费洪峰）。条目带 `transport` 字段标明丢弃它的传输 |

未知 `source` 值返回 `400 Bad Request`。死信深度与累计入队数也可经 `corec_dead_letter_queue_size` / `corec_dead_letter_pushed_total` 指标监控（入站丢弃量见 `corec_transport_dropped_commands_total`）。

### 响应

`200 OK`

```json
{
  "failed_writes": [
    {
      "command": {
        "driver": "plc1",
        "device": "192.168.1.10",
        "tag": "setpoint",
        "value": 50.0,
        "type": "float64"
      },
      "error": "modbus: connection refused",
      "failed_at": "2024-09-08T10:30:05.123456789Z",
      "attempts": 4
    }
  ],
  "count": 1
}
```

### DeadLetterEntry 字段说明

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `command` | object | 原始 `WriteCommand`（driver, device, tag, value, type） |
| `error` | string | 最后一次失败的错误信息（入站丢弃为丢弃原因，如 `command channel full at ingress`） |
| `failed_at` | string (RFC 3339) | 进入死信队列的时间 |
| `attempts` | int | 总尝试次数（含首次 + 重试；入站丢弃恒为 1） |
| `transport` | string | 仅 `source=ingress` 时出现：丢弃该命令的传输实例名 |

### 示例

```bash
curl http://localhost:9090/write/failed \
  -H "Authorization: Bearer corec-secret-token"
```

---

## 查询写入审计历史

返回写入审计历史（audit trail）：最近 **1000** 条写入尝试，**成功与失败均记录**，按时间新→旧排列。控制类系统的合规刚需——不仅失败要可见（死信队列），成功的 PLC 写操作也必须可追溯（何时、何人、从何处写了哪个点）。

与 `GET /write/failed` 的区别：死信队列只保留重试耗尽的失败指令；审计历史是"最近发生了哪些写"的完整有界记录，每一条**尝试**记一条（反向流指令重试时，每次重试各记一条）。

### 请求

```http
GET /write/history
GET /write/history?driver={name}&tag={substr}
GET /write/history?since={rfc3339}&limit={n}
```

#### 查询参数（roadmap #35）

| 参数 | 类型 | 必填 | 说明 |
|:---|:---|:---|:---|
| `driver` | string | 否 | 只返回该驱动的写记录（精确匹配） |
| `tag` | string | 否 | 标签**子串**匹配 |
| `since` | string (RFC 3339) | 否 | 只返回该时刻**及之后**的记录（如 `2026-10-20T12:00:00Z`；非法值返回 400） |
| `limit` | int | 否 | 最多返回前 n 条（新→旧截断） |

参数可组合，过滤在服务端完成——合规检索不再需要拉全量 1000 条后客户端过滤。

### 响应

`200 OK`

```json
{
  "writes": [
    {
      "timestamp": "2024-09-08T10:30:00.123456789Z",
      "driver": "plc1",
      "device": "192.168.1.10",
      "tag": "setpoint",
      "value": 50.0,
      "type": "float64",
      "success": true,
      "source": "api",
      "remote": "192.168.1.50:53412"
    },
    {
      "timestamp": "2024-09-08T10:29:58.987654321Z",
      "driver": "plc1",
      "device": "192.168.1.10",
      "tag": "motor_enable",
      "value": true,
      "type": "bool",
      "success": false,
      "error": "modbus exception code 3",
      "source": "command"
    }
  ],
  "count": 2
}
```

### WriteAuditEntry 字段说明

| 字段 | 类型 | 说明 |
|:---|:---|:---|
| `timestamp` | string (RFC 3339) | 该次写尝试的记录时间 |
| `driver` | string | 目标驱动实例名称 |
| `device` | string | 目标设备标识（常为空字符串） |
| `tag` | string | 目标标签名称 |
| `value` | any | 实际写入的值。与运行日志刻意不记录值不同，写审计历史的核心用途就是记录"写了什么" |
| `type` | string | 值的数据类型（`DataType`，按字符串序列化） |
| `success` | bool | 该次尝试是否成功 |
| `error` | string | 失败信息（仅失败时出现） |
| `source` | string | 写来源：`api`（`POST /write` 直写）或 `command`（反向流指令，即 MQTT/HTTP 命令通道触发的写） |
| `remote` | string | `source=api` 时为调用方网络地址（`r.RemoteAddr`）；`source=command` 时省略（无对端地址） |
| `raw` | bool | `true` 表示该写跳过了 scale/offset 反演（记录的 `value` 即设备原始值）；默认省略（`value` 为工程值） |

::: info 审计来源不可伪造
`source` 与 `remote` 由服务端在信任边界处写入：`POST /write` 在解码请求体**之后**统一盖章 `source="api"` + 调用方地址；引擎命令监听器对每条来自传输通道的指令统一盖章 `source="command"`。请求体或指令载荷中携带的同名字段一律被覆盖，外部输入无法污染审计历史。
:::

::: warning 历史边界
审计历史为内存环形缓冲，上限 1000 条（超出驱逐最旧条目）：跨配置 reload 存活，**进程重启后清空**；它是有界的最近记录，不是持久化审计日志。需要长期留存时请由外部系统轮询拉取归档。
:::

### 引擎不支持时 — `501 Not Implemented`

当引擎未实现可选角色接口 `core.WriteAuditor`（如测试替身、第三方 Engine 实现）时返回：

```json
{
  "error": "write history not supported by this engine"
}
```

### 示例

```bash
curl http://localhost:9090/write/history \
  -H "Authorization: Bearer corec-secret-token"
```
