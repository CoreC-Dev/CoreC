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

`GET /metrics` 端点以 Prometheus 文本格式（version 0.0.4）暴露指标族。该端点位于认证路由组内，抓取方需携带 API secret。

### 全局指标

#### 计数器（Counters）

| 指标 | 说明 | 数据来源 |
|:---|:---|:---|
| `corec_reads_total` | 从驱动读取的数据点总数 | `EngineStats.TotalRead` |
| `corec_publishes_total` | 发布到传输的数据点总数 | `EngineStats.TotalPublish` |
| `corec_errors_total` | 处理错误总数 | `EngineStats.TotalErrors` |
| `corec_dropped_total` | 丢弃的数据点总数 | `EngineStats.TotalDropped` |
| `corec_log_dropped_total` | 因消费者过慢而丢弃的日志事件数 | `log.Dropped()` |

#### 仪表盘（Gauges）

| 指标 | 说明 | 数据来源 |
|:---|:---|:---|
| `corec_drivers` | 已配置的驱动数量 | `EngineStats.Drivers` |
| `corec_transports` | 已配置的传输数量 | `EngineStats.Transports` |
| `corec_rules` | 已配置的规则数量 | `EngineStats.Rules` |
| `corec_uptime_seconds` | 引擎运行时长（秒） | `EngineStats.Uptime` |
| `corec_points_per_second` | 近 60 秒滑动窗口的数据处理速率（点/秒）；采集停止超过 1 分钟后归零（窗口内速率，非启动以来均值） | `EngineStats.PointsPerSec` |

### 丢弃归因指标（audit OBS-6）

`corec_dropped_total` 是四类丢弃的总和（历史兼容保留）。以下按因拆分的族让"到底丢在哪一层"可以直接回答与告警：

| 指标 | 类型 | 标签 | 说明 |
|:---|:---|:---|:---|
| `corec_databus_dropped_total` | counter | `channel` | 内部数据总线满载逐出的点数：`main`（普通通道）/ `priority`（快间隔高优通道） |
| `corec_slow_subscriber_dropped_total` | counter | — | `/tags/stream` 订阅者消费过慢被丢的点数 |
| `corec_bad_quality_dropped_total` | counter | — | `on-bad-quality: drop` 策略丢弃的点数（配置行为，**不再**计入 `corec_errors_total`） |

```promql
# 任一丢弃原因出现都值得知道
corec_databus_dropped_total > 0 or corec_slow_subscriber_dropped_total > 0 or corec_bad_quality_dropped_total > 0
```

### 每驱动指标

以下指标按驱动实例名分组，按名称排序输出（确保快照确定性）。

