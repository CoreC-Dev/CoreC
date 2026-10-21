# 代码库质量提升计划（冗余消除 / 架构质量 / 文档 / Harness）

> **状态**：进行中（0/H/C/F 主体完成；余：D2 页族与豁免清零、B8 拆分、批次 G 文档、批次 W 卫生）
> **来源**：`docs/audits/redundancy-quality-audit-2026-10-21.md`（2026-10-21 四路并行审计：后端冗余 / 前端冗余 / 文档质量 / Harness 工程化，全部发现含 file:line 证据并经主会话二次复核）
> **范围**：CoreC + CoreC-Dashboard 双仓。审计发现汇总、优先级、批次规划与验收标准见来源文档 §0–§3；本文件只跟踪执行进度。
> **纪律**：每批次独立可验证（两仓门禁 + 测试全绿 + CI 绿）；重构零行为变化；F1/F2/F3 为 bug 修复，按 R2 登记于 commit message；B11——本计划的批次取舍决策记录在来源文档。

## 批次进度

| 批次 | 内容 | 状态 | Commit |
|---|---|---|---|
| 0 | 跨仓契约修复：F1 日志 level 字符串契约 / F2 流量面板死亡 / F3 安全文档虚假声明 / D6 边界 schema 化 | ✅ 完成 | FE `1a3e0d0` |
| H | Harness：H1 lint 钉版+钩子硬失败 / H2 version-check 死代码迁移 / H3 server.mjs 纳管 / H4 CI 对齐本地门禁 | ✅ 完成 | `f11fdf1`(H1+H2) `ce52840`(H4) · FE `5e8295d`(H3) |
| C | 后端清理：B1 守卫缺口 / B2 复杂度 / B3+B4 死代码 / B5 守卫薄弱 / B6 数字漂移 / B7 命名与重复 / B8 巨型结构（并入 CPLX-001） | 🔄 主体完成（B1–B7 除 mock 合并与 ParseDurationOr 外；B8 随 CPLX-001 拆分单独排期） | `fbddb4c` `a1b03df` `4334dc5` + 本批 |
| F | 前端清理：D1 staged API 决策 / D2 脚手架去重与豁免清零 / D3 i18n 死键 / D4 分层 / D5 死导出 | 🔄 D1-D5 + 两个最大豁免已拆（RuleGroupEditor 890→324/290/310、configSchema 811→450/291/53/37；豁免 6→4）；余 4 个豁免（Detail×2 677/656、Wizard 645、DriversPage 637）与页族脚手架抽取 | FE `b7f88bd` `0ccd1b0` `5c01c5a` `7ea6490` |
| G | 文档：G1+G2 巨型参考退役 / G3 可达性 / G4 对账 / G5 零散 | ⏳ 待做 | — |
| W | 仓库卫生：H6 清单 | ⏳ 待做 | — |

## 批次内明细跟踪

### 批次 0（✅ 完成 2026-10-21，FE `1a3e0d0`）
- [x] F1：`logEventSchema`（z.preprocess 字符串 level → 数值，含 LEVEL+N 自定义severity）+ AlertStreamProvider/EventLogTerminal 边界消费 + 12 个契约回归测试
- [x] F2：`trafficFrameSchema` 判别联合（rate\|totals）+ RealtimeStreamsPanel 直连 rate 帧（保留 totals 客户端 diff 兼容路径）+ 陈旧"CUMULATIVE"注释删除
- [x] F3：SECURITY.md 两处改事实（localStorage + raw 明文 + Bearer 门禁姿态）+ 删死 sessionStorage 助手及其测试
- [x] D6：/logs 与 /traffic 双模式边界 schema 齐备（TrafficChart 本就以字段探测消费 rate 帧，风险敞口仅在无校验——批次 F 补）
