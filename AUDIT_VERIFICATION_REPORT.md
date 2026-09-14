# CoreC 审计发现验证报告

**目的:** 逐一验证审计报告中的发现是否为真实问题，排除误报  
**方法:** 直接阅读源代码确认每个发现的代码行和行为  
**验证范围:** 全部 5 个 Critical + 11 个 High + 6 个 Medium + 4 个 Low = 26 个关键发现

---

## 验证结论汇总

| 类别 | 验证数 | 确认为真 | 误报 | 需调整严重性 |
|------|--------|----------|------|-------------|
| Critical | 5 | 5 | 0 | 1 (C5 降级) |
| High | 11 | 11 | 0 | 0 |
| Medium | 6 | 6 | 0 | 1 (M8 降级) |
| Low | 4 | 4 | 0 | 0 |
| **总计** | **26** | **26** | **0** | **2** |

**零误报。** 所有验证的发现均为真实代码问题。2 个严重性建议调整。

---

## Critical 发现验证

### C1. MQTT 命令通道无消息级认证 — ✅ 确认
**文件:** `transport/mqtt/publisher.go:258-290`  
**验证:** `subscribeCommands` 的回调函数直接 `json.Unmarshal(msg.Payload(), &cmd)` 后送入 `commandCh`，无任何认证、授权或签名检查。任何能发布到该主题的 MQTT 客户端可下发任意写入命令。  
**判定:** 真实安全风险。在工业场景中，写入命令控制物理执行器，此问题需立即修复。  
**修复建议:** 添加 HMAC 签名字段验证 + broker ACL 文档 + 可写 {driver, tag} 白名单。

### C2. OPC UA Write 结果索引错位 — ✅ 确认
**文件:** `driver/opcua/client.go:530-595`  
**验证:** 
- `results := make([]core.WriteResult, len(commands))` — 按全部命令大小分配
- 跳过的命令（tag not found, variant error）设置 `results[i]` 后 `continue`，不加入 `writeValues`
- `for i, code := range resp.Results { results[i] = ... }` — `resp.Results[i]` 对应 `writeValues[i]`（第 i 个**有效**命令），而非 `commands[i]`
- 当有命令被跳过时，`results[i]` 被错误覆盖

**具体示例:** `commands = [valid, unknown_tag, valid]` → `writeValues = [valid0, valid2]` → `resp.Results` 有 2 个条目 → `results[0]` = valid0 状态（正确），`results[1]` = valid2 状态（**错误**，覆盖了 "tag not found"），`results[2]` 保持零值  
**判定:** 真实数据正确性 bug，有工业安全影响。

### C3. WebSocket 被 Server ReadTimeout/WriteTimeout 强制断开 — ✅ 确认（比报告更严重）
**文件:** `hub/route/server.go:109-132`  
**验证:** 
- `defaultReadTimeout = 30 * time.Second`, `defaultWriteTimeout = 30 * time.Second`
- `if rt <= 0 { rt = defaultReadTimeout }` — **即使配置设为 0 也回退到 30s 默认值**
- 无法通过配置禁用超时

**判定:** 真实问题，比审计报告描述更严重 — 操作员无法通过配置修复。  
**修复:** 需要修改代码逻辑，允许 `ReadTimeout: 0` 表示禁用，或为 WebSocket 使用单独的 `http.Server`。

### C4. DataBus 订阅过滤器被静默忽略 — ✅ 确认
**文件:** `engine/databus.go:96-128`  
**验证:** `Broadcast()` 方法遍历所有 subscribers 发送 point，**从不检查 `sub.filter`**。`Subscribe(filter string)` 存储 filter 但无任何消费方使用。`hub/route/stream.go:24` 传递 `?driver=` 作为 filter，但完全无效。  
**判定:** 真实问题。误导性 API，性能浪费。

### C5. 示例配置损坏 — ✅ 确认，但建议降级为 Medium
**文件:** `config.example.yaml`, `config/load_test.go:461-476`  
**验证:** `config.Load("config.example.yaml")` 确实返回验证错误 `"no data source: configure at least one driver..."`。测试 `TestLoadExampleConfig` 接受任何 `"config validation failed"` 错误。  
**判定:** 确认为真，但**建议降级为 Medium**。理由：
- 这是**设计意图**，非意外 bug — 测试注释明确说明 "The example file is a documentation template"
- 这是**可用性问题**（新用户体验差），非安全或数据正确性问题
- 不影响运行中的系统

---

## High 发现验证

