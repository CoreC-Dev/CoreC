# 场景 8：自动发现两级级联

与 scenario2 拓扑相同，但使用**拓扑自动发现**替代显式 topic 配置。

## 拓扑

```
PLC ──modbus──▶ CoreC-A ──MQTT(自动)──▶ CoreC-B ──MQTT(cloud/...)──▶ 订阅者
(mock-plc)      (采集器)                (中继)                        (观察端)
```

## 与场景 2 的区别

| 字段 | 场景 2（手动） | 场景 8（自动发现） |
|:---|:---|:---|
| A 的 `topic-template` | `"edgeA/{{.Driver}}/{{.Tag}}"` | **省略** → 自动生成 `"topo/edge-A/data/..."` |
| B 的 `data-topic` | `"edgeA/#"` | **省略** → 从 A 的心跳自动发现 |
| B 的 `parser` | `{ type: default }` | **省略** → 自动设为 `default` |
| A 的 `rules` | `forward-all → mqtt` | **省略** → 自动添加 |
| B 的 `rules` | `forward → mqtt-out` | 显式配置（业务逻辑） |
| B 的 `topic-template` | `"cloud/{{.Driver}}/{{.Tag}}"` | 显式配置（订阅者期望此格式） |

## 自动发现工作原理

1. 两个节点在 broker 上向 `corec/_discovery/{node-id}` 广播心跳
2. A 的心跳声明："我是 edge-A，发布到 `topo/edge-A/data/#`"
3. B 的心跳声明："我是 relay-B，订阅 `edge-A`"
4. B 收到 A 的心跳 → 自动创建 transport，设 `data-topic: "topo/edge-A/data/#"`
5. 数据流：A → broker → B → broker → 订阅者

## 核心原则：显式优先，省略自动填

- 你写的字段原样使用（如 B 的 `topic-template: "cloud/..."`）
- 你省略的字段自动生成
- 没有 `node:` 段的现有配置完全不受影响

## 运行

```bash
docker compose up --build
```

订阅者应看到：
```
cloud/plc/temperature {"driver":"plc","name":"temperature","value":25.5,...}
cloud/plc/humidity    {"driver":"plc","name":"humidity","value":60,...}
```
