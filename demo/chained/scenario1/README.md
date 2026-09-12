# 场景一：纯订阅转发（协议网关）

```
lora-sim ──MQTT(lora/+/up)──▶ CoreC ──MQTT(cloud/...)──▶ subscriber
(3rd-party JSON)               (jsonpath parser)           (observe)
```

## 运行

```bash
docker compose up
# 看数据流
docker compose logs -f subscriber
```

## 预期输出

subscriber 日志显示 CoreC 将第三方 LoRa JSON 解析后以标准格式发布到 `cloud/`：
```
cloud/lora/sensor-01 {"driver":"lora","tag":"sensor-01","value":23.4,...}
```

## 验证点

- CoreC 无驱动（`drivers: []`），数据全部来自 `data-topic`
- `parser: jsonpath` 将 `{dev_id, temp}` 映射为 DataPoint
- lora-sim 每 2 秒发布一次非标准 JSON
