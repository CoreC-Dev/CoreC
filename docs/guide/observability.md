---
title: 可观测性
description: Prometheus 指标、健康检查、分布式追踪与 pprof 性能分析 —— CoreC 的四大可观测性支柱
---

# 可观测性（Observability）

CoreC 内置四套可观测性机制，无需任何外部依赖或第三方库：

| 能力 | 端点 | 认证 | 代码位置 |
|:---|:---|:---|:---|
| Prometheus 指标 | `GET /metrics` | 是 | `hub/route/metrics.go` |
| 健康检查 | `GET /healthz/live`、`GET /healthz/ready` | 否 | `hub/route/health.go` |
| 分布式追踪 | 中间件注入，无独立端点 | — | `common/trace/` |
| pprof 性能分析 | `GET /debug/pprof/*` | 是 | `hub/route/server.go` |

::: tip 设计原则
指标数据完全由引擎现有的 `Stats()` 方法生成，不引入 Prometheus 客户端库，零额外依赖。追踪系统无外部依赖，可平滑升级至 OpenTelemetry。
:::

## Prometheus 指标

`GET /metrics` 端点以 Prometheus 文本格式（version 0.0.4）暴露 26 个指标族。该端点位于认证路由组内，抓取方需携带 API secret。

### 全局指标

#### 计数器（Counters）

| 指标 | 说明 | 数据来源 |
|:---|:---|:---|
| `corec_reads_total` | 从驱动读取的数据点总数 | `EngineStats.TotalRead` |
| `corec_publishes_total` | 发布到传输的数据点总数 | `EngineStats.TotalPublish` |
| `corec_errors_total` | 处理错误总数 | `EngineStats.TotalErrors` |
| `corec_dropped_total` | 丢弃的数据点总数 | `EngineStats.TotalDropped` |

#### 仪表盘（Gauges）

| 指标 | 说明 | 数据来源 |
|:---|:---|:---|
| `corec_drivers` | 已配置的驱动数量 | `EngineStats.Drivers` |
| `corec_transports` | 已配置的传输数量 | `EngineStats.Transports` |
| `corec_rules` | 已配置的规则数量 | `EngineStats.Rules` |
| `corec_uptime_seconds` | 引擎运行时长（秒） | `EngineStats.Uptime` |
| `corec_points_per_second` | 当前数据处理速率（点/秒） | `EngineStats.PointsPerSec` |

### 每驱动指标

以下指标按驱动实例名分组，按名称排序输出（确保快照确定性）。

| 指标 | 类型 | 标签 | 说明 |
|:---|:---|:---|:---|
| `corec_driver_read_total` | counter | `driver`, `type` | 该驱动执行的读取次数 |
| `corec_driver_errors_total` | counter | `driver` | 该驱动报告的错误次数 |
| `corec_driver_tags` | gauge | `driver` | 该驱动配置的标签数量 |
| `corec_driver_connected` | gauge | `driver` | 连接状态：`1`=已连接，`0`=未连接 |

示例输出：

```
# HELP corec_driver_read_total Total reads performed by this driver
# TYPE corec_driver_read_total counter
corec_driver_read_total{driver="plc1",type="modbus-tcp"} 15234
corec_driver_read_total{driver="plc2",type="s7"} 8721
# HELP corec_driver_connected 1 if the driver is connected, 0 otherwise
# TYPE corec_driver_connected gauge
corec_driver_connected{driver="plc1"} 1
corec_driver_connected{driver="plc2"} 0
```

### 每传输指标

以下指标按传输实例名分组，按名称排序输出。

| 指标 | 类型 | 标签 | 说明 |
|:---|:---|:---|:---|
| `corec_transport_published_total` | counter | `transport`, `type` | 该传输发布的消息数 |
| `corec_transport_failed_total` | counter | `transport` | 该传输的发布失败数 |
| `corec_transport_received_total` | counter | `transport` | 该传输接收的数据点数（链式核心入站） |
| `corec_transport_queue_size` | gauge | `transport` | 当前出站队列长度 |
| `corec_transport_connected` | gauge | `transport` | 连接状态：`1`=已连接，`0`=未连接 |

### Go 运行时指标

