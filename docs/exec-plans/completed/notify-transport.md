# 执行计划：通知传输插件（Notify Transport）

> 状态：待执行 · 关联功能：告警闭环 · 业务行为影响：**无**（新增插件，不改变现有传输行为）
> 跨仓库：CoreC-Dashboard 对应计划见 `CoreC-Dashboard/docs/exec-plans/completed/notify-transport.md`

## 1. 目标

新增北向传输插件 `transport/notify`，支持 Slack / Microsoft Teams / Email(SMTP) / 通用 Webhook 四种通道。规则命中后经 `action: forward, target: notify-xxx` 即可把告警消息推送到人，闭环"采集→越限→通知"。复用现有 transport 的 retry / offline-buffer / fallback，传输不可达时告警不丢。

## 2. 背景与缺口

- 现有 `alert` 规则动作只触发**进程内** `alertHandlers`（`engine/engine.go:56`、`engine/publish.go:173 fireAlert`），无外发通道。
- 全仓无 SMTP / Slack / 通知实现（实测 grep 无命中）。
- 工业告警需到达运维人员，当前只能落到一个 MQTT topic，需运维再接一层中间件。

## 3. 设计

### 3.1 路由方式：forward 目标（零引擎改动）

MVP 用 `action: forward, target: notify-slack`——notify 就是一个会格式化消息的普通传输，规则引擎现有的 forward 路径直接复用，**不改 alert 机制、不改 engine**。后续可选增强 `alert` 动作也转发到 target（登记为后续项，不在本计划范围 R4）。

### 3.2 插件结构（镜像 transport/mqtt）

```
transport/notify/
  register.go      // init() → core.RegisterTransport("notify", factory)
  notify.go        // NotifyTransport 实现 core.Transport（10 方法）
  channel_slack.go
  channel_teams.go
  channel_email.go
  channel_webhook.go
  template.go      // text/template 渲染
  cooldown.go      // 同规则窗口内去重
  *_test.go
```

- `init()` 调 `core.RegisterTransport("notify", NewNotifyTransport)`（对齐 `transport/mqtt/register.go` 模式）。
- `transport/all/all.go` 加空导入 `_ "github.com/CoreC-Dev/CoreC/transport/notify"`。
- `NotifyTransport` 实现 `core.Transport`：
  - `Init`：解析 `config.Settings`（channel + 通道特定字段 + template + cooldown），编译模板。
  - `Start`：就绪（无长连接，Slack/email 都是按需 HTTP/SMTP）。
  - `Publish(ctx, point)`：渲染消息 → cooldown 检查 → 限频 → 发送。`PublishBatch` 逐条或合并。
  - `OnCommand()`/`OnData()`：返回 nil（notify 是纯出方向）。
  - `Stop`：关闭 cooldown 状态、等待在途请求。
  - `Name()`/`Type()`/`Status()`：标准元数据。

### 3.3 通道实现（零外部依赖优先）

| 通道 | 实现 | 依赖 |
|---|---|---|
| **Slack** | POST JSON 到 incoming webhook URL | `net/http`（标准库） |
| **Teams** | POST MessageCard JSON 到 webhook URL | `net/http` |
| **Email** | `net/smtp` + STARTTLS | `net/smtp`（标准库） |
| **Webhook** | 可配 method/headers/body 模板，接任意系统（钉钉/飞书/自建） | `net/http` |

- **限频**：Slack 限 1 msg/sec/channel，内置令牌桶；遇 HTTP 429 读 `Retry-After` 退避。
- **模板**：Go `text/template`（标准库），可用 <code v-pre>{{.Tag}} {{.Value}} {{.Driver}} {{.Device}} {{.Group}} {{.Timestamp}} {{.Quality}}</code>。规则名经 `DataPoint.Metadata["rule"]` 传入（forward 路径需在 publish 时注入 rule 名，见 3.5）。

### 3.4 配置 schema

```yaml
transports:
  - name: notify-slack
    type: notify
    settings:
      channel: slack              # slack | teams | email | webhook
      webhook-url: ${SLACK_WEBHOOK_URL}
      template: "🚨 [{{.Metadata.rule}}] {{.Tag}} = {{.Value}} ({{.Driver}})"
      cooldown: 5m                # 同 rule 5 分钟内至多 1 条（防高频标签刷屏）
  - name: notify-email
    type: notify
    settings:
      channel: email
      smtp-host: smtp.example.com
      smtp-port: 587
      smtp-user: ${SMTP_USER}
      smtp-pass: ${SMTP_PASS}
      from: corec@example.com
      to: ["oncall@example.com"]
      subject: "[CoreC] {{.Metadata.rule}}"
      body: "Tag {{.Tag}} on {{.Driver}}/{{.Device}} = {{.Value}} at {{.Timestamp}}"
      cooldown: 5m
  - name: notify-generic
    type: notify
    settings:
      channel: webhook
      url: ${CUSTOM_WEBHOOK_URL}
      method: POST
      headers: {Authorization: "Bearer ${WH_TOKEN}"}
      body-template: '{"tag":"{{.Tag}}","value":{{.Value}}}'
```

