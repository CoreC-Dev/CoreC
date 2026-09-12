# 场景七：双向级联（数据上行 + 命令下行）

```
PLC ◀──modbus──▶ CoreC-A ◀──MQTT──▶ CoreC-B ◀──MQTT──▶ subscriber
(mock)    write     (edge)    data↑   (relay)   data↑    (observe)
          downlink           cmd↓              cmd↓
```

## 运行

```bash
docker compose up
docker compose logs -f subscriber
```

## 数据上行（完全可用）

A 采集 PLC → 发布 `edgeA/...` → B 订阅转发到 `cloud/...` → subscriber 可见。

## 命令下行（限制说明）

⚠️ **命令经中继 B 透传到 A 当前不支持**（引擎已知限制）。
B 收到 `cloud/commands/#` 的命令时只会调用自身本地驱动（B 是中继，`drivers: []`），不会重发布到 `edgeA/commands/#`。

### 测试命令下行（直连 A）

跳过中继，直接向 A 的 command-topic 发布命令：

```bash
# 通过 broker 容器发送写命令
# type=9 对应 float32（见 core.DataType 枚举）
docker exec -it $(docker compose ps -q broker) mosquitto_pub -h localhost \
  -t 'edgeA/commands/plc/temperature' \
  -m '{"driver":"plc","tag":"temperature","value":42.0,"type":9}'
```

mock-plc 日志会显示 `WRITE temperature = 42.00 (holding 10s)`，随后 A 采集到的值变为 42.0 并保持 10 秒。