| 指标 | 类型 | 说明 |
|:---|:---|:---|
| `corec_goroutines` | gauge | 运行中的 goroutine 数量 |
| `corec_mem_heap_alloc_bytes` | gauge | 堆内存已分配且仍在使用的字节数 |
| `corec_mem_heap_sys_bytes` | gauge | 从 OS 获取的堆内存字节数 |
| `corec_mem_stack_inuse_bytes` | gauge | 栈内存使用字节数 |
| `corec_mem_total_alloc_bytes` | gauge | 累计分配的内存字节数（含已释放） |
| `corec_gc_count` | gauge | GC 完成次数 |
| `corec_gc_pause_total_seconds` | gauge | GC 累计暂停时间（秒） |
| `corec_cpu_count` | gauge | 进程可用的逻辑 CPU 数 |

::: warning 运行时指标类型
`corec_mem_total_alloc_bytes`、`corec_gc_count` 和 `corec_gc_pause_total_seconds` 在语义上是累计值（单调递增），但代码中按 gauge 类型暴露（非 counter），因为它们直接读取 `runtime.MemStats` 的累计字段，未经过 Prometheus counter 的递增封装。
:::

### 抓取配置示例

```yaml
# prometheus.yml
scrape_configs:
  - job_name: corec
    scrape_interval: 15s
    metrics_path: /metrics
    bearer_token: corec-secret-token  # 与 api.secret 一致
    static_configs:
      - targets: ['localhost:9090']
```

### 常用 PromQL 查询

```promql
# 数据吞吐率（点/秒）
rate(corec_reads_total[1m])

# 丢弃率告警阈值
rate(corec_dropped_total[5m]) > 1

# 驱动错误率
sum by (driver) (rate(corec_driver_errors_total[5m]))

# 传输发布成功率
1 - (rate(corec_transport_failed_total[5m]) / rate(corec_transport_published_total[5m]))

# 所有未连接的驱动
corec_driver_connected == 0

# goroutine 泄漏检测
corec_goroutines > 500
```

## 健康检查

两个健康检查端点注册在认证路由组**之外**，Kubernetes 探针无需携带 API secret 即可访问。

### 存活探针 — `GET /healthz/live`

```http
GET /healthz/live
```

**响应**（始终 `200 OK`）：

```json
{"status":"alive"}
```

存活探针仅检查进程是否在运行并能响应 HTTP 请求。它**不依赖**任何外部状态（驱动、传输、broker 连接），因为短暂的下游中断不应触发进程重启。

::: tip Kubernetes 配置
```yaml
livenessProbe:
  httpGet:
    path: /healthz/live
    port: 9090
  initialDelaySeconds: 5
  periodSeconds: 10
```
:::

### 就绪探针 — `GET /healthz/ready`

```http
GET /healthz/ready
```

**就绪判定逻辑**（全部满足才返回 `200`）：

1. 引擎状态为 `running`（非 `stopped` / `suspended`）
2. 若配置了驱动，至少一个驱动处于 `connected` 状态（`ConnState = 2`）
3. 若配置了传输，至少一个传输处于 `connected` 状态（`ConnState = 2`）

未配置驱动或传输时，对应检查空真满足（vacuously satisfied）。

**就绪响应**（`200 OK`）：

```json
{"status":"ready"}
```

**未就绪响应**（`503 Service Unavailable`）：

```json
{"status":"not_ready"}
```

引擎未初始化时：

```json
{"status":"not_ready","reason":"engine not initialized"}
```

::: tip Kubernetes 配置
```yaml
readinessProbe:
  httpGet:
    path: /healthz/ready
    port: 9090
  initialDelaySeconds: 10
  periodSeconds: 5
  failureThreshold: 3
```
:::

## 分布式追踪

CoreC 实现了轻量级分布式追踪，位于 `common/trace/` 包，无外部依赖。

### W3C Trace Context 传播

追踪中间件（`trace.Middleware`）在 HTTP 请求进入时注入 trace ID，传播策略如下：

1. **优先解析 `traceparent` 头** — 格式 `version-trace_id-parent_id-trace_flags`（W3C 标准）。若存在且合法，复用其 trace-id，使本服务日志与上游调用方的追踪关联
2. **回退至 chi RequestID** — 若无 `traceparent`，使用 chi 中间件生成的请求 ID
3. **最终回退** — 生成新的 W3C 合规 trace ID（16 字节 hex，32 字符）

`traceparent` 头校验规则：
- version 必须为 `00`
- trace-id 必须为 32 位小写 hex
- 全零 trace-id 视为非法（W3C 规范要求）

### Span API

```go
// 启动一个 span
ctx, span := trace.Start(ctx, "modbus.Read")
defer span.End()

// 添加属性
span.SetAttr("address", "192.168.1.10:502")
span.SetAttr("tag_count", 42)
```

