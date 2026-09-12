# 场景六：一对多分发

```
              ┌──MQTT(cloud/...)──▶ subscriber
PLC ──▶ CoreC ┤
(mock)        └──HTTP──▶ http-sink
```

## 运行

```bash
docker compose up
# 两个终端分别看
docker compose logs -f subscriber
docker compose logs -f http-sink
```

## 验证点

- 单个 CoreC 用 `action: mirror` + `targets: [cloud-mqtt, mes-http]`
- 同一数据点同时出现在 MQTT subscriber 和 http-sink 日志中