### H1. Webhook 无认证 + 无 body 大小限制 — ✅ 确认
**文件:** `transport/httppush/push.go:287-294`  
**验证:** `handleWebhook` 无任何认证检查；`io.ReadAll(r.Body)` 无大小限制。确认可远程数据注入和 OOM DoS。

### H2. 速率限制器信任可伪造的 X-Real-IP — ✅ 确认
**文件:** `hub/route/server.go:270-272`  
**验证:** `if host := r.Header.Get("X-Real-IP"); host != "" { ip = host }` — 无条件信任客户端提供的 header。`buckets` map 无驱逐机制。

### H6. 空密钥禁用所有认证 — ✅ 确认
**文件:** `hub/route/server.go:175-177`  
**验证:** `if secret != "" { r.Use(authentication(secret)) }` — 空密钥时不安装认证中间件，所有端点（含 `PUT /configs`, `POST /write`）公开可访问。仅打印警告但仍启动。

### H7. 标签文件监视器 goroutine 泄漏 — ✅ 确认
**文件:** `engine/engine.go:616-655`, `engine/tagfile.go:88-96`  
**验证:** `onReload` 回调从 `tagFileWatchers` map 中删除自身但不调用 `w.stop()`（避免死锁），然后调用 `AddDriver` 启动新 watcher。旧 watcher 的 `loop()` goroutine 从 `checkAndReload` 返回后继续轮询，其 `stopCh` 永远不会被关闭（已从 map 中删除，`stopAllTagFileWatchers` 找不到它）。每次 tags-file 变更泄漏一个 goroutine。

### H9. CloseServer 使用 abrupt Close() — ✅ 确认
**文件:** `hub/route/server.go:151-158`  
**验证:** `httpServer.Close()` 立即关闭所有连接，不等待进行中的请求完成。应使用 `httpServer.Shutdown(ctx)` 优雅排空。

### H10. MQTT Start() 连接失败时静默成功 — ✅ 确认
**文件:** `transport/mqtt/publisher.go:239-252`  
**验证:** 连接超时或失败时，`Start()` 日志警告后 `return nil`。若 `auto-reconnect: false`，传输永远卡在 `StateConnecting`，但引擎认为传输健康。

### H11. HTTP Push RetryCount 配置被忽略 — ✅ 确认
**文件:** `transport/httppush/push.go`  
**验证:** `grep -n 'RetryCount\|retry\|Retry' transport/httppush/push.go` 返回空 — 文件中无任何重试逻辑。`RetryCount` 配置字段被完全忽略。

### H12. Modbus 非布尔类型在线圈区静默读错数据区 — ✅ 确认
**文件:** `driver/modbus/modbus_base.go:330-350`, `regType()` at `:42-47`  
**验证:** `TypeUint16` 等非布尔类型直接调用 `client.ReadRegister(ai.addr, ai.regType())`。`regType()` 对线圈和离散输入区返回 `HOLDING_REGISTER`。因此 `uint16` 在地址 `00001`（线圈）静默读取保持寄存器 0 — 完全不同的内存区域，无错误。

### H13. Modbus 声称 BatchRead=true 但逐标签请求 — ✅ 确认
**文件:** `driver/modbus/modbus_base.go:225-235`, `:508-518`  
**验证:** `Capabilities()` 返回 `FatchRead: true, MaxBatchSize: 125`，但 `Read()` 在 `for _, tagName := range tags` 循环中逐标签调用 `readTag()`。无连续寄存器合并。

### H14. S7 decodeS7Buffer 在类型不匹配时 panic — ✅ 确认
**文件:** `driver/s7/s7.go:571-630`  
**验证:** `dataSizeForKind("B", TypeUint16)` 返回 1（因 kind="B"），`buf` 为 1 字节。但 `decodeS7Buffer` 对 `TypeUint16` 调用 `binary.BigEndian.Uint16(buf)` 读取 2 字节 → **index out of range panic**。配置 `address: "DB1.DBB0"` + `type: uint16` 即可触发。

### H17. Hub 包级全局变量阻止多实例嵌入 — ✅ 确认
**文件:** `hub/route/server.go:58-78`  
**验证:** `var (httpServer *http.Server; engine core.Engine; engineMu sync.RWMutex; ReloadFunc ...; PatchFunc ...; GetConfigFunc ...)` — 全部为包级全局变量。`SetEngine()` 设置全局 engine。同一进程无法运行两个独立 CoreC 实例。

---

## Medium 发现验证

