# 工程与质量中心

本页是两仸仓库的工程记录枢纽——审计发现、执行计划、门禁定义与质量评分都从这里可达（此前这整簇文档相互引用但没有任何站点导航入口，2026-10-21 质量审计 G3 修复）。

## 审计记录

- [后端功能完整性审计（2026-10-19）](audits/backend-functionality-audit-2026-10-19.md)——40 项路线图的来源；其加固计划已全部完成
- [代码库质量审计（2026-10-21）](audits/redundancy-quality-audit-2026-10-21.md)

进行中：[端到端验证程序（2026-10-23）](exec-plans/active/e2e-verification-program.md)——生产级稳定性与可用性验证，含实测发现清单——冗余/架构/文档/Harness 四路并行审计，本计划的来源

## 执行计划

- [已完成：代码库质量提升计划](exec-plans/completed/codebase-quality-program.md)（七批次全部落地）
- [已完成：生产加固计划](exec-plans/completed/production-hardening.md)（40/40 路线图项收口记录）
- [已完成：代码库质量提升计划](exec-plans/completed/codebase-quality-program.md)（冗余/架构/文档/Harness 四维七批次）
- [技术债跟踪表](exec-plans/tech-debt-tracker.md)

## 质量与门禁

- [质量评分](QUALITY_SCORE.md)——按域打分，含复算依据
- [CI 决定](CI.md)——工作流清单与门禁决策
- [门禁清单](gates.md)——G1-G10 逐项定义

## 设计文档

- [设计文档索引](design-docs/index.md)（含历史评估快照：AI_HANDOVER / IMPROVEMENTS / QUALITY_ASSESSMENT——已加冻结标注）

## 冻结的历史参考

- [API Contract（冻结快照）](api/COREC_API_CONTRACT.md)——已被 [API 索引](api/overview.md) + 主题页取代
- [API Reference（冻结快照）](API_REFERENCE.md)——同上
