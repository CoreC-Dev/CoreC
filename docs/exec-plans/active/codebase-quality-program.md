# 代码库质量提升计划（冗余消除 / 架构质量 / 文档 / Harness）

> **状态**：进行中（批次 0 实施中）
> **来源**：`docs/audits/redundancy-quality-audit-2026-10-21.md`（2026-10-21 四路并行审计：后端冗余 / 前端冗余 / 文档质量 / Harness 工程化，全部发现含 file:line 证据并经主会话二次复核）
> **范围**：CoreC + CoreC-Dashboard 双仓。审计发现汇总、优先级、批次规划与验收标准见来源文档 §0–§3；本文件只跟踪执行进度。
> **纪律**：每批次独立可验证（两仓门禁 + 测试全绿 + CI 绿）；重构零行为变化；F1/F2/F3 为 bug 修复，按 R2 登记于 commit message；B11——本计划的批次取舍决策记录在来源文档。

## 批次进度

| 批次 | 内容 | 状态 | Commit |
|---|---|---|---|
| 0 | 跨仓契约修复：F1 日志 level 字符串契约 / F2 流量面板死亡 / F3 安全文档虚假声明 / D6 边界 schema 化 | 🔄 进行中 | — |
| H | Harness：H1 lint 钉版+钩子硬失败 / H2 version-check 死代码迁移 / H3 server.mjs 纳管 / H4 CI 对齐本地门禁 | ⏳ 待做 | — |
| C | 后端清理：B1 守卫缺口 / B2 复杂度 / B3+B4 死代码 / B5 守卫薄弱 / B6 数字漂移 / B7 命名与重复 / B8 巨型结构（并入 CPLX-001） | ⏳ 待做 | — |
| F | 前端清理：D1 staged API 决策 / D2 脚手架去重与豁免清零 / D3 i18n 死键 / D4 分层 / D5 死导出 | ⏳ 待做 | — |
| G | 文档：G1+G2 巨型参考退役 / G3 可达性 / G4 对账 / G5 零散 | ⏳ 待做 | — |
| W | 仓库卫生：H6 清单 | ⏳ 待做 | — |

## 批次内明细跟踪

### 批次 0（进行中）
- [ ] F1：`logEventSchema`（z.preprocess 字符串 level → 数值）+ AlertStreamProvider/EventLogTerminal 消费归一化值 + 回归测试（字符串帧）
- [ ] F2：`trafficFrameSchema` 判别联合（rate\|totals）+ RealtimeStreamsPanel 直连 rate 帧 + 注释修正 + 回归测试
- [ ] F3：SECURITY.md 两处改事实（localStorage + raw 明文）+ 删死 sessionStorage 助手
- [ ] D6：TrafficChart 帧补校验；/logs 与 /traffic 边界 schema 齐备
