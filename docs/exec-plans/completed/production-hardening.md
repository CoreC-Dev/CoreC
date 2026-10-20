# 执行计划：生产级加固（audit-backend-functionality P0/P1 落地）

> 状态：**全部完成**（2026-10-20）：P0+P1（12/12）+ P2（17/17，TRA-1 复核为误报）+ P3（#31–#39 全部落地；#40 并入 #5 已随死信可观测实现）。#39 docs 残留同步见 `8a848f9`（端点索引/指标清单与代码机械对账）。
> 来源：`docs/audits/backend-functionality-audit-2026-10-19.md`（2026-10-19 后端功能完整性审计，40 项路线图）
> 业务行为影响：**变更行为**（写路径反演、PUT /configs 错误码、指标新增/删除——每项单独登记，见各行）
> 跨仓库：CoreC-Dashboard 需同步的项标注 [FE]；文档需同步的项标注 [DOC]

## 进度（2026-10-20 更新）

| # | 项 | 状态 | Commit |
|---|---|---|---|
| 1 | 写路径 scale/offset 反演（P0） | ✅ | `2d2b9c1`（含 ApplyTransform 全类型覆盖修复；FE 类型同步 `dcb0f03`） |
| 2 | points_per_second 60s 窗口速率 | ✅ | `299251d` |
| 3 | corec_driver_last_read_* 新鲜度 gauge | ✅ | `8e1ba83` |
| 4 | 延迟直方图 driver/transport 标签 | ✅ | `51dd697` |
| 5 | 死信可观测 + /write/failed?source=ingress | ✅ | `f4b3f30`（FE 类型同步 `3180b32`） |
| 6 | 告警一等公民化（/alerts + ack + stream + 指标 + 前端接入） | ✅ | `7766c6e`（FE 同步 `a17dfbc`） |
| 7 | 调度器健康指标（降级/连续错误/超限累计） | ✅ | `58b2da3` |
| 8 | derived-tags 热应用 + node/buffer 重启警告 | ✅ | `6685acb` |
| 9 | reload 失败自动回滚 | ✅ | `d063afd` |
| 10 | PUT /configs 400 语义修复 | ✅ | `6ff8ccd` |
| 11 | OPC UA 订阅质量映射 | ✅ | `5c0ad6c` |

审计文档基线注册：`58f7d6e`。

## P2 批次进度（2026-10-20）

| 路线图# | 项 | 状态 | Commit |
|---|---|---|---|
| 18 | 限流键按 host 分桶（防连接轮换绕过） | ✅ | `c80140c` |
| 15 | 规则求值错误可见（eval_errors 统计 + preview 透出） | ✅ | `a801a76`（FE `112045e`） |
| 16 | 缓存逐出：删驱动/删 tag 清幽灵值 | ✅ | `4f62dc0` |
| 21 | 驱动 pause/resume 端点 | ✅ | `2d29981`（FE `0751461`） |
| 13 | 删除 statistic 双轨计数包 | ✅ | `08f90b9` |
| 19 | webhook fail-open | 复核为误报（守卫+测试 aee3946 已在） | `24bc9d2`（审计更正） |
| 12 | 丢弃归因：per-cause 族 + bad-quality 不再计入 errors | OBS-6 | `ad68303` |
| 14 | core.Metrics 端口端到端接通 + modbus 异常码指标 | OBS-8 | `1c3b996` |
| 20 | MQTT 遗嘱消息（LWT） | TRA-2 | `3175fbd` |
| 24 | /configs/raw 材料化陷阱文档化 | CFG-5 | `a75e219` |
| 28 | offline buffer 字节水位 gauge | 路线图#28 | `a124349` |
| 17 | 派生标签 `driver.tag` 限定引用 | PIPE-2 | `201e2d0` |
| 22 | GET /configs/audit 配置变更审计 | HUB-4 | `8c36395`（FE `4f93757`） |
| 23 | PATCH 扩面（log-format / stale-threshold） | CFG-6 | `3b5e399`（FE `ba135d3`） |
| 30 | discovery HTTP 分支处置：标注 experimental + 可见跳过 | RES-3 | `48d9a27` |
| 29 | 死信落盘选项（dead-letter-path） | RES-2 | `a989e9c` |
| 27 | 专用免密 metrics 端口（metrics-addr） | OBS-12 | `bfa1577` |
| 25 | settings 键名校验（typo 拒绝 + 漂移测试） | CFG-4 | `005cfdc` |
| 26 | 资源级 DELETE 端点（drivers/transports/rules） | HUB-3 | `ed64736`（FE `02cc612`） |

## P3 批次进度（2026-10-20）

| 路线图# | 项 | 状态 | Commit |
|---|---|---|---|
| 31 | runtime 累计指标改 counter TYPE | OBS-10 | `92c7aef` |
| 32 | /traffic 改推速率（服务端差分，mode=totals 兼容） | OBS-11 | `92c7aef`（FE `62aaa5a`） |
| 33 | mirror 语义澄清（文档对齐实现） | RULE-1 | `92c7aef` |
| 34 | 规则环境加 timestamp | RULE-3 | `92c7aef` |
| 36 | webhook 批量响应逐项计数 | TRA-3 | `92c7aef` |
| 38 | 派生标签编译期循环检测 | PIPE-4 | `92c7aef` |
| 35 | /tags 与 /write/history 服务端过滤 | HUB-5 | `2bcdd02`（FE `6f63879`） |
| 37 | POST /write/batch 批量写（顺序执行、逐条结果） | RES-5 | `f6b4a24`（FE `eb03a17`） |
| 39 | docs 残留同步（端点索引对账/指标清单/README 宣称/关停顺序） | 10.2 | `8a848f9` |
| 40 | （并入 #5：ingress 死信计数与明细已随 `f4b3f30` 落地） | TRA-4 | `f4b3f30` |

