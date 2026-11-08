# 安全态势

## 已落地的防护

- **管理面认证**：全部 RESTful/WS 端点（除 `GET /version`、`/healthz/*`）要求 Bearer secret（≥8 字符，配置校验强制）；WS 支持 `?token=` 查询串（浏览器 WebSocket API 无法自定义 Header）
- **发布二进制校验和**：每个 Release 附 `checksums.txt`（SHA-256）
- **命令通道 fail-closed**：MQTT 命令订阅无 secret 时拒绝启动；HMAC + 重放窗口 + strict 模式
- **Webhook fail-closed**：配置 `webhook-addr` 而未配 `webhook-secret` 时拒绝启动
- **路径穿越**：备份名白名单校验（`backups.go` 单源）；HTTP 端点经 chi 规范化
- **配置密钥**：`${ENV_VAR}` 展开（tree-based，非字节级）；`PUT /configs`/`GET /configs/raw` 均在认证组内

## 残余风险

- **`GET /configs/raw` 返回明文**（无脱敏）：`GET /configs` 概要版做 `secret-set: true` 布尔脱敏
- **二进制未签名**：以校验和替代
- **认证为共享 secret**：单租户网关模型
- **REST/WS 无 TLS 终止**：`api.listen` 为明文 HTTP

## 报告

安全问题请开私有 advisory（GitHub Security Advisories）或直接联系维护者，勿在公开 issue 描述可利用细节。