| 指标 | 类型 | 标签 | 说明 |
|:---|:---|:---|:---|
| `corec_driver_read_total` | counter | `driver`, `type` | 该驱动执行的读取次数 |
| `corec_driver_errors_total` | counter | `driver` | 该驱动报告的错误次数 |
| `corec_driver_reconnect_total` | counter | `driver` | 该驱动的重连尝试次数 |
| `corec_driver_tags` | gauge | `driver` | 该驱动配置的标签数量 |
| `corec_driver_connected` | gauge | `driver` | 连接状态：`1`=已连接，`0`=未连接 |
| `corec_driver_last_read_timestamp_seconds` | gauge | `driver` | 最近一次成功读取的 Unix 时间戳（秒）；首次读取前不输出该序列 |
| `corec_driver_last_read_seconds` | gauge | `driver` | 距最近一次成功读取的秒数——采集停更告警的基础（如 `> 2 × 采集周期` 判定驱动静默）；首次读取前不输出该序列 |

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
# HELP corec_driver_last_read_seconds Seconds since the driver's last successful read; use for staleness alerting (e.g. > 2x collection interval means the driver went quiet); omitted before the first read
# TYPE corec_driver_last_read_seconds gauge
corec_driver_last_read_seconds{driver="plc1"} 1.2
corec_driver_last_read_seconds{driver="plc2"} 748.9
```

### 调度器健康指标

调度器的运行状态（自动降级、连续读失败、读超限）此前只出现在节流日志里，无法用于告警。以下指标按驱动分组（聚合该驱动全部采集任务）：

| 指标 | 类型 | 标签 | 说明 |
|:---|:---|:---|:---|
| `corec_scheduler_degraded` | gauge | `driver` | `1`=该驱动任一采集任务处于自动降级（连续读失败后拉长间隔），`0`=正常 |
| `corec_scheduler_consecutive_errors` | gauge | `driver` | 该驱动采集任务的最差连续读失败次数（首次成功即归零） |
| `corec_scheduler_overruns_total` | counter | `driver` | 该驱动"读取耗时超过采集周期"的累计次数（进程生命周期内单调递增，不清零） |

### 驱动自报指标（audit OBS-8）

驱动通过 `core.Metrics` 端口上报的协议级指标经引擎注册表汇聚后，按其**原生命名**（驱动侧命名空间，约定以协议为前缀：`modbus_*`、`opcua_*`、`s7_*`）原样暴露：

| 指标 | 类型 | 标签 | 说明 |
|:---|:---|:---|:---|
| `modbus_exception_total` | counter | `driver`, `code` | Modbus 异常响应计数（如 `illegal-data-address`——通常是地址配置错误；`server-device-busy`——设备过载）。传输层错误（超时/拒绝）不计入——那属于连接层，由重连计数覆盖 |

### 每传输指标

以下指标按传输实例名分组，按名称排序输出。

| 指标 | 类型 | 标签 | 说明 |
|:---|:---|:---|:---|
| `corec_transport_published_total` | counter | `transport`, `type` | 该传输发布的消息数 |
| `corec_transport_failed_total` | counter | `transport` | 该传输的发布失败数 |
| `corec_transport_received_total` | counter | `transport` | 该传输接收的数据点数（链式核心入站） |
| `corec_transport_queue_size` | gauge | `transport` | 当前出站队列长度 |
| `corec_transport_connected` | gauge | `transport` | 连接状态：`1`=已连接，`0`=未连接 |

### 离线缓冲指标

当配置了离线缓冲（`buffer.enabled: true`）时，以下指标反映缓冲区的积压与重放状态：

| 指标 | 类型 | 说明 |
|:---|:---|:---|
| `corec_offline_buffer_pending` | gauge | 当前等待重放的缓冲区批次数 |
| `corec_offline_buffer_drained_total` | counter | 成功重放的批次总数 |
| `corec_offline_buffer_pushed_total` | counter | 重试耗尽后写入缓冲区的批次总数 |
| `corec_offline_buffer_evicted_total` | counter | 缓冲区满时被逐出的最旧批次总数（新数据优先） |
| `corec_offline_buffer_bytes` | gauge | 缓冲区占用的磁盘字节数——批次计数回答"积压多少批"，字节数回答"断网吃掉多少磁盘"（告警建议按磁盘容量设阈值） |

### 批处理背压指标

| 指标 | 类型 | 说明 |
|:---|:---|:---|
| `corec_flush_batches_dropped_total` | counter | 异步 flush 队列满载时被逐出的整批批次数（持续增长即传输端持续消费不动） |

### 死信队列指标

写指令的死信队列（`GET /write/failed`）的监控面：

| 指标 | 类型 | 说明 |
|:---|:---|:---|
| `corec_dead_letter_queue_size` | gauge | 当前列队中的死信指令数——持续增长即"写路径在持续失败" |
| `corec_dead_letter_pushed_total` | counter | 启动以来累计进入死信队列的指令数（队列有界驱逐不减少该计数） |

入站丢弃（命令通道满）见 `corec_transport_dropped_commands_total`（每传输）与 `GET /write/failed?source=ingress`（丢弃明细）。

### 告警指标

一等告警注册表（`GET /alerts`）的监控面（详见 [告警 API](../api/alerts.md)）：

| 指标 | 类型 | 标签 | 说明 |
|:---|:---|:---|:---|
| `corec_alerts_total` | counter | `rule` | 每条规则的累计触发次数（原始计数）；`rate()` 反映触发频率 |
| `corec_alerts_active` | gauge | — | 当前活动告警数（触发中且未静默清除，含已确认） |

### HTTP 请求指标

`httpMetricsMiddleware` 中间件自动记录所有 HTTP 请求（包括 `/healthz` 探针）：

| 指标 | 类型 | 标签 | 说明 |
|:---|:---|:---|:---|
| `corec_http_requests_total` | counter | `method`, `status` | 按方法和状态码分组的请求总数 |
| `corec_http_request_duration_seconds` | histogram | — | HTTP 请求耗时分布（桶：1ms/5ms/10ms/50ms/100ms/500ms/1s/5s） |

### 延迟直方图指标

引擎自动测量驱动读取和传输发布的耗时，以 Prometheus 直方图格式暴露。**三个直方图族都带维度标签**：读延迟按 `driver`、发布延迟与数据年龄按 `transport` 分序列——单个慢驱动/慢传输不再被健康同伴的平均值稀释（观测审计 OBS-2）。聚合查询用 `sum by (le)`：

| 指标 | 类型 | 标签 | 说明 |
|:---|:---|:---|:---|
| `corec_read_latency_seconds` | histogram | `driver` | 该驱动的读取延迟分布（桶：1ms/5ms/10ms/50ms/100ms/500ms/1s/5s/10s）；至少完成一次读取的驱动才有序列 |
| `corec_publish_latency_seconds` | histogram | `transport` | 该传输的发布延迟分布（同上桶配置） |
| `corec_data_age_seconds` | histogram | `transport` | 该传输的数据年龄分布——采集到发布的真实延迟，含批量/重试/离线回放（桶：0.1s~600s） |

```promql
# 单个驱动的读延迟 P99（标签化后可直接按驱动拆分）
histogram_quantile(0.99, rate(corec_read_latency_seconds_bucket{driver="plc1"}[5m]))

