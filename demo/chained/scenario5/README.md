# 场景五：多对一汇聚

```
CoreC-A ──MQTT(edge/A/...)──▶
CoreC-B ──MQTT(edge/B/...)──▶ CoreC-G ──MQTT(cloud/...)──▶ subscriber
CoreC-C ──MQTT(edge/C/...)──▶
```

三个边缘节点 + 一个汇聚网关。

## 运行

```bash
docker compose up
docker compose logs -f subscriber
```

## 验证点

- A/B/C 分别发布到 `edge/A/...`、`edge/B/...`、`edge/C/...`
- G 订阅 `edge/#` 一次性接收所有节点数据
- subscriber 可看到来自三个不同源的数据交替出现
