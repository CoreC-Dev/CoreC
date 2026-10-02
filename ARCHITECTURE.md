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
| `log` | （标准库） | 依赖任何业务包 |
| `cmd/corec` | 所有（wiring 点） | — |

**跨域禁止**：`driver/modbus` 不可依赖 `driver/opcua` 或 `driver/s7`；`transport/mqtt` 不可依赖 `transport/httppush`。

### 当前状态（Phase 1 扫描验证）

- ✅ 0 环依赖
- ✅ 0 跨层违规
- ✅ 0 跨域依赖
- ⚠️ ARCH-001：无机械约束（depguard/结构测试），依赖方向正确但无护栏防退化 → Phase 3 建立
- ⚠️ ARCH-002：`config.validate` 耦合 `core.globalRegistry` 全局单例 → Phase 4 批次 2 修复

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
| `core.Rule` | 10 | 规则求值：Match/Apply/Compile 等 |
| `core.RuleEngine` | 9 | 规则集管理：AddRule/RemoveRule/MatchAll 等 |

接口方法数均 ≤ 12（合理领域抽象，非 God Interface）。

## 关停顺序

```
signal.Notify(SIGINT, SIGTERM) → hub.Stop() → engine.Stop()
  → cancel()（级联取消所有子 ctx）
  → discovery.Stop() → scheduler.Stop()
  → stopComponents():
      drivers/transports（带超时 time.After）
      batchers（PERF-008: 当前无超时 → Phase 4 修复）
      dataBus.Close() → ruleProviders → tagWatchers
  → wg.Wait()（收集 processingLoop/listener goroutine）
```

## 已知架构债务

详见 `docs/exec-plans/tech-debt-tracker.md`。架构相关条目：
- ARCH-001（P1）：无 depguard/结构测试 → Phase 3
- ARCH-002（P2）：config 耦合全局单例 → Phase 4 批次 2
- CPLX-020/021/022（P1）：CoreCEngine/MQTTTransport/OPCUADriver God Object → Phase 4 批次 3/7/5
