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

## 命令下行

B 已配置 `command-forward-topic`，云端命令经 B 重签名透传到 A：向 `cloud/commands/#` 发布用 `COREC_CLOUD_SECRET` 签名的命令，B 校验后用 `COREC_COMMAND_SECRET` 重签名并发布到 `edgeA/commands/#`，A 验签后写入 PLC。

### 测试命令下行（经中继 B）

命令必须携带 HMAC-SHA256 签名（fail-closed，无签名的消息会被丢弃）。签名发布配方见 `docker-compose.yml` 头部注释，完整步骤如下（需 python3 + openssl）：

```bash
# type=9 对应 float32（见 core.DataType 枚举）
export COREC_CLOUD_SECRET=demo-cloud-secret
PAYLOAD=$(python3 -c 'import json; print(json.dumps({"driver":"plc","tag":"temperature","value":42,"type":9}, sort_keys=True, separators=(",",":")))')
SIG=$(printf %s "$PAYLOAD" | openssl dgst -sha256 -hmac "$COREC_CLOUD_SECRET" | cut -d" " -f2)
MSG=$(python3 -c 'import json,sys; d=json.loads(sys.argv[1]); d["X-Signature"]=sys.argv[2]; print(json.dumps(d))' "$PAYLOAD" "$SIG")
docker exec scenario7-broker-1 mosquitto_pub -h broker -t cloud/commands/plc/temperature -m "$MSG"
```

签名格式为 `hex(HMAC-SHA256(secret, canonical-JSON))`，canonical-JSON 是移除签名字段、按键名递归排序后的载荷（见 docs/config/transports.md → 命令认证与防重放）。若要跳过中继直连 A，把主题换成 `edgeA/commands/plc/temperature` 并改用 `COREC_COMMAND_SECRET=demo-command-secret` 计算签名。

mock-plc 日志会显示 `WRITE temperature = 42.00 (holding 10s)`，随后 A 采集到的值变为 42.0 并保持 10 秒。
