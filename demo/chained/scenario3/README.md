# 场景三：多级级联（边缘 → 网关 → 云端）

```
PLC ──▶ CoreC-A ──MQTT──▶ CoreC-B ──HTTP──▶ CoreC-C ──MQTT──▶ subscriber
(mock)   (车间)            (厂区网关)         (区域中心)         (observe)
```

三跳：modbus → MQTT → HTTP → MQTT。

## 运行

```bash
docker compose up
docker compose logs -f subscriber
```

## 验证点

- A: modbus 采集 → MQTT `edgeA/...`
- B: MQTT `edgeA/#` 收 → HTTP POST 到 `corec-c:9091/ingest`
- C: HTTP webhook `:9091/ingest` 收 → MQTT `cloud/...`
- 每一跳都可加规则过滤/变换