`Span.End()` 在 debug 日志级别记录 span 名称、trace ID、持续时间和所有属性。多次调用 `End()` 是幂等的（no-op）。

### 出站传播

向下游服务传播追踪上下文：

```go
// 在发起 HTTP 请求前注入 traceparent 头
trace.InjectTraceparent(ctx, req)
// 生成格式: 00-{traceID}-{newSpanID}-01
```

若 context 中无 trace ID，则不设置头（不凭空创建追踪上下文）。

### Trace ID 生成

`NewTraceID()` 使用 `crypto/rand` 生成 16 字节随机数，hex 编码为 32 字符。若 `crypto/rand` 失败，回退为时间戳编码。

`NewSpanID()` 同理生成 8 字节（16 hex 字符）的 span ID。

::: info 升级路径
追踪系统设计为可平滑升级至 OpenTelemetry。当前 trace ID 格式已兼容 W3C Trace Context，未来可通过替换 `common/trace/` 内部实现接入 OTLP exporter，无需修改调用方代码。
:::

## pprof 性能分析

pprof 端点提供 Go 运行时性能分析能力，适用于生产环境按需诊断。

### 配置

| 配置项 | 类型 | 默认值 | 说明 |
|:---|:---|:---|:---|
| `api.pprof-disabled` | bool | `false` | 设为 `true` 完全禁用 pprof |
| `api.pprof-addr` | string | 空 | pprof 独立端口地址（如 `127.0.0.1:6060`） |

**三种运行模式：**

| `pprof-disabled` | `pprof-addr` | 行为 |
|:---|:---|:---|
| `false`（默认） | 空 | pprof 注册在主 API 端口 `/debug/pprof/*`，需认证 |
| `false`（默认） | 非空（如 `127.0.0.1:6060`） | pprof 运行在独立端口，**无需认证**，应绑定回环地址 |
| `true` | 任意 | pprof 完全禁用 |

::: warning 安全建议
当使用独立端口模式（`pprof-addr` 非空）时，pprof 端点不要求 API secret。务必绑定到回环地址（`127.0.0.1`）或私有网络接口，避免暴露到公网。
:::

### 可用端点

| 端点 | 说明 |
|:---|:---|
| `/debug/pprof/` | 概览页，列出所有可用分析类型 |
| `/debug/pprof/cmdline` | 进程命令行参数 |
| `/debug/pprof/profile` | CPU profile（`?seconds=30` 采样 30 秒） |
| `/debug/pprof/symbol` | 符号表查询 |
| `/debug/pprof/trace` | 执行追踪（`?seconds=5` 采样 5 秒） |

此外，`go tool pprof` 可直接分析堆分配和 goroutine 状态：

```bash
# CPU profile（30 秒采样）
go tool pprof http://localhost:9090/debug/pprof/profile?seconds=30

# 堆分配
go tool pprof http://localhost:9090/debug/pprof/heap

# goroutine 状态
go tool pprof http://localhost:9090/debug/pprof/goroutine

# 阻塞 profile（需先启用 runtime.SetBlockProfileRate）
go tool pprof http://localhost:9090/debug/pprof/block
```

::: tip 独立端口模式
当 `pprof-addr: "127.0.0.1:6060"` 时，上述命令中的端口替换为 `6060`，且无需携带认证 token：

```bash
go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=30
```
:::

## 与 API 统计的关系

`GET /stats` 端点（详见 [统计监控](../api/stats)）返回 JSON 格式的引擎统计，与 Prometheus 指标共享同一数据源（`EngineStats`）。两者区别：

| 维度 | `GET /stats`（JSON） | `GET /metrics`（Prometheus） |
|:---|:---|:---|
| 格式 | JSON | Prometheus 文本 0.0.4 |
| 认证 | 是 | 是 |
| 指标数量 | 引擎 + 驱动 + 传输统计 | 26 个指标族（含 Go 运行时） |
| 用途 | 一次性查询、Dashboard 展示 | 持续抓取、告警、长期存储 |
| 运行时指标 | 无 | 有（goroutine、内存、GC、CPU） |

## 中间件链中的追踪

追踪中间件在请求处理链中的位置（详见 [API 总览](../api/overview)）：

```
RequestID → Trace → SafeRequestLogger → Recoverer → CORS → RateLimit → Authentication
```

Trace 中间件位于第 2 步，紧接 RequestID 之后，确保所有后续中间件和处理器的日志都能关联到 trace ID。