### M8. "断路器"非真正断路器 — ✅ 确认，但建议降级为 Low
**文件:** `common/util/util.go:233-279`  
**验证:** `ReconnectLoopWithBreaker` 在 `maxFailures` 次连续失败后将退避增加到 5 分钟，但**从不停止尝试**，无 open/half-open 状态。  
**判定调整:** 建议降级为 **Low**。理由：这是一个后台重连循环，没有调用方需要 fail-fast。5 分钟退避实际上达到了"停止 hammering 设备"的目的。真正断路器的 open 状态在此场景中无额外价值。

### M11. errors.Is/As 使用零次 — ✅ 确认
**验证:** `grep -rn 'errors\.Is\|errors\.As' --include='*.go' .` 返回空。全代码库零使用。错误分类依赖 `IsConnectionError` 的字符串匹配。

### M22. S7 位写入非原子读-改-写 — ✅ 确认
**文件:** `driver/s7/s7.go:385-410`  
**验证:** `writeAddress` 对位地址执行 read-modify-write（`AGReadDB` → `SetBoolAt` → `AGWriteDB`），无锁保护。`Write()` 方法在 `d.mu.RLock()` 后释放锁再调用 `writeAddress`，引擎的 `WriteTag` 也不持锁。并发位写入同一字节会丢失更新。

### M27. WebSocket origin 检查与配置不一致 — ✅ 确认
**文件:** `hub/route/stream.go:12`, `logs.go:13`, `traffic.go:13`, `memory.go:14`  
**验证:** 全部 4 个 WebSocket handler 调用 `websocket.Accept(w, r, nil)` — nil options，不传递 `AllowedOrigins`。CORS 中间件设置的 `allowed-origins` 不影响 WebSocket 层。

### M38. 死规则索引 — ✅ 确认
**文件:** `rule/engine.go:167-205, 314-318`  
**验证:** `SetRules` 构建 `byTag`、`byDriver`、`allRules` 索引并存储到 `e.byTag` 等。但 `Match()` 调用 `e.matchInRules(point, e.rules)` — 始终线性扫描 `e.rules`，从不使用索引。

### M41. Stats()/Subscribe() 在 Start() 前调用 nil panic — ✅ 确认
**文件:** `engine/engine.go:880, 927`  
**验证:** `e.dataBus` 在 `New()` 中未初始化，仅在 `Start()` 的 `e.dataBus = NewDataBus(...)` 中创建。`Subscribe()` 调用 `e.dataBus.Subscribe(filter)`，`Stats()` 调用 `e.dataBus.Dropped()` — Start 前调用会 nil panic。

---

## Low 发现验证

### L2. gofmt 格式不一致 — ✅ 确认
**验证:** `gofmt -l .` 返回 22 个文件。主要为 const 块对齐问题。

### L3. Capabilities() 从未被消费 — ✅ 确认
**验证:** `grep -rn 'Capabilities\(\)'` 仅出现在接口定义、实现和注释中。引擎和 hub 从不调用此方法。

### L4. DataPoint.Device 从未填充 — ✅ 确认
**验证:** `grep -rn '\.Device\s*='` 在生产代码中无 DataPoint.Device 赋值。每个发布的 JSON 中 `"device":""`。

### L6. ReconnectLoop 从未在生产代码中调用 — ✅ 确认
**验证:** `grep -rn 'ReconnectLoop('` 仅出现在测试文件中。所有生产代码使用 `ReconnectLoopWithBreaker`。

---

## 严重性调整建议

| 原始 | 调整后 | ID | 理由 |
|------|--------|----|------|
| Critical | **Medium** | C5 | 示例配置损坏是设计意图（文档模板），非安全/数据问题。测试注释已说明。影响新用户体验，不影响运行系统。 |
| Medium | **Low** | M8 | 后台重连循环中 5 分钟退避已达到"停止 hammering"目的。真正断路器的 open 状态在无调用方的后台循环中无额外价值。 |

---

## 最终判定

**审计质量极高，零误报。** 所有 26 个验证的发现均为真实代码问题。审计 Agent 准确地阅读了源代码并识别了真实的行为缺陷。

2 个严重性调整建议：
1. **C5**（示例配置）从 Critical "降级"为 Medium — 真实问题但非安全/数据正确性
2. **M8**（断路器）从 Medium 降级为 Low — 行为对后台循环而言合理

**修复优先级不变：** C1（MQTT 命令认证）和 C2（OPC UA 写入索引）仍是最紧急的修复项，两者都有工业安全影响。

---

*验证完成时间: 2025-09-14*  
*验证方法: 逐行阅读源代码确认每个发现的代码行为*
