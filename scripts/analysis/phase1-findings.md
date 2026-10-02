# Phase 1 扫描发现汇总（scratch，供综合用）

> 七路并行扫描的原始发现累积于此。综合后迁入 docs/exec-plans/tech-debt-tracker.md 与 harness-migration.md §3/§4，本文件可保留为分析留痕。

## 已完成：ARCH（架构 / 子任务3）

**摘要**：CoreC 结构良好的六边形/插件注册架构。core 为纯 ports/types 真叶子；core.Driver/core.Transport 是插件缝；适配器 init() 注册经 driver/all + transport/all blank import 触发。实测 0 循环依赖、0 越层违规、0 跨域直连。映射：core=Types、config=Config、driver+transport=Repo/适配器、engine+rule=Service、hub=Runtime、cmd=UI/entry、common/*+log=横切。

**测量数据**：主模块 Go 包数 24；直接内部 import 边 51；循环 0；越层违规 0；跨域直连 0；import 具体适配器的包仅 4 个且合规（driver/all、transport/all、cmd/corec、demo/chained/validate）；import 方向机械强制缺失（无 depguard、无结构测试）。

**允许边表（供 ARCH-001 落地 depguard/结构测试规约）**：
- core → ∅；common/* → ∅；engine/statistic → ∅；e2e → ∅
- config → {core}；rule → {core}；log → {common/observable, core}
- driver/{modbus,opcua,s7} → {common/util, core}
- transport/parser → {common/util, core}
- transport/httppush → {common/trace, common/util, core, transport/parser}
- transport/mqtt → {common/util, core, transport/parser}
- driver/all → {driver/modbus, driver/opcua, driver/s7}
- transport/all → {transport/httppush, transport/mqtt}
- engine → {common/metrics, common/trace, common/util, core, engine/statistic, log, rule}
- hub/route → {common/trace, core, log}
- hub/executor → {config, core, hub/route, log}
- hub → {core, hub/executor, hub/route}
- cmd/corec → {config, core, driver/all, engine, hub, log, transport/all}
- demo/chained/validate → {config, driver/all, transport/all}
- 禁止代表性边：core→任何具体适配器；engine/rule/hub/hub.*/config/log→任何 driver/*或transport/*具体包；config→engine/hub；rule→engine/hub；engine→hub

**发现条目**：
| ID | 标题 | 位置 | 类别 | 严重度 | 证据 | 修复建议 | 业务行为影响 | 验收方式 |
|---|---|---|---|---|---|---|---|---|
| ARCH-001 | 分层依赖方向不变量(§4.1/T7)未机械强制 | `.golangci.yml`(全仓)；无结构测试文件 | 架构 | P1 | .golangci.yml 无 depguard；全仓无 import-graph 结构测试。当前图干净(0循环/0越层/0跨域)，不变量仅靠人工审查维持——未来 engine 或 hub/route 加入 import ".../driver/modbus" 将静默通过 CI 引发架构漂移 | 启用 depguard 按允许边表配白名单；或新增 import-graph 结构测试。错误信息内嵌修复指令 | 无 | 新增禁止边(如 engine→driver/modbus)时 depguard/结构测试失败并报可修复错误；当前允许边表全部通过 |
| ARCH-002 | Config 层校验耦合全局适配器注册表(内层依赖外层注册副作用) | config/config.go:240,244,313,391 | 架构 | P2 | config.validate 调用 core.RegisteredDrivers()/RegisteredTransports() 读取 core.globalRegistry(core/registry.go:14-19 包级可变单例)，该表由外层适配器 init() 填充。静态 import 边 config→core 合法，但语义上 Config 校验结果依赖 Repo 层适配器是否已 import 并 init()——倒置，import 顺序依赖，脱离适配器注册无法独立单测 | 将"已注册类型名集合"作为参数注入 Validate(纯函数化)；或显式文档化+结构测试断言 config 仅 import core | 无 | config.Validate 在不触发任何适配器 init() 的独立单测中，以显式传入注册类型集合正确判定 |

## 已完成：DUP（重复 / 子任务5）

**摘要**：共享 util ReconnectLoopWithBreakerCounted 已被三驱动正确复用(T6 在重连循环本身已落地)，但外层包装方法(Start/Stop/Restart/reconnectLoop/startReconnectLoop/handleConnectionLost/Status)在三驱动逐行复制~176行；reconnect_count_test.go 三份近乎全等(~350行重复)；TLS 测试证书生成助手三处各写一份；engine/publish.go numericValue 是 util.ToFloat64 变体副本；modbus tcp.go 与 net.go Init 近乎全等；AddDriver 与 reloadDriverTags 共享流程各写一份。所有重复为纯重构(无)，未发现已发散为 bug。

**测量数据**：估算重复行数约 740。DUP-001(~176)+DUP-002(~350)=~526 占 71%。common/util/reconnect_breaker+reconnect_counted 已被三驱动复用(T6 核心已落地)，重复在外层包装方法和测试。

**发现条目**：
| ID | 标题 | 位置 | 类别 | 严重度 | 证据 | 修复建议 | 业务行为影响 | 验收方式 |
|---|---|---|---|---|---|---|---|---|
| DUP-001 | 驱动生命周期方法在 modbus/opcua/s7 三处逐行复制 | driver/modbus/modbus_base.go:232-296,885-926; driver/opcua/client.go:160-177,380-428,613-662; driver/s7/s7.go:161-248,453-477,711-727 | 重复 | P1 | Start/Stop/Restart/Status/handleConnectionLost/reconnectLoop/startReconnectLoop 三处同构 | 抽取 common/util 或 driver 内嵌基类的生命周期骨架，驱动只注入 connect/close 闭包+type 字符串 | 无 | 三处共用同一生命周期骨架，结构测试断言无重复实现 |
| DUP-002 | reconnect_count_test.go 在三驱动近乎全等 | driver/modbus/reconnect_count_test.go:1-217; driver/opcua/reconnect_count_test.go:1-135; driver/s7/reconnect_count_test.go:1-145 | 重复 | P1 | TestXxxReconnectCount* 三处同构，仅构造方式和端点不同 | 抽取 common/util/driver_lifecycle_test.go 表驱动测试助手 | 无 | 三处共用一个表驱动测试助手 |
| DUP-003 | TLS 测试证书生成助手三处各写一份 | driver/modbus/tls_test.go:25-62; transport/mqtt/tls_test.go:25-63; transport/httppush/tls_test.go:28-68; driver/modbus/tls_test_helper.go:13-33 | 重复 | P2 | 三处均 ecdsa.GenerateKey→x509.CreateCertificate→Marshal→pem→WriteFile，模板字段相同 | 抽取 common/testutil/cert.go 共享 GenerateSelfSignedCertFiles | 无 | 三处共用一个 cert 生成助手 |
| DUP-004 | 重连/熔断配置解析在三驱动 Init 逐行复制 | driver/modbus/modbus_base.go:163-165; driver/opcua/client.go:135-137; driver/s7/s7.go:135-137 | 重复 | P2 | 三处相同三行 reconnectBackoff/maxReconnectBackoff/maxReconnectFailures 解析+标签解析循环同构 | 抽取 common/util ParseReconnectSettings 助手 | 无 | 三处调用同一解析助手 |
| DUP-005 | engine/publish.go numericValue 是 util.ToFloat64 变体副本 | engine/publish.go:43-71; common/util/util.go:99-133 | 重复 | P2 | 两者 switch v.(type) 遍历数值类型返回 float64，13 case 中 11 逐字相同 | 用 util.ToFloat64 或抽取 util.NumericToFloat64(v)(float64,bool) | 无 | publish.go 调用 util 共享助手 |
| DUP-006 | modbus tcp.go Init 与 net.go Init 近乎全等 | driver/modbus/tcp.go:37-66; driver/modbus/net.go:62-90 | 重复 | P2 | diff 仅差接收者类型/错误前缀/日志消息，主体逐行相同 | 合并 initNetDriver(d,settings,config,defaultPort) 助手 | 无 | tcp/net 共用同一 Init 助手 |
| DUP-007 | modbus/s7 内联 time.ParseDuration，opcua 用 util.GetDurationSetting | driver/modbus/modbus_base.go:155-158; driver/s7/s7.go:126-129; driver/opcua/client.go:132 | 重复 | P2 | modbus/s7 各写内联解析，opcua 一行 util.GetDurationSetting | modbus/s7 改用 util.GetDurationSetting | 无 | 三处统一调用 util.GetDurationSetting |
| DUP-008 | stopComponents 中驱动停止循环与传输停止循环同构 | engine/engine.go:514-525,534-545 | 重复 | P2 | 两段均为 for+go+select+time.After 停止模式，仅变量名/日志标签不同 | 抽取 stopWithTimeout(name,stopFn,timeout) 助手 | 无 | 两处调用同一 stopWithTimeout 助手 |
| DUP-009 | AddDriver 与 reloadDriverTags 共享 create→init→start→swap→stop-old 流程各写一份 | engine/driver_manager.go:30-108,221-278 | 重复 | P2 | 两者均 core.CreateDriver→Init→Start→锁内删旧→锁外 Stop 旧→锁内安装新+tagGroups→scheduleDriverTags；reloadDriverTags 注释自述"mirrors AddDriver" | 抽取 createInitStartDriver/swapDriver 助手 | 无 | 两处共用助手 |

## 已完成：TEST（测试 / 子任务7）

**摘要**：整体覆盖率 80.1%（go test -short -cover 合并 profile，全包通过无 FAIL）。21 可测包中 14 个 ≥80%，但 cmd/corec 24.6%、driver/opcua 56.0%、transport/parser 66.2%、engine 71.9%、transport/mqtt 77.3%。最严重 engine/discovery.go 拓扑自动发现运行时 11 函数全 0.0%（discovery_test.go 342 行只测纯辅助函数，未启动真实 Discovery 心跳/协调 goroutine）。goleak 真正启用（TestMain+VerifyTestMain 在 rule/hub/route/transport/mqtt/engine，含合理 IgnoreAnyFunction 豁免）；e2e 真实集成（6 场景，无 build tag/t.Skip/环境依赖）；*_bug_test.go/*_fix_test.go 均有实质断言的回归测试。注：实际 *_test.go 文件数 85（非 79）。

**测量数据**：整体覆盖率 80.1%。逐包：cmd/corec 24.6%, common/metrics 100%, common/observable 95.1%, common/trace 93.3%, common/util 96.9%, config 86.9%, core 100%, demo/chained/validate 0%, driver/modbus 77.8%, driver/opcua 56.0%, driver/s7 83.4%, engine 71.9%, engine/statistic 100%, hub 96.4%, hub/executor 82.9%, hub/route 92.7%, log 79.2%, rule 85.5%, transport/httppush 91.3%, transport/mqtt 77.3%, transport/parser 66.2%。

**发现条目**：
| ID | 标题 | 位置 | 类别 | 严重度 | 证据 | 修复建议 | 业务行为影响 | 验收方式 |
|---|---|---|---|---|---|---|---|---|
| TEST-001 | 拓扑自动发现运行时零测试：11 函数全 0.0% | engine/discovery.go:91-345 | 测试 | P0 | go tool cover -func 显示 discovery.go 全部 11 函数 0.0%；discovery_test.go:1-342 仅测纯辅助(topicTemplateToSubscriptionPattern/nodeInfo/autoFillNodeConfig/sanitizeBroker)，未启动任何 Discovery 实例。含心跳 goroutine+MQTT 收发+reconcile 协调循环，并发+网络高风险 | 用 mock MQTT broker/接口注入为 Discovery 补 Start/Stop 生命周期、心跳发布、onDiscoveryMessage 解析、reconcile 节点增删、goroutine 不泄漏测试 | 无 | 覆盖率 ≥80% 且注入缺陷(如 reconcile 漏删离线节点)使测试失败 |
| TEST-002 | MQTT Publish() 核心发布路径仅 8.3% | transport/mqtt/publisher.go:983 | 测试 | P1 | Publish 8.3%、subscribeCommands 0%、subscribeData 0%、evictExpired 0%；publisher_methods_test.go:14-16 注释明言"None require a running MQTT broker" | 用 in-process MQTT broker 补 Publish 成功/失败/QoS、订阅命令与数据回调、去重驱逐过期测试 | 无 | Publish 覆盖率 ≥80% 且断开 broker 使 Publish 返回错误且测试失败 |
| TEST-003 | OPC UA 订阅模式与连接丢失重连全 0% | driver/opcua/client.go:300,644,179,430 | 测试 | P1 | driver/opcua 56.0%；startSubscription/subscriptionLoop/handleConnectionLost 均 0%。订阅是 OPC UA 主流采集模式，重连是可靠性关键路径 | 用 OPC UA 测试服务器补订阅建立、数据回调、连接断开后自动重连与订阅恢复测试 | 无 | 覆盖率 ≥70% 且模拟连接断开后断言重连成功且订阅恢复 |
| TEST-004 | Modbus 连接丢失处理与单标签读/重试欠测 | driver/modbus/modbus_base.go:911,437,642; net.go:62; rtu.go:89; tls.go:108 | 测试 | P1 | driver/modbus 77.8%；handleConnectionLost 0%、readTag 10.5%、三种 connect 0% | 用 mock Modbus server(e2e 已有 modbusHandler 可复用)补连接断开→重连、readTag 各类型错误、readBatchWithRetry 重试耗尽测试 | 无 | handleConnectionLost/readTag 覆盖率 ≥80% 且注入不可达 slave 使重连测试失败 |
| TEST-005 | engine Start 半途失败回滚路径(rollbackStart)零测试 | engine/engine.go:444-478 | 测试 | P1 | rollbackStart 0.0%。所有 engine 测试均 eng.Start 后期望成功，无一注入中途失败触发回滚。该路径防 goroutine 泄漏与半启动态 | 注入 Start 后期失败的 driver/transport 断言 rollbackStart 清理已启动组件、无 goroutine 泄漏、Status=Stopped | 无 | 注入中途失败后断言已启动 driver/transport 被 Stop 且 goleak 通过 |
| TEST-006 | cmd/corec 入口覆盖率 24.6%，main() 0% / run() 18% | cmd/corec/main.go:29,33-111 | 测试 | P1 | main 0%、run 18%；main_test.go 覆盖 setupLogging 与 config 缺失/非法，但 run() 的 engine.Start 失败返回 1、hub 生命周期、信号处理分支未覆盖；TestRunValidConfigWithSignal 跑子进程但忽略退出码 | 补 run() 各返回码分支测试(engine.Start 失败→1、SIGTERM→0)，强化信号测试断言退出码 | 无 | run() 覆盖率 ≥80% 且注入 engine.Start 失败使 run() 返回 1 的测试失败 |
| TEST-007 | transport/parser 入站解析边界欠测 66.2% | transport/parser/parser.go:311,287,273,128 | 测试 | P1 | 整包 66.2%；parseScalar 23.8%、parseTimestamp 33.3%、parseTplValueString 0%。标量/时间戳/模板解析是第三方入站数据边界(T4/T5 关键点) | 补 parseScalar 各类型/非法值、parseTimestamp 多格式与非法、parseTplValueString 模板缺失字段测试 | 无 | 覆盖率 ≥85% 且注入非法标量使 parseScalar 返回 error 且测试失败 |
| TEST-008 | log 包 ParseLevel 与 logger_adapter 零测试 | log/level.go:21; log/logger_adapter.go:17-35; log/log.go:46,79,162 | 测试 | P2 | log 79.2%；ParseLevel 0%、logger_adapter 全 0、buffer 订阅与 WithAttrs/WithGroup 0 | 补 ParseLevel 合法/非法、adapter 各级别转发、InitBuffer/SubscribeWithBuffer、WithAttrs/WithGroup 测试 | 无 | 覆盖率 ≥90% 且注入非法 level 使 ParseLevel 返回 ok=false |
| TEST-009 | demo/chained/validate 无测试 0.0% | demo/chained/validate/main.go | 测试 | P2 | 覆盖率 0%；演示程序 main，非生产代码 | 演示程序可不补；若作为校验工具复用则补表驱动测试。登记不丢 | 无 | (可选)覆盖率 >0% |
| TEST-010 | hub/route 离线缓冲指标与部分配置端点欠测 | hub/route/metrics.go:202,174; configs.go:70,189 | 测试 | P2 | hub/route 92.7% 整体高，但离线缓冲 Prometheus 指标 writeOfflineBufferMetrics 12.5%、Flush 0%、getConfigsRaw/patchConfigs 50% | 补 writeOfflineBufferMetrics 各计数器、getConfigsRaw/patchConfigs 鉴权与错误响应测试 | 无 | 上述函数覆盖率 ≥85% |
| TEST-011 | engine cache.GetAll 与 DataBus.Push 丢弃路径欠测 | engine/cache.go:134; engine/databus.go:72,122 | 测试 | P2 | Push/PushHighPriority 57.9%(丢弃/背压分支未覆盖)，GetAll 0% | 补满 bus 丢弃计数、GetAll 快照、高优先级背压测试 | 无 | 覆盖率 ≥90% 且满 bus 时断言 Dropped 递增 |

## 已完成：SEC（安全 / 子任务8）

**摘要**：安全基线整体强于同类 IIoT 项目。管理 API 在 api.secret 空时 fail-closed 拒绝启动(server.go:190-201)，认证 SHA256+hmac.Equal 常量时间比较，token 查询参数在访问日志中脱敏；config ${ENV} 替换 parse→expand→re-marshal 防 YAML 注入且不记录明文，secrets.go Redact()+MergeSentiels() 保证 GET /configs/raw 永不回显明文；MQTT 命令转发在 command-secret 设置时强制 HMAC-SHA256+时间戳新鲜度+有界重放缓存；TLS(mqtt/httppush/modbus)均 MinVersion TLS1.2+mTLS+ServerName 校验，生产代码 crypto/tls.InsecureSkipVerify 出现 0 次；expr-lang/expr 求值环境固定 DataPoint 字段 map，未注册危险 expr.Function，DisableBuiltin("type")，非 RCE 向量；无 os/exec/plugin.Open/unsafe.Pointer。无 P0。

**测量数据**：生产 crypto/tls.InsecureSkipVerify=0（2 处出现：hub/route/common.go:34 WS Origin 检查跳过非 TLS 证书校验、transport/httppush/tls_test.go:181 仅测试）；硬编码密钥候选(生产 Go)=0；demo 配置明文 "demo-token" 16 文件；依赖均为近期版本无可在精确版本上利用的已知 CVE（gorilla/websocket v1.5.3 历史 CVE-2020-27828 已修复）。

**发现条目**：
| ID | 标题 | 位置 | 类别 | 严重度 | 证据 | 修复建议 | 业务行为影响 | 验收方式 |
|---|---|---|---|---|---|---|---|---|
| SEC-001 | OPC UA 驱动默认 SecurityPolicy#None + Anonymous 认证且无告警 | driver/opcua/client.go:184-194 | 安全 | P1 | L184 if d.securityPolicy != "" 才追加 SecurityPolicy，空则默认 None(无加密/无签名)；L190-193 username 空时显式 AuthAnonymous。全仓无对此默认的校验告警(对比 MQTT/webhook 均有 Warn) | 启动时若 security-policy 或 username 为空则 slog.Warn(对齐 publisher.go:487/push.go:201)。仅加告警=修复bug；改默认为安全策略=变更行为需人工决策 | 修复bug(告警)/变更行为(改默认) | grep 确认 Init 路径有告警；config 校验测试断言空 security-policy 时产生 warning |
| SEC-002 | MQTT 命令转发默认无认证(command-secret 空=接受未签名控制命令) | transport/mqtt/publisher.go:646(门控)+:487-488(告警) | 安全 | P1 | L646 if t.commandSecret != "" 才校验 HMAC；空时 handleCommandMessage 直接放行未签名命令到 commandCh→PLC 写入。L487-488 已 Warn 但不阻止。command_auth_test.go:123 固化此向后兼容行为 | 选项A(修复bug)：文档/config.example 强调 command-topic 必配 command-secret；选项B(变更行为)：config 校验在 command-topic 非空且 command-secret 空时 fail-closed 拒绝启动。B 破坏现有无密部署需人工决策 | 变更行为(若 fail-closed)/修复bug(仅文档+告警强化) | 现有 command_auth_test.go 覆盖签名校验；新增结构测试断言 fail-closed(若采纳 B) |
| SEC-003 | HTTP webhook 默认无认证(webhook-secret 空=接受任意 POST 数据) | transport/httppush/push.go:404(门控)+:201-203(告警) | 安全 | P1 | L404 if t.webhookSecret != "" 才校验 Authorization/X-Webhook-Secret(HMAC 常量时间)；空时 handleWebhook 接受任意 POST→数据投毒。L201-203 已 Warn | 同 SEC-002：文档强化(修复bug)或 webhook-addr 非空时强制 webhook-secret(变更行为需人工决策) | 变更行为(若强制)/修复bug(文档) | webhook_auth_test.go 已覆盖有密路径；新增测试断言无密告警/拒绝 |
| SEC-004 | WebSocket 握手在 permissive 模式跳过 Origin 校验 | hub/route/common.go:34 | 安全 | P2 | L32-34 if len(wsAllowedOrigins)==0 { opts.InsecureSkipVerify = true }。此为 coder/websocket AcceptOptions.InsecureSkipVerify(跳过 Origin 检查非 TLS 证书校验)。WS 端点仍在 authentication(secret) 组内，auth 是 query token 非 cookie CSRF 不可行，风险低 | 生产部署文档要求设置 allowed-origins；或默认仅允许 localhost。改默认会破坏 dashboard :3080→:9090 跨域，属变更行为 | 变更行为 | grep 确认非空 origins 时不设 InsecureSkipVerify；route_test 断言 OriginPatterns 生效 |
| SEC-005 | Demo 配置内置已知共享密钥 "demo-token" 且绑定 0.0.0.0 | demo/chained/scenario5/corec-g.yaml:5(及 scenario1-8 共 16 文件) | 安全 | P2 | api: { listen: "0.0.0.0:9090", secret: "demo-token" }。若运维直接拷 demo 配置上生产且未改 secret，API 密钥为公开已知值 | demo 改用 ${COREC_API_SECRET} 环境变量；或启动时对已知弱值 "demo-token" 在非 demo 模式告警。仅改 demo=无行为影响 | 无 | grep demo 确认无明文 "demo-token" 或改为 ${...}；启动弱密检查测试 |
| SEC-006 | pprof 独立端口(PprofAddr)无认证 | hub/route/server.go:317-342 | 安全 | P2 | L337 log.Infoln "pprof server listening (no auth)"；pMux 无 auth 中间件暴露 goroutine/heap/profile。默认 pprof 在主 server authenticated 组内，此为 PprofAddr 显式设置时 opt-in 路径 | 对独立 pprof server 加 authentication(secret)，或校验 PprofAddr 为 loopback 拒绝非回环。加 auth 可能破坏外部抓取=变更行为 | 变更行为(加 auth)/无(仅 loopback 校验) | 测试断言非 loopback PprofAddr 被拒绝或要求认证 |

## 已完成：DOC（文档 / 子任务6）

**摘要**：config.example.yaml 字段/类型与 core.Config 基本一致，但有一处主动误导：示例声称 env 替换是"byte-level (pre-YAML-parse)"，而 config.go 实际为 tree-based(parse→expand→re-marshal)防 YAML 注入。四份根级分析文档均为 AI 生成一次性快照，与 docs/ 站点及未来 ARCHITECTURE.md/QUALITY_SCORE.md/tech-debt-tracker.md 职责重叠；IMPROVEMENTS.md 含不可能的行号引用(engine.go:1244 等指向仅 875 行文件，函数已迁出至 processing.go/publish.go/command_manager.go)。27 个 Go 包中 18 个缺 // Package 包文档注释；AGENTS.md 与 ARCHITECTURE.md 均不存在(harness 必需项，阶段2 创建)。VitePress 站点侧边栏/导航/内部链接全部可解析无死链，但 docs/api/COREC_API_CONTRACT.md 与 docs/API_REFERENCE.md 两份孤儿文档未被索引链接。Go 代码 TODO/FIXME/XXX/HACK 计数 0。

**测量数据**：缺包文档注释 18/27(66.7%)；TODO/FIXME/XXX/HACK=0；死链=0；孤儿文档=2；config.example.yaml 结构化字段与 core.Config 全部匹配，唯一不一致为描述性注释(DOC-001)。

**四份根级分析文档处置建议（仅标记不删除）**：
- AI_HANDOVER.md(35644B)：AI-residue 部分过时，与 docs/architecture/* 及未来 ARCHITECTURE.md 重叠，engine.go 行数声明过时文件树遗漏新增文件 → 归档待确认(阶段2 写完 ARCHITECTURE.md 后移至 docs/references/ 或 docs/exec-plans/completed/，含设计 rationale 勿删)
- IMPROVEMENTS.md(35772B)：AI-residue 行号严重过时，与未来 tech-debt-tracker.md 重叠 → 归档待确认(仍有效改进项迁移到 tech-debt-tracker.md，原文归档勿直接删)
- QUALITY_ASSESSMENT.md(27806B)：AI-residue 评分快照，与未来 docs/QUALITY_SCORE.md 重叠 → 归档待确认(评分方法论迁移到 QUALITY_SCORE.md，快照归档)
- REALTIME_EVALUATION.md(11481B)：一次性性能分析基本准确，与 docs/architecture/performance.md 重叠 → 归档待确认(移至 docs/references/ 或 docs/design-docs/)

**发现条目**：
| ID | 标题 | 位置 | 类别 | 严重度 | 证据 | 修复建议 | 业务行为影响 | 验收方式 |
|---|---|---|---|---|---|---|---|---|
| DOC-001 | 配置示例错误描述环境变量替换机制为"字节级" | config.example.yaml:19 vs config/config.go:48-54 | 文档 | P1 | 示例注释写"Substitution is byte-level (pre-YAML-parse)"；代码注释写"Unlike naive byte-level replacement…parses YAML into generic tree first, expands placeholders, re-serializes"。代码已从字节级重构为 tree-based 防注入，示例注释未同步 | 将 config.example.yaml:18-21 改为"tree-based(parse→expand→re-marshal)，env 值经 YAML 编码器正确转义防注入" | 无 | 文档 lint 校验 config.example.yaml 替换机制描述与 config.go expandEnvVars 注释一致 |
| DOC-002 | IMPROVEMENTS.md 引用 engine.go 不存在的行号(函数已迁出) | IMPROVEMENTS.md:12,25,60 引用 engine/engine.go:1244/1560/1314/1370 | 文档 | P1 | engine/engine.go 仅 875 行；processingLoop 现在 engine/processing.go:7、publishToTargets 在 engine/publish.go:82、startCommandListener 在 command_manager.go:68、executeWriteWithRetry 在 command_manager.go:148。引用行号不可能存在 | 归档前逐条更新为"文件:函数名"形式，或迁移有效条目到 tech-debt-tracker.md 后归档 | 无 | 文档 lint 校验分析文档 file:line 引用行号 ≤ 文件实际行数且函数名匹配 |
| DOC-003 | IMPROVEMENTS.md 引用 batcher.go/publisher.go 已偏移行号 | IMPROVEMENTS.md:20,49 引用 batcher.go:72/183/206/269、publisher.go:665 | 文档 | P1 | 实际 newTransportBatcher@120、publish@295、flush@347、publishWithRetryAndBuffer@386(偏移 48–177 行)；commandCh 满丢弃在 publisher.go:711-713(偏移 +46) | 同 DOC-002：改用函数名引用或迁移到 tech-debt-tracker.md | 无 | 同 DOC-002 |
| DOC-004 | REALTIME_EVALUATION.md 行号引用偏移 | REALTIME_EVALUATION.md:55 引用 types.go:175 | 文档 | P2 | ReadTimeout 字段实际在 core/types.go:229(偏移 +54)；其余多为文件级引用基本准确 | 更新为 types.go:229 或改用字段名 TagConfig.ReadTimeout | 无 | 同 DOC-002 |
| DOC-005 | QUALITY_ASSESSMENT.md 行号引用偏移(含待改进项) | QUALITY_ASSESSMENT.md:88,204 等 | 文档 | P2 | publisher.go:438-440(标"command-secret warn")实际是 TLS cert pool 代码；modbus_base.go:325(标"time.Sleep 重试")实际是 tag 迭代；publisher.go:819(标"goroutine leak 已修复")实际是 canonicalRawJSON。行号漂移 | 归档前更新行号或改用函数名；评分方法论迁移到 docs/QUALITY_SCORE.md | 无 | 同 DOC-002 |
| DOC-006 | AI_HANDOVER.md engine.go 行数声明过时 | AI_HANDOVER.md:96,426 | 文档 | P2 | 声明"已从 1923 行拆为 823 行"，实际 engine/engine.go 为 875 行 | 更新为 875 行或删除具体行数(易腐)改用"已拆分为多职责文件" | 无 | 文档 lint 校验声明行数与 wc -l 一致 |
| DOC-007 | AI_HANDOVER.md 文件树拓扑遗漏新增文件 | AI_HANDOVER.md:31-160 | 文档 | P2 | 遗漏 driver/modbus/{batch_test,lifecycle_test,reconnect_count_test}.go、transport/mqtt/{command_auth_test,forward_test,replay_test,...}.go 等 | 阶段2 写 ARCHITECTURE.md 时以代码现状为准重绘；AI_HANDOVER 归档 | 无 | 结构测试校验文档列举文件集 ⊆ 实际文件集 |
| DOC-008 | 缺少 AGENTS.md(harness 入口地图) | 仓库根 | 文档 | P2 | ls AGENTS.md → No such file；§2.1/§2.2 要求为必需项(≤100 行地图) | 阶段2 按 §2.2/附录 E 创建 | 无 | 存在性检查 + 行数 ≤200 + 含必需 5 节 |
| DOC-009 | 缺少 ARCHITECTURE.md(领域与分层地图) | 仓库根 | 文档 | P2 | ls ARCHITECTURE.md → No such file；§2.1 要求为必需项 | 阶段2 创建(领域地图+包分层+依赖方向规则) | 无 | 存在性检查 + 依赖方向规则与代码抽样一致(≥3 处) |
| DOC-010 | 18/27 Go 包缺 // Package 包文档注释 | 见证据列 | 文档 | P2 | 缺注释 18 包：cmd/corec、common/{metrics,observable,trace,util}、core、demo/chained/{http-sink,lora-sim,mock-plc,validate}、driver/s7、e2e、engine/statistic、hub、hub/{executor,route}、log、transport/parser。有注释 9 包：config、driver/{all,modbus,opcua}、engine、rule、transport/{all,httppush,mqtt} | 为每个缺注释包在主 .go 文件加 // Package X <一句话职责> | 无 | golint/revive 包注释检查通过 |
| DOC-011 | docs/api/COREC_API_CONTRACT.md 未被任何索引链接(孤儿) | docs/api/COREC_API_CONTRACT.md | 文档 | P2 | grep 全 docs/ 的 .md/.ts 无任何链接指向它；VitePress sidebar /api/ 组未收录 | 在 docs/.vitepress/config.ts 的 API sidebar 加入条目，或在 docs/api/overview.md 索引 | 无 | 文档 lint 校验 docs/ 下每个 .md 至少有一条入站链接 |
| DOC-012 | docs/API_REFERENCE.md 未被任何索引链接(孤儿) | docs/API_REFERENCE.md | 文档 | P2 | 同上无入站链接；与 docs/api/* 目录职责重叠 | 确认其与 docs/api/overview.md 关系后合并或链接 | 无 | 同 DOC-011 |
| DOC-013 | config.example.yaml 未演示 scale/offset 字段 | config.example.yaml vs core/types.go:219-220 | 文档 | P2 | TagConfig.Scale/Offset 为可选字段(omitempty)，docs/config/drivers.md 有文档与示例但根级 config.example.yaml 未演示 | 在示例某 tag 加 scale: 0.01 / offset: 0.0 注释行演示 | 无 | 文档 lint 校验示例覆盖所有非 omitempty-仅字段 |

## 已完成：CPLX（复杂度 / 子任务4）

**摘要**：13 个非测试 .go 文件超 T1 的 400 行阈值，3 个超 800 行(publisher.go 1186、modbus_base.go 953、engine.go 875)。golangci-lint gocyclo(阈值 20)实测 4 个函数圈复杂度 >20，全部已被 //nolint:gocyclo 显式抑制并在注释标注复杂度 22–27(scheduler.runTask=27、engine.Start=26、modbus.readTag=26、executor.ApplyConfig=22)——团队已知晓但选择抑制而非重构；8 个函数落 16–20 候选带。13 个函数嵌套 >4 层，最深 scheduler.runTask 7 层。多个 God Object：CoreCEngine(43 字段/59 方法)、MQTTTransport(46/20)、OPCUADriver(35/18)。3 个函数参数 >5；未发现布尔参数地狱。所有发现纯复杂度问题，业务行为影响=无。

**测量数据**：超 400 行非测试 .go 文件 13；圈复杂度 >20 函数 4(均 nolint 抑制，gocyclo 门禁"绿"因热点被 nolint 抑制而非已重构)；圈复杂度 16–20 候选 8；嵌套 >4 层 13(最深 7)；God Object 7(CoreCEngine/MQTTTransport/OPCUADriver/modbusBase/S7Driver/HTTPTransport/transportBatcher)；参数 >5 函数 3；bool 参数 >2 函数 0。Top-10 文件：publisher.go 1186、modbus_base.go 953、engine.go 875、s7.go 727、server.go 701、client.go 662、rule/engine.go 659、batcher.go 512、push.go 480、config.go 467。

**发现条目**：
| ID | 标题 | 位置 | 类别 | 严重度 | 证据 | 修复建议 | 业务行为影响 | 验收方式 |
|---|---|---|---|---|---|---|---|---|
| CPLX-001 | 超长文件: MQTT publisher | transport/mqtt/publisher.go:1-1186 | 复杂度 | P1 | 1186 行(>800) | 按职责拆分 replay_window.go/command_handler.go/tls_config.go/publisher.go | 无 | 文件 <400 行(T1) |
| CPLX-002 | 超长文件: Modbus base | driver/modbus/modbus_base.go:1-953 | 复杂度 | P1 | 953 行(>800) | 拆分 modbus_read.go/modbus_write.go/modbus_address.go/modbus_base.go | 无 | 文件 <400 行(T1) |
| CPLX-003 | 超长文件: engine.CoreCEngine | engine/engine.go:1-875 | 复杂度 | P1 | 875 行(>800) | 拆分 engine_lifecycle.go/engine_stats.go/engine_config.go | 无 | 文件 <400 行(T1) |
| CPLX-004 | 超长文件: S7 driver | driver/s7/s7.go:1-727 | 复杂度 | P1 | 727 行(600-800) | 拆分 s7_address.go/s7_codec.go/s7.go | 无 | 文件 <400 行(T1) |
| CPLX-005 | 超长文件: route server | hub/route/server.go:1-701 | 复杂度 | P1 | 701 行(600-800) | 拆分 server_lifecycle.go/middleware.go/router.go | 无 | 文件 <400 行(T1) |
| CPLX-006 | 超长文件: OPCUA client | driver/opcua/client.go:1-662 | 复杂度 | P1 | 662 行(600-800) | 拆分 opcua_subscription.go/opcua_read.go/opcua_write.go/client.go | 无 | 文件 <400 行(T1) |
| CPLX-007 | 超长文件: rule engine | rule/engine.go:1-659 | 复杂度 | P1 | 659 行(600-800) | 拆分 rule_engine.go/rule_build.go/rule_match.go | 无 | 文件 <400 行(T1) |
| CPLX-008 | 超长文件: batcher | engine/batcher.go:1-512 | 复杂度 | P2 | 512 行(400-600) | 拆出 retry_buffer.go | 无 | 文件 <400 行(T1) |
| CPLX-009 | 超长文件: httppush | transport/httppush/push.go:1-480 | 复杂度 | P2 | 480 行(400-600) | 拆出 webhook.go/push_config.go | 无 | 文件 <400 行(T1) |
| CPLX-010 | 超长文件: config | config/config.go:1-467 | 复杂度 | P2 | 467 行(400-600) | 拆出 validate.go/env_expand.go | 无 | 文件 <400 行(T1) |
| CPLX-011 | 超长文件: route metrics | hub/route/metrics.go:1-453 | 复杂度 | P2 | 453 行(400-600) | 按指标族拆分 metrics_driver.go/metrics_transport.go/metrics_runtime.go | 无 | 文件 <400 行(T1) |
| CPLX-012 | 超长文件: executor | hub/executor/executor.go:1-450 | 复杂度 | P2 | 450 行(400-600) | 拆出 diff.go/apply.go | 无 | 文件 <400 行(T1) |
| CPLX-013 | 超长文件: driver_manager | engine/driver_manager.go:1-422 | 复杂度 | P2 | 422 行(400-600) | 拆出 tagfile_watcher.go | 无 | 文件 <400 行(T1) |
| CPLX-014 | 超高圈复杂度: scheduler.runTask | engine/scheduler.go:190-350(161行) | 复杂度 | P2 | gocyclo=27(>20), //nolint:gocyclo 抑制; 嵌套7层 | 抽取 deadband 过滤/错误降级/重连为独立函数；早返回降低嵌套 | 无 | gocyclo <20 且嵌套 ≤4 层 |
| CPLX-015 | 超高圈复杂度: engine.Start | engine/engine.go:235-379(145行) | 复杂度 | P2 | gocyclo=26(>20), //nolint:gocyclo 抑制 | 按子系统拆为 startDrivers/startTransports/startScheduler/startBatchers | 无 | gocyclo <20 |
| CPLX-016 | 超高圈复杂度: modbus.readTag | driver/modbus/modbus_base.go:437-536(100行) | 复杂度 | P2 | gocyclo=26(>20), //nolint:gocyclo 抑制 | 按数据类型分派表替换 if/switch 链；拆 decodeInt/decodeFloat/decodeBool | 无 | gocyclo <20 |
| CPLX-017 | 超高圈复杂度: executor.ApplyConfig | hub/executor/executor.go:275-364(90行) | 复杂度 | P2 | gocyclo=22(>20), //nolint:gocyclo 抑制 | 按子系统 diff 拆为 applyDrivers/applyTransports/applyRules/applyEngine | 无 | gocyclo <20 |
| CPLX-018 | 圈复杂度候选带(16-20, 8个函数) | config/config.go:298 validateDrivers=20; transport/mqtt/publisher.go:393 buildTLSConfig=20,:467 Start=18; driver/s7/s7.go:621 decodeS7Buffer=18; transport/httppush/push.go:90 Init=17,:274 PublishBatch=17; driver/modbus/modbus_base.go:298 Read=16; config/secrets.go:187 mergeSettings=16 | 复杂度 | P2 | gocyclo 实测均 >15 且 ≤20(低于阈值20, 候选) | 逐个拆分决策分支；优先 buildTLSConfig(20)与 validateDrivers(20) | 无 | gocyclo <16 |
| CPLX-019 | 深嵌套(>4层, 13个函数) | engine/scheduler.go:190 runTask(最深7层@331); transport/mqtt/publisher.go:635 handleCommandMessage; transport/httppush/push.go:189 Start; hub/route/server.go:532 rateLimitMiddleware,:642 authentication; driver/opcua/client.go:300 subscriptionLoop; engine/driver_manager.go:368 scheduleDriverTags; engine/discovery.go:254 reconcile; engine/batcher.go:295 publish; driver/s7/s7.go:250 Read; driver/modbus/modbus_base.go:298 Read; config/config.go:298 validateDrivers; common/util/util.go:303 ReconnectLoopWithBreakerCounted | 复杂度 | P2 | awk 测最大 tab 缩进 ≥5(函数内 ≥4 层嵌套); 最深 runTask=7 | 用早返回(guard clauses)消嵌套；提取嵌套块为命名函数 | 无 | 最大嵌套 ≤4 层 |
| CPLX-020 | God Object: CoreCEngine | engine/engine.go:24-144(struct) | 复杂度 | P1 | 43 字段, 59 方法(均远超阈值 10/12) | 按职责拆为 EngineCore/DriverRegistry/TransportRegistry/StatsCollector 等组合结构 | 无 | 字段 ≤10 且方法 ≤12 |
| CPLX-021 | God Object: MQTTTransport | transport/mqtt/publisher.go:41-123(struct) | 复杂度 | P1 | 46 字段, 20 方法 | 拆为 MQTTConn(连接)/CommandGate(命令+HMAC+重放)/Publisher(发布)组合 | 无 | 字段 ≤10 且方法 ≤12 |
| CPLX-022 | God Object: OPCUADriver | driver/opcua/client.go:22-72(struct) | 复杂度 | P1 | 35 字段, 18 方法 | 拆为 OPCUAConn/SubscriptionManager/NodeCache 组合 | 无 | 字段 ≤10 且方法 ≤12 |
| CPLX-023 | God Object: modbusBase | driver/modbus/modbus_base.go:88-136(struct) | 复杂度 | P2 | 26 字段, 22 方法 | 拆为 ModbusConn/BatchPlanner/TagResolver 组合 | 无 | 字段 ≤10 且方法 ≤12 |
| CPLX-024 | 字段过多: S7Driver/HTTPTransport | driver/s7/s7.go:50-96 S7Driver(27字段/17方法); transport/httppush/push.go:31-71 HTTPTransport(25字段) | 复杂度 | P2 | 字段数 >10 | 收敛连接/状态字段到子结构体 | 无 | 字段 ≤10 |
| CPLX-025 | God Object: transportBatcher | engine/batcher.go:53-110(struct) | 复杂度 | P2 | 16 字段, 13 方法 | 拆出 RetryPolicy/BufferState 子结构 | 无 | 字段 ≤10 且方法 ≤12 |
| CPLX-026 | 高参数计数(>5, 3个函数) | common/util/util.go:303 ReconnectLoopWithBreakerCounted(7参); engine/batcher.go:120 newTransportBatcher(6参); common/util/util.go:289 ReconnectLoopWithBreaker(6参) | 复杂度 | P2 | 参数数 6-7(>5) | 引入 ReconnectOpts/BatcherOpts 配置结构体收拢参数 | 无 | 参数 ≤5 |

## 已完成：PERF（性能 / 子任务9）

**摘要**：可靠性基线整体扎实。goleak 在 4 包强制；所有 goroutine 启动均绑定 context 取消或 WaitGroup；DataBus/OfflineBuffer/replayCache/deadLetterQueue/commandSem/flushBatches 均有上界+驱逐；reconnect 熔断器实现指数退避+抖动+熔断-open；engine.Stop() 先 cancel 再按序 scheduler→discovery→drivers/transports(带超时)→dataBus→ruleProviders→tagWatchers→wg.Wait()；cmd/corec 用 signal.Notify 协调关停；调度器按 interval 分组单次传全量 tags(无 N+1)。残留三处：(1) OPC UA 用 context.Background() 调 Close/Cancel 无超时——服务器无响应时关停挂起+泄漏；(2) Modbus 重试 time.Sleep 不响应 ctx 取消延迟关停；(3) S7 逐 tag 独立 PLC 调用(N+1)、MQTT PublishBatch 逐点串行。另有 rule provider 热重载无 hash 检查、Close 不等 goroutine 退出、goleak 未覆盖 driver/* 与 transport/httppush。无 P0。

**测量数据**：go func 启动 13；bare go someFunc( 启动 11；goroutine 总数 24 全部绑定 context 取消或 WaitGroup；无可见超时网络调用 3(OPC UA Close/Cancel×2 + Cancel×1)+不响应 ctx 阻塞 2(modbus time.Sleep×2)；无界 channel 发送 0 风险(4 个无缓冲 channel 均为 chan struct{} 信号通道单写单关；数据通道均带 buffer+drop-oldest/select default)。关停路径：cmd/corec signal.Notify→hub.Stop()→eng.Stop()→cancel→discovery.Stop→scheduler.Stop→stopComponents(drivers/transports 带超时, batchers 无超时 PERF-008, dataBus, ruleProviders, tagWatchers)→wg.Wait。无 P0 死锁/泄漏/无界内存。

**发现条目**：
| ID | 标题 | 位置 | 类别 | 严重度 | 证据 | 修复建议 | 业务行为影响 | 验收方式 |
|---|---|---|---|---|---|---|---|---|
| PERF-001 | OPC UA Close/Cancel 用 context.Background() 无超时，关停可挂起+泄漏 goroutine | driver/opcua/client.go:411,652,372 | 性能 | P1 | Stop()(411)与 handleConnectionLost()(652)调 d.client.Close(context.Background())；stopSubscription()(372)调 sub.Cancel(context.Background())。gopcua Close/Cancel 不保证受 RequestTimeout 约束。engine.Stop() 用 go func+time.After 包裹 driver.Stop()，超时后 engine 继续但该 goroutine 永久阻塞→泄漏 | 三处改用 context.WithTimeout(context.Background(), d.timeout)；Close/Cancel 失败仅 log 不阻塞 | 修复bug | 注入永不响应的 mock OPC UA 服务器，断言 driver.Stop() 在 N 秒内返回且无 goroutine 泄漏(goleak) |
| PERF-002 | Modbus 重试 time.Sleep 不响应 context 取消，延迟优雅关停 | driver/modbus/modbus_base.go:384,651 | 性能 | P1 | readTagWithRetry(384)与 readBatchWithRetry(651)失败后 time.Sleep(b.retryBackoff)。调度器 readCtx 已取消时 Read 仍睡满 retryBackoff×maxRetry 才返回；scheduler.Stop()→wg.Wait() 被 engine.Stop() 无超时等待→关停延迟 | 将 time.Sleep(b.retryBackoff) 改为 select{case <-ctx.Done(): return; case <-time.After(b.retryBackoff):}，把 ctx 传入 retry 路径 | 修复bug | 取消 ctx 后断言 Read 在 ≤retryBackoff 内返回；关停时延测试通过 |
| PERF-003 | S7 驱动逐 tag 独立 PLC 往返(N+1) | driver/s7/s7.go:265-292 | 性能 | P2 | Read 内 for _, tagName := range batch { d.readAddress(client, addr, dt) }，注释明写"Each tag is still an independent gos7 call"。N tag = N 次串行 PLC 读，无连续地址合并(对比 modbus performBatchReads 已合并) | 对同 DB+连续地址的 tag 合并为单次 ABReadDB；非连续回退逐 tag | 变更行为(合并改变时序/错误粒度，需人工确认) | 基准：N tag 单次 Read 的 PLC 往返数从 N 降至合并后批数；行为测试覆盖混合地址 |
| PERF-004 | MQTT PublishBatch 逐点串行发布(N 次往返) | transport/mqtt/publisher.go:1109-1120 | 性能 | P2 | PublishBatch 内 for i := range points { t.Publish(ctx, points[i]) }，每次 Publish 调 token.WaitTimeout 串行等待。100 点 = 100 次串行 MQTT PUBLISH+ACK | 先并发 fire 所有 token 再统一 WaitTimeout(保留 firstErr 语义)；或文档化此为有意串行 | 变更行为(并发改变顺序/背压语义，需人工确认) | 基准：batch 发布延迟从 N×RTT 降至 ~RTT；QoS/顺序测试通过 |
| PERF-005 | rule provider 热重载每 tick 全量重解析+重编译，无 hash 检查 | rule/provider.go:99-112 | 性能 | P2 | reloadLoop 每 tick 调 p.load()→os.ReadFile+yaml.Unmarshal+逐规则 compileExpr，无变更检测。对比 engine/tagfile.go 用 SHA-256 hash 跳过未变更文件 | 仿 tagfile.go 加 SHA-256 hash，未变更则跳过 load | 修复bug | 未改文件时断言 load 次数为 0 |
| PERF-006 | rule provider Close 不等 reloadLoop 退出(有界泄漏) | rule/provider.go:140-142 | 性能 | P2 | Close() 仅 close(p.stopCh)，无 done channel/WaitGroup。reloadLoop 最长再跑一个 tick 才退出；期间 provider 不可被 GC。有界(≤interval)但非即时 | 加 done chan struct{}，loop 退出时 close(done)，Close 后 <-done。仿 tagfile.go:132-135 | 修复bug | goleak 测试：Close 后无 reloadLoop 拷留 |
| PERF-007 | goleak 仅覆盖 4 包，driver/* 与 transport/httppush 未强制 | driver/opcua/,driver/modbus/,driver/s7/,transport/httppush/(无 main_test.go) | 性能 | P2 | goleak.VerifyTestMain 仅在 engine/,transport/mqtt/,hub/route/,rule/ 的 main_test.go。driver 的 reconnectLoop/subscriptionLoop、httppush 的 webhook goroutine 无机械泄漏校验 | 为上述 4 包加 main_test.go + goleak.VerifyTestMain(按需 IgnoreAnyFunction 排除 paho/gos7 后台线程) | 修复bug | 新增 main_test.go 后 go test ./driver/... ./transport/httppush/... goleak 通过 |
| PERF-008 | engine.Stop() 对 batcher.stop() 无超时(与 driver/transport 不对称) | engine/engine.go:528-531 | 性能 | P2 | for i, b := range batchers { b.stop() } 无 time.After 包裹。b.stop()→flushFinal()→publishWithRetryAndBuffer(context.Background(),…)。当前因内部 WithTimeout+有界 retry 而有界，但若 transport.PublishBatch 忽略 ctx 或 retry 配置过大，Stop 挂起 | 给 batcher.stop() 同样加超时包裹，或给 flushFinal 传带超时的 ctx | 修复bug | 注入阻塞型 transport，断言 engine.Stop() 在 shutdownTimeout 内返回 |

## 全部 7 路扫描完成。综合统计：
- 总条目 75：P0×1(TEST-001)，P1×27，P2×47
- 按域：ARCH×2, CPLX×26, DUP×9, DOC×13, TEST×11, SEC×6, PERF×8
- 业务行为影响：无=66，修复bug=6(SEC-001告警/PERF-001/PERF-002/PERF-005/PERF-006/PERF-007/PERF-008 中修复bug 的)，变更行为=7(SEC-001改默认/SEC-002/SEC-003/SEC-004/SEC-006/PERF-003/PERF-004)
  注：SEC-001 双重标注(告警=修复bug，改默认=变更行为)；PERF-007 归修复bug(补护栏)
