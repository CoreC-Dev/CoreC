# 场景四：协议转换（MQTT → HTTP）

```
PLC ──▶ CoreC-A ──MQTT(factory/...)──▶ CoreC-B ──HTTP──▶ http-sink
(mock)   (source)                       (MQTT→HTTP桥)      (MES/ERP模拟)
```

## 运行

```bash
docker compose up
# 看 HTTP 接收端
docker compose logs -f http-sink
```

## 验证点

- A: modbus → MQTT `factory/...`
- B: MQTT `factory/#` 收 → HTTP POST 到 `http-sink:8080/telemetry`
- http-sink 打印收到的 JSON body