- 密钥字段（`webhook-url`/`smtp-pass`/headers.Authorization）经 `${ENV_VAR}` 替换（已有 tree-based 机制），不落明文；API 永不回显（`config/config.go` 已保证）。

### 3.5 规则名注入

forward 路径当前把 `DataPoint` 发给 target 传输。notify 模板需知道是哪条规则触发的。方案：规则引擎在 forward 时把规则名写入 `point.Metadata["rule"]`（Metadata 已是 `map[string]string`，`omitempty`，不破坏现有 payload）。若该改动触及行为，单独提交并标注（R2）。

> 备选（更隔离）：notify 不依赖 Metadata，而是 transport 配置里写死模板（不显示规则名）。MVP 取注入方案，因规则名对告警可读性关键。

### 3.6 cooldown（高频抑制）

- transport 内部 `map[ruleKey]lastSentTime` + `cooldown` 窗口。窗口内重复命中只计数不外发（对齐 README"高频错误抑制 10s 窗口"同思路）。
- `ruleKey` = `Metadata["rule"]`（无规则名时按 tag）。
- 状态进程内，重启重置（可接受；持久化登记为后续项）。

### 3.7 故障容错（复用现有基础设施）

notify 是真 transport → 自动享有：
- **retry**：`TransportConfig.RetryCount`，发送失败重试。
- **offline-buffer**：`TransportConfig` 的 buffer 配置，Slack 宕了离线缓冲接住、恢复后回放，**告警不丢**。
- **fallback**：`TransportConfig.Fallback` 指定备用通道（如 notify-slack fallback notify-email）。

## 4. 批次分解（单一职责提交）

| 批次 | 范围 | 文件 | 验收 |
|---|---|---|---|
| **N1** | 插件骨架 + 注册 | `transport/notify/register.go`、`notify.go`（Transport 10 方法骨架）；`transport/all/all.go` 加空导入 | `go build ./...` 通过；`core.RegisteredTransports()` 含 `notify`；`archtest` 依赖方向通过 |
| **N2** | webhook + slack 通道 | `channel_webhook.go`、`channel_slack.go`、`template.go`、`cooldown.go` + 测试（mock httptest） | 模板渲染正确；429 退避；cooldown 窗口内去重；`go test -race` 通过 |
| **N3** | teams + email 通道 | `channel_teams.go`、`channel_email.go` + 测试（mock SMTP） | Teams MessageCard 格式正确；email STARTTLS；失败重试 |
| **N4** | 规则名注入 + 端到端 | 规则引擎 forward 注入 `Metadata["rule"]`（若需）；`e2e/` 加 notify 端到端（mock webhook server） | 规则命中 → webhook 收到含规则名的消息；offline-buffer 回放验证 |
| **N5** | 配置示例 + 文档 | `config.example.yaml` 加 notify 段；`docs/config/` 通知传输参考；`README.md` 特性列表 | `scripts/check-docs.sh` 无死链；示例 validate 通过 |

> R2：N1–N3、N5 业务行为影响=无（纯新增插件）。N4 注入 Metadata["rule"] 改变 forward payload（新增可选字段，omitempty 不破坏现有消费者），标注 `feat(notify): inject rule name into forward metadata` 单独提交。

## 5. DoD

- [ ] 四种通道均可发送（mock 验证），含模板渲染、限频、cooldown。
- [ ] 规则 `action: forward, target: notify-slack` 命中后 webhook 收到正确消息。
- [ ] 传输不可达时离线缓冲接住、恢复后回放（端到端）。
- [ ] 密钥经 `${ENV_VAR}` 替换，API `/configs/raw` 不回显明文（复用现有测试）。
- [ ] `go test -race ./...` 全绿；新增文件 gocyclo <15、行数 <400（按通道拆分保证）。
- [ ] `golangci-lint run` 无新增告警；`archtest` 通过（notify 只依赖 core/common）。
- [ ] 文档同步；本计划移入 `completed/`。

## 6. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 密钥泄露 | `${ENV_VAR}` 替换 + API 不回显（已有机制）；测试用 mock 不触真实端点 |
| Slack 限频被封 | 令牌桶 + 429 Retry-After 退避；cooldown 双重抑制 |
| 模板注入（用户配置的模板含恶意构造） | text/template 天然无代码执行；值经 JSON 编码进 webhook body |
| email SMTP 阻塞 | net/smtp 调用加 context 超时；失败走 retry/offline-buffer |
| cooldown 状态重启丢失 | 文档声明；持久化登记后续项（R4 不在本计划范围） |

## 7. 依赖

- 无新外部依赖（全用标准库 `net/http`/`net/smtp`/`text/template`）。
- 复用 `core.Transport`/`TransportConfig`/`TransportFactory`/`RegisterTransport`（已有）。
