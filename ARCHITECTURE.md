# ARCHITECTURE.md

> CoreC 领域与包分层地图。依赖方向规则是**不可违反的约束**，由阶段 3 结构测试 + depguard 机械强制。

## 架构风格

**六边形可插拔（Ports & Adapters）**。核心定义接口缝隙（`core.Driver`、`core.Transport`、`core.Rule`），驱动/传输/规则作为适配器实现这些接口，通过 `init()` 工厂注册到全局 registry。入口 `cmd/corec` 做 wiring：加载配置 → 构建引擎 → 启动 hub → 空导入触发插件注册。

## 数据流

```
南向设备                    核心引擎                     北向传输
┌─────────┐            ┌──────────────┐            ┌─────────┐
│ Modbus  │──Read──→   │  Scheduler   │            │  MQTT   │
│ OPC UA  │──Read──→   │  ↓           │  ─Push──→  │  HTTP   │
│ S7      │──Read──→   │  Rules       │  ─Push──→  │(webhook)│
│         │←─Write──   │  ↓           │            │         │
│         │            │  Transport   │            │         │
└─────────┘            └──────────────┘            └─────────┘
       ↑                                                ↑
       └────────── 控制指令（反向流）──────────────────┘
```

- **正向流**：Driver.Read → DataBus → Rule 变换 → Batcher → Transport.Publish
- **反向流**：Transport 收到命令 → HMAC 校验 → CommandManager → Driver.Write → PLC

## 包分层与依赖方向

```
Layer 0 (接口层):    core
Layer 1 (适配层):    driver/{modbus,opcua,s7}   transport/{mqtt,httppush,parser}   rule
Layer 2 (编排层):    engine   hub/{route,executor}
Layer 3 (支撑层):    config   common   log
Layer 4 (入口层):    cmd/corec
```

### 依赖方向规则（允许边表）

| 包 | 允许依赖（→） | 禁止 |
|---|---|---|
| `core` | （无，纯接口/类型） | 依赖任何业务包 |
| `driver/*` | `core`, `common` | 依赖 `engine`/`transport`/`hub`/`config`/其他 `driver/*` |
| `transport/*` | `core`, `common` | 依赖 `engine`/`driver`/`hub`/`config`/其他 `transport/*` |
| `rule` | `core`, `common` | 依赖 `engine`/`driver`/`transport`/`hub` |
| `engine` | `core`, `driver`(接口), `transport`(接口), `rule`, `config`, `common` | 依赖 `hub`/`cmd` |
| `hub/*` | `core`, `engine`, `config`, `common` | 依赖 `driver`/`transport`/`rule`（仅通过 engine 间接） |
| `config` | `core`(仅类型), `common` | 依赖 `engine`/`driver`/`transport`/`hub` |
| `common` | （无，纯工具） | 依赖任何业务包 |
| `log` | `core`, `common/observable` | 依赖 `engine`/`driver`/`transport`/`hub`/`config` |
| `cmd/corec` | 所有（wiring 点） | — |

**跨域禁止**：`driver/modbus` 不可依赖 `driver/opcua` 或 `driver/s7`；`transport/mqtt` 不可依赖 `transport/httppush`。

### 当前状态（Phase 1 扫描验证）

- ✅ 0 环依赖
- ✅ 0 跨层违规
- ✅ 0 跨域依赖
- ✅ ARCH-001：结构测试已建立（archtest/ 机械强制依赖方向）
- ✅ ARCH-002：config.validate 已消除全局单例耦合

## 插件注册机制

```
cmd/corec/main.go
  import (
      _ "github.com/.../driver/all"     // 触发 driver/modbus, opcua, s7 的 init()
      _ "github.com/.../transport/all"   // 触发 transport/mqtt, httppush 的 init()
  )
```

每个驱动/传输包的 `init()` 调用 `core.RegisterDriver()` / `core.RegisterTransport()` 注册工厂函数。`all` 包仅做空导入聚合。新增插件须在对应 `all` 包添加空导入。

## 核心接口缝隙

| 接口 | 方法数 | 职责 |
|------|--------|------|
| `core.Driver` | 11 | 南向设备读写：Connect/Close/Read/Write/Health/Reconnect 等 |
| `core.Transport` | 10 | 北向推送：Init/Start/Stop/Publish/PublishBatch/HandleCommand 等 |
| `core.Rule` | 11 | 规则求值：Match/Apply/Compile/Continue 等 |
| `core.RuleEngine` | 9 | 规则集管理：AddRule/RemoveRule/MatchAll 等 |

接口方法数均 ≤ 12（合理领域抽象，非 God Interface）。

## 关停顺序

```
signal.Notify(SIGINT, SIGTERM) → hub.Stop() → engine.Stop()
  → cancel()（级联取消所有子 ctx）
  → discovery.Stop() → scheduler.Stop()（wg 屏障：生产者先于消费者静默）
  → stopComponents():
      drivers（逐个带超时）
      排空 dataBus（滞留点位计数 + Warn 日志后丢弃）
      batchers（带超时；flushFinal 余量发布）
      transports（逐个带超时）
      dataBus.Close() → ruleProviders → tagWatchers
  → wg.Wait()（收集 processingLoop/listener goroutine）
```

- **batchers 必须先于 transports 停止**：batcher 的 `flushFinal` 余量发布需要存活的 transport；若顺序颠倒（先停 transports），flush 会打在已断开的连接上而全部失败。此前本节文档写的顺序（transports → batchers）与代码不符且照做会引入数据丢失——以代码顺序为准。
- **第二个 SIGINT/SIGTERM 强制退出**：优雅关停进行中再次收到信号时进程立即 `os.Exit(1)`，卡住的关停无需 `kill -9`。
- **关停数据语义**：已进入 Batcher 的数据保证被 flush（发布或落盘）；仍滞留在 DataBus 通道、未被处理管线消费的点位在关停时被丢弃（有 Warn 日志与 error 计数，不落盘）。详见 `docs/architecture/dataflow.md` 的交付语义一节。

## 已知架构债务

详见 `docs/exec-plans/tech-debt-tracker.md`。架构相关条目：
- CPLX-020（P1）：CoreCEngine God Object → 延期至阶段5后