## 1. 目标

按审计推荐方案落地 P0+P1 全部 11 项，使后端达到"可告警、可归因、写值正确"的生产水位；前端与文档随行为变更同步。P2/P3 项在 P0/P1 收口后另开计划。

## 2. 范围与顺序（P0 → P1）

| # | 项 | 来源 | 行为影响 | 验收 |
|---|---|---|---|---|
| 1 | **写路径 scale/offset 反演**：三驱动写编码前应用 `(value-offset)/scale`；`WriteCommand.Raw` 显式逃生口；审计记录 Raw | DRV-1 (P0) | 变更行为：带 scale 的 tag 写入值从"原样下发"变为"反演后下发"（原本即错） | 带 scale tag 写工程值→设备收到正确原始值；`raw:true` 跳过反演；三驱动测试 |
| 2 | `corec_points_per_second` 语义修复（改为 60s 滑动窗口速率） | OBS-1 | 变更行为：指标从"启动均值"变为"近 60s 速率" | 断流 1 分钟内指标→0；[DOC] observability.md 同步 |
| 3 | `corec_driver_last_read_seconds`（年龄 gauge）| OBS-5 | 无（新增） | /metrics 含 per-driver last_read 年龄；[DOC] 同步 |
| 4 | 延迟直方图加 driver/transport 标签（保留全局聚合族） | OBS-2 | 无（新增带标签族） | `corec_read_latency_seconds_bucket{driver=...}` 可查 |
| 5 | 死信可观测：queue_size gauge + pushed_total；MQTT 入站死信并入 `/write/failed?source=ingress` | OBS-3/RES-1/TRA-4 | 无（新增指标+查询参数） | 死信堆积可告警；入站丢弃可查；[FE] DLQ 视图加 source 维度 |
| 6 | **告警一等公民化**：engine 内建 alertManager（active/ack/history 去重冷却）+ `GET /alerts` + `WS /alerts/stream` + `corec_alerts_total{rule}` | OBS-4/HUB-6 | 变更行为：新增 API 面（旧 /logs 流不动） | 告警有状态/历史/流；[FE] AlertsPage 接入；[DOC] API 参考 |
| 7 | 调度器状态指标：degraded / overruns_total（不清零）/ consecutive_errors | OBS-5 | 无（新增） | 降级发生时可在 /metrics 看到 |
| 8 | derived-tags 热应用；node/buffer 进 restart-required 警告 | CFG-1 | 变更行为：reload 改派生标签即时生效 | 改派生标签+PUT 后实际生效 |
| 9 | reload 失败善后：diff 失败时按 oldCfg 回滚已删组件 | CFG-2 | 变更行为：失败后引擎与 /configs/raw 一致 | 注入失败 reload→引擎组件集不变 |
| 10 | PUT /configs 错误分类：parse/validate→400 透传；apply 失败→500 掩码 | CFG-3/HUB-2 | 变更行为：错误码语义 | 非法配置 PUT 返回 400+原因；[FE] 错误展示无需改（原样展示 error） |
| 11 | OPC UA 订阅质量映射：读 `item.Value.Status` → Quality | DRV-2 | 变换行为：订阅 Bad 值不再以 Good 入管线 | 订阅回调按 StatusCode 置 Quality |

## 3. 设计要点（关键决策，B11 登记）

- **反演数学**：`raw = (value - offset) / scale`，镜像 `util.ApplyTransform` 的归一化：`scale==0 && offset==0` 视为未配置（原样透传）；`scale==0 && offset!=0` 读侧等价 `+offset`，写侧反演为 `-offset`。非数值（bool/string/bytes）读侧不变换，写侧同样透传。helper 落位 `driverbase.WriteValueForTag`（单一事实源，三驱动共用），数学原语 `util.InvertTransform`。
- **滑动窗口速率**：statsCollector 增加 60s 环形分桶（每秒一个桶，原子操作），`PointsPerSec` 取窗口内总数/窗口时长；uptime<窗口时退化为总数/uptime。语义即文档宣称的"当前速率"。
- **告警状态模型**：不引入"恢复表达式"（超出范围）；active 定义为"未 ack 且最近 `alert-active-window`（默认 15m）内再触发"；ack 经 `PATCH /alerts/{key}/ack`；自动 clear 超窗未再触发。去重窗口内合并计数（复用 notify 的 cooldown 思路，引擎层实现）。历史 ring 封顶 500。`/alerts/stream` 推送事件（fired/ack/cleared）。
- **错误分类**：executor 返回 `*ConfigError`（parse/validate 类，客户端可修复）vs 其他（内部）；route 层 `errors.As` 判定 400/500。
- **回滚**：`ApplyConfig` 在 diff 前记录被删组件名，任一 diff 失败→按 oldCfg 逐一 re-Add（best-effort，日志记录失败项）。

## 4. 同步义务

- [DOC] README API 表、docs/guide/observability.md 指标表、docs/api/* 新端点、docs/config/drivers.md scale 写语义。
- [FE] #5 DLQ source 维度、#6 AlertsPage 接 `/alerts`+`/alerts/stream`、#2 Dashboard 若消费 points_per_sec 需确认语义。
- 提交粒度：每项一个 commit，行为变更在 message 显式登记（R2）。

## 5. 验收基线

- `go build ./...`、`go vet ./...`、`go test -short ./...` 全绿；关键路径补 race（`go test -race` 定向跑改动包）。
- Dashboard `npm run build` + `npm test` 全绿。
