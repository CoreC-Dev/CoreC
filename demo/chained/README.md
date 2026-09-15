# Chained Core 可运行 Demo

将 [链式核心文档](../../docs/guide/chained-core.md) 中的场景做成可运行的多容器部署。
每个场景一条 `docker compose up` 即可看到数据在链上真实流动。

## 前置条件

- Docker + Docker Compose v2+
- 首次运行会自动构建镜像（corec、mock-plc、http-sink、lora-sim），约 1-2 分钟

## 快速开始

```bash
# 进入某个场景目录
cd scenario2

# 启动（首次会构建镜像）
docker compose up

# 看数据流（另一个终端）
docker compose logs -f subscriber

# 停止
docker compose down
```

## 场景总览

| 场景 | 目录 | 拓扑 | 容器数 |
|:---|:---|:---|:---:|
| ① 纯订阅转发 | `scenario1/` | lora-sim → CoreC → subscriber | 4 |
| ② 两级级联 | `scenario2/` | PLC → CoreC-A → CoreC-B → subscriber | 5 |
| ③ 多级级联 | `scenario3/` | PLC → A → B → C → subscriber | 6 |
| ④ 协议转换 | `scenario4/` | PLC → A → B → http-sink | 5 |
| ⑤ 多对一汇聚 | `scenario5/` | A+B+C → G → subscriber | 9 |
| ⑥ 一对多分发 | `scenario6/` | PLC → CoreC → MQTT + HTTP | 5 |
| ⑦ 双向级联 | `scenario7/` | PLC ↔ A ↔ B → subscriber | 5 |
| ⑧ 自动发现 | `scenario8/` | PLC → CoreC-A → CoreC-B → subscriber | 5 |

## 共享组件

| 组件 | 说明 |
|:---|:---|
| `corec.Dockerfile` | 多阶段构建 CoreC 二进制（从 repo 根） |
| `mock-plc/` | 模拟 Modbus TCP PLC，温度正弦变化 + 计数器自增 |
| `http-sink/` | 极小 HTTP 服务，收到的 body 打印到日志 |
| `lora-sim/` | 模拟 LoRa 网关，发布第三方格式 JSON 到 MQTT |

## 场景⑦说明

场景⑦的**数据上行**（A→B→云端）完全可用。
**命令下行经中继透传**（云端→B→A）是引擎已知限制——B 无本地驱动，不会重发布命令。
如需测试命令下行，直接向 A 的 `command-topic` 发布命令即可（见 scenario7/README.md）。