# 全局聚合读延迟 P99（跨驱动合并）
histogram_quantile(0.99, sum by (le) (rate(corec_read_latency_seconds_bucket[5m])))

# 找出最慢的传输（按中位发布延迟排序）
histogram_quantile(0.5, sum by (transport, le) (rate(corec_publish_latency_seconds_bucket[5m])))
```

### Go 运行时指标

| 指标 | 类型 | 说明 |
|:---|:---|:---|
| `corec_goroutines` | gauge | 运行中的 goroutine 数量 |
| `corec_mem_heap_alloc_bytes` | gauge | 堆内存已分配且仍在使用的字节数 |
| `corec_mem_heap_sys_bytes` | gauge | 从 OS 获取的堆内存字节数 |
| `corec_mem_stack_inuse_bytes` | gauge | 栈内存使用字节数 |
| `corec_mem_total_alloc_bytes` | counter | 累计分配的内存字节数（含已释放，单调递增） |
| `corec_gc_count` | counter | GC 完成次数 |
| `corec_gc_pause_total_seconds` | counter | GC 累计暂停时间（秒） |
| `corec_cpu_count` | gauge | 进程可用的逻辑 CPU 数 |


### 抓取配置示例

::: code-group

```yaml [默认：随主 API 认证抓取]
# prometheus.yml — /metrics 在主 API 端口上，位于 Bearer 认证组内
scrape_configs:
  - job_name: corec
    scrape_interval: 15s
    metrics_path: /metrics
    bearer_token: corec-secret-token  # 与 api.secret 一致
    static_configs:
      - targets: ['localhost:9090']
```

```yaml [生产推荐：专用免密指标接口]
# CoreC 侧：global.api.metrics-addr: "127.0.0.1:9109"（仅回环）
# Prometheus 侧：无需 token —— 抓取面拿不到能 PUT /configs 的主 API secret
scrape_configs:
  - job_name: corec
    scrape_interval: 15s
    static_configs:
      - targets: ['localhost:9109']
```

:::

配置 `global.api.metrics-addr` 后：`/metrics` **从主 API 撤下**，只在专用端口上免认证提供（该端口除 `/metrics` 外无任何路由）。因无认证，**必须绑定回环地址**（与 pprof-addr 同样的 fail-closed 强制）；Prometheus 与 CoreC 同机或经 SSH 隧道/私有网桥时用此模式。audit OBS-12：此前每台 Prometheus 都要拿到能改配置的主 API secret。

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

# 驱动重连频率（频繁重连说明连接不稳定）
rate(corec_driver_reconnect_total[5m]) > 0.1

# 读取延迟 P99（跨驱动聚合；单驱动查询见"延迟直方图指标"节）
histogram_quantile(0.99, sum by (le) (rate(corec_read_latency_seconds_bucket[5m])))

# 离线缓冲积压告警
corec_offline_buffer_pending > 100

# 驱动采集停更告警（距上次成功读取超过 60 秒即驱动静默）
corec_driver_last_read_seconds > 60

# 调度器降级告警（驱动因连续读失败被自动降频采集）
corec_scheduler_degraded == 1

# 日志丢弃告警（日志在静默丢失）
rate(corec_log_dropped_total[5m]) > 0

# HTTP 5xx 错误率
sum(rate(corec_http_requests_total{status=~"5.."}[5m])) / sum(rate(corec_http_requests_total[5m])) > 0.01
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
{
  "status": "not_ready",
  "reason": "no connected drivers",
  "components": {
    "drivers": [
      {"name": "plc1", "connected": false},
      {"name": "plc2", "connected": true}
    ],
    "transports": [
      {"name": "mqtt1", "connected": false}
    ]
  }
}
```

`components` 字段列出每个驱动和传输的连接状态，便于直接从探针响应定位故障组件，无需额外查询 `/stats`。

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

## 日志格式配置

CoreC 使用 `log/slog` 结构化日志，支持两种输出格式：

| 格式 | 配置值 | 适用场景 |
|:---|:---|:---|
| Text（默认） | `log-format: text` | 开发环境，人类可读 |
| JSON | `log-format: json` | 生产环境，ELK/Loki 等日志聚合系统 |

```yaml
global:
  log-level: info
  log-format: json  # 切换为 JSON 格式以适配日志采集系统
```

日志同时通过 WebSocket 流（`GET /logs`）实时推送，便于 Dashboard 展示。当日志通道满或订阅者缓冲区溢出时，事件会被静默丢弃并计入 `corec_log_dropped_total` 指标。
