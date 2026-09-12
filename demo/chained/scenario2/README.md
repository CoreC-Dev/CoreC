# 场景二：两个 CoreC 级联（边缘 → 云端）

```
PLC ──modbus──▶ CoreC-A ──MQTT(edgeA/...)──▶ CoreC-B ──MQTT(cloud/...)──▶ subscriber
(mock-plc)      (边缘)                        (中继: 告警+转发)            (observe)
```

## 运行

```bash
docker compose up
docker compose logs -f subscriber
```

## 预期输出

subscriber 显示经两级转发后的数据。温度超过 30°C 时 CoreC-B 日志出现 `ALERT`。

## 验证点

- A 用 `topic-template: edgeA/{{.Driver}}/{{.Tag}}` 发布
- B 用 `data-topic: edgeA/#` + `parser: default` 接收
- B 的规则链：高温告警 (priority 1) + 全量转发 (priority 999)
- 控制面：`curl localhost:9090/tags -H "Authorization: Bearer demo-token"`
