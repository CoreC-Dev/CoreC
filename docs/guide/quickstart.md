---
title: 快速上手
description: 5 分钟编写最小配置、启动 CoreC、验证数据采集与第一个数据点
---

# 快速上手

本章带你用一份最小配置跑通 CoreC 的完整数据链路：从 Modbus 设备采集一个温度点位，经规则路由，推送到 MQTT Broker。

## 场景假设

```
Modbus TCP 设备 (192.168.1.100:502)  ──采集──▶  CoreC  ──推送──▶  MQTT Broker (公共测试服务器)
```

我们将采集一个保持寄存器中的 `float32` 温度值，每秒一次，推送到 EMQX 公共 Broker 的 `corec/demo/temperature` 主题。

::: tip 无真实设备？
本章末尾提供了 [使用模拟设备验证](#使用模拟设备验证) 方案，无需真实 PLC 即可跑通数据流。你也可以先跳到 [验证 API](#验证-api) 章节，仅验证控制面是否就绪。
:::

## 第一步：编写最小配置

在工作目录下创建 `config.yaml`：

```yaml
# config.yaml —— CoreC 最小可用配置
global:
  log-level: info
  api:
    listen: 0.0.0.0:9090
    secret: "corec-secret-token"

# 南向：一台 Modbus TCP 设备
drivers:
  - name: demo-plc
    type: modbus-tcp
    settings:
      host: 192.168.1.100
      port: 502
      slave-id: 1
      timeout: 3s
    tags:
      - name: temperature
        address: "40001"      # 保持寄存器 0
        type: float32
        group: sensors
        interval: 1s

# 北向：MQTT 公共测试 Broker
transports:
  - name: cloud-mqtt
    type: mqtt
    settings:
      broker: tcp://broker.emqx.io:1883
      client-id: corec-quickstart-01
      qos: 1
      topic-template: "corec/demo/{{.Tag}}"

# 规则：所有数据转发到 MQTT
rules:
  - name: forward-all
    match: "ALL"
    action: forward
    target: cloud-mqtt
    priority: 999
```

### 配置结构解读

| 段落 | 作用 | 必填 |
|:---|:---|:---:|
| `global` | 日志级别、API 监听地址与鉴权密钥 | 是 |
| `drivers` | 南向设备连接与点位定义，**至少一个** | 是 |
| `transports` | 北向数据目的地，**至少一个** | 是 |
| `rules` | 数据路由规则，决定哪些数据发往哪里 | 否* |

::: warning 规则缺省行为
配置校验要求至少配置一个驱动和一个传输，但规则段不是强制的。不过若没有匹配的规则，数据点将不会被推送到任何传输。因此实践中**始终建议配置一条 `match: "ALL"` 的兜底规则**。
:::

## 第二步：启动 CoreC

```bash
./corec -c config.yaml
```

启动成功后会看到如下日志：

```text
INFO registered drivers types=[modbus-tcp modbus-rtu modbus-rtuovertcp modbus-udp modbus-rtuoverudp modbus-tls s7 opcua]
INFO registered transports types=[mqtt http]
INFO CoreC engine starting drivers=1 transports=1 rules=1
INFO mqtt transport initialized name=cloud-mqtt broker=tcp://broker.emqx.io:1883 client-id=corec-quickstart-01
INFO mqtt connected name=cloud-mqtt broker=tcp://broker.emqx.io:1883
INFO transport added name=cloud-mqtt type=mqtt
INFO modbus-tcp driver initialized name=demo-plc host=192.168.1.100 port=502 slave-id=1 tags=1
INFO modbus-tcp driver started name=demo-plc endpoint=192.168.1.100:502
INFO task added id=demo-plc_1s driver=demo-plc interval=1s tags=1
INFO driver added name=demo-plc type=modbus-tcp tags=1
INFO CoreC engine started successfully
INFO CoreC is running config=config.yaml
```

> 引擎先初始化传输（数据消费者），再初始化驱动（数据生产者），因此传输日志先于驱动日志出现。`types=` 列表顺序取决于 `init()` 注册顺序，可能因构建而异。

关键日志含义：

- `registered drivers/transports` —— 所有插件工厂已通过 `init()` 注册完成
- `task added` —— 调度器已为 `demo-plc` 驱动的 1s 周期点位创建采集任务
- `mqtt connected` —— 北向传输已成功连接 Broker

## 第三步：验证数据流

### 方式 A：订阅 MQTT 主题

使用任意 MQTT 客户端（如 `mosquitto_sub`）订阅目标主题：

```bash
mosquitto_sub -h broker.emqx.io -p 1883 -t "corec/demo/#" -v
```

每秒应收到一条 JSON 消息：

```json
corec/demo/temperature {"driver":"demo-plc","device":"","group":"sensors","tag":"temperature","value":42.5,"type":"float32","quality":0,"timestamp":"2024-01-15T10:30:01.234Z"}
```

这就是 CoreC 的标准数据点 `DataPoint` 结构——从设备读出的原始 `TagValue` 经引擎富化后，补充了 `driver`、`group` 等路由元信息。

> `type` 字段序列化为可读字符串（如 `"float32"`），与 YAML 配置中的 `type: float32` 一致。
> 写命令（`/write` API、MQTT command topic）的 `type` 字段同时接受字符串 `"float32"` 和历史整数形式 `9`。

### 方式 B：验证 API

CoreC 启动后会在 `:9090` 暴露控制面 API，使用 Bearer Token 鉴权：

```bash
# 查看引擎整体状态
curl -s -H "Authorization: Bearer corec-secret-token" http://localhost:9090/stats | jq
```

预期响应：

```json
{
  "status": "running",
  "uptime": 15000000000,
  "drivers": 1,
  "transports": 1,
  "rules": 1,
  "total_read": 15,
  "total_publish": 15,
  "total_errors": 0,
  "total_dropped": 0,
  "points_per_sec": 1.0,
  "driver_stats": {
    "demo-plc": {
      "name": "demo-plc",
      "type": "modbus-tcp",
      "state": 2,
      "last_read": "2024-01-15T10:30:15Z",
      "last_error": "",
      "tag_count": 1,
      "read_count": 15,
      "error_count": 0
    }
  },
  "transport_stats": {
    "cloud-mqtt": {
      "name": "cloud-mqtt",
      "type": "mqtt",
      "state": 2,
      "published": 15,
      "failed": 0,
      "received": 0,
      "last_publish": "2024-01-15T10:30:15Z",
      "queue_size": 0
    }
  }
}
```

查看驱动状态：

```bash
curl -s -H "Authorization: Bearer corec-secret-token" \
  http://localhost:9090/drivers/demo-plc | jq
```

```json
{
  "name": "demo-plc",
  "type": "modbus-tcp",
  "state": 2,
  "last_read": "2024-01-15T10:30:15Z",
  "last_error": "",
  "tag_count": 1,
  "read_count": 15,
  "error_count": 0
}
```

### 方式 C：WebSocket 实时数据流

使用 `websocat` 或浏览器连接 WebSocket 端点，实时接收数据点：

```bash
websocat "ws://localhost:9090/tags/stream?token=corec-secret-token"
```

::: info 鉴权方式
API 同时支持两种鉴权写法：
- Header：`Authorization: Bearer <secret>`
- Query：`?token=<secret>`（WebSocket 只能用这种方式，因为浏览器无法自定义 WS Header）
:::

## 第四步：你的第一个数据点

让我们通过 API 即时读取一次温度值（不依赖调度周期）：

```bash
curl -s -H "Authorization: Bearer corec-secret-token" \
  "http://localhost:9090/drivers/demo-plc/tags" | jq
```

返回的是 `LatestCache` 中缓存的该驱动所有点位最新值：

```json
{
  "tags": {
    "temperature": {
      "driver": "demo-plc",
      "tag": "temperature",
      "value": 42.5,
      "type": "float32",
      "quality": 0,
      "timestamp": "2024-01-15T10:30:15.234Z",
      "device": "",
      "group": "sensors"
    }
  }
}
```

::: tip 按驱动查询点位
`GET /drivers/{name}/tags` 返回指定驱动下所有点位的最新缓存值。`GET /tags`（不带路径参数）则返回全部驱动全部点位的最新值——这两个端点都**不接受** `driver` / `tag` 查询参数。
:::

### 反向下发：写入设备

如果该点位是可写的保持寄存器，可以通过 API 下发控制值：

```bash
curl -s -X POST -H "Authorization: Bearer corec-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"driver":"demo-plc","tag":"temperature","value":50.0,"type":"float32"}' \
  http://localhost:9090/write | jq
```

```json
{ "success": true }
```

::: warning 写入语义
`POST /write` 会直接调用驱动的 `Write` 方法将值写入设备寄存器，**绕过规则引擎**。这是即时控制指令，不会进入数据总线。写入后的新值会在下一次调度读取时回流到缓存与北向传输。

`type` 字段接受可读字符串（`"float32"`、`"int16"`、`"bool"` …，与 YAML 配置一致）或历史整数形式（`9` = float32）。推荐使用字符串形式。
:::

## 使用模拟设备验证

如果没有真实 Modbus 设备，可以使用开源的 [diagslave](https://github.com/stephane/diagslave) 或 Python `pymodbus` 模拟器：

```bash
# 使用 pymodbus 启动一个 Modbus TCP 模拟从站
pip install pymodbus
pymodbus simulator --modbus-server tcp --modbus-port 502
```

然后将配置中的 `host` 改为 `127.0.0.1`，即可在本地跑通完整链路。

## 常见问题

### 启动报错 "no data source: ..."

配置中没有数据源。CoreC 要求至少配置一个驱动（`drivers`），或者——纯转发（链式核心）节点——至少有一个传输配置了入站：MQTT 的 `data-topic` 或 HTTP 的 `webhook-addr`，或者启用了拓扑自动发现（`node.id` + `node.subscribe` 非空）。如果三者都没有，引擎没有数据可处理，因此校验会拒绝。

### 启动报错 "unknown driver type: xxx"

`type` 字段拼写错误，或对应的驱动子包未被导入。内置支持的类型：`modbus-tcp`、`modbus-rtu`、`modbus-rtuovertcp`、`modbus-udp`、`modbus-rtuoverudp`、`modbus-tls`、`s7`、`opcua`。

### 设备连接失败但程序未退出

这是**预期行为**。CoreC 采用后台重连策略——初始连接失败不会导致启动失败，而是进入 `connecting` 状态并按指数退避重试。设备恢复后会自动重连并恢复采集。通过 `/drivers/{name}` API 可查看 `state` 字段确认连接状态。

### MQTT 收不到消息

依次排查：

1. Broker 地址是否可达：`mosquitto_sub -h broker.emqx.io -p 1883 -t test` 能否连通
2. 规则是否匹配：检查 `/stats` 中 `total_publish` 是否在增长
3. 主题模板是否正确：`topic-template` 使用 Go text/template 语法，变量为 `&lbrace;&lbrace;.Driver&rbrace;&rbrace;`、`&lbrace;&lbrace;.Group&rbrace;&rbrace;`、`&lbrace;&lbrace;.Tag&rbrace;&rbrace;` 等

## 下一步

- [数据流](./data-flow.md) —— 深入理解 `TagValue → DataPoint` 富化与管道各阶段
- [驱动](./drivers.md) —— 掌握三种驱动的能力差异与点位配置
- [规则引擎](./rules.md) —— 编写条件路由与告警规则
