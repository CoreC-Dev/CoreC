import { defineConfig } from 'vitepress'

export default defineConfig({
  title: 'CoreC',
  description: 'Connect · Collect · Control — 工业物联网数据采集与控制核心',
  lang: 'zh-CN',
  base: '/',

  head: [
    ['meta', { name: 'theme-color', content: '#3c8772' }],
    ['link', { rel: 'icon', href: '/favicon.svg', type: 'image/svg+xml' }],
  ],

  themeConfig: {
    logo: '/favicon.svg',

    nav: [
      { text: '首页', link: '/' },
      { text: '指南', link: '/guide/introduction' },
      { text: '架构', link: '/architecture/overview' },
      { text: '配置', link: '/config/global' },
      { text: 'API', link: '/api/overview' },
      { text: '工程', link: '/eng' },
      { text: '许可证', link: '/license' },
    ],

    sidebar: {
      '/guide/': [
        {
          text: '快速开始',
          items: [
            { text: '介绍', link: '/guide/introduction' },
            { text: '安装', link: '/guide/installation' },
            { text: '快速上手', link: '/guide/quickstart' },
          ],
        },
        {
          text: '核心概念',
          items: [
            { text: '数据流', link: '/guide/data-flow' },
            { text: '驱动', link: '/guide/drivers' },
            { text: '传输', link: '/guide/transports' },
            { text: '链式核心', link: '/guide/chained-core' },
            { text: '规则引擎', link: '/guide/rules' },
            { text: '可观测性', link: '/guide/observability' },
          ],
        },
      ],
      '/architecture/': [
        {
          text: '架构设计',
          items: [
            { text: '总览', link: '/architecture/overview' },
            { text: '数据流图', link: '/architecture/dataflow' },
            { text: '高性能设计', link: '/architecture/performance' },
            { text: '核心输入输出', link: '/architecture/io' },
            { text: '架构决策记录', link: '/architecture/decisions' },
          ],
        },
      ],
      '/config/': [
        {
          text: '配置参考',
          items: [
            { text: '全局配置', link: '/config/global' },
            { text: '驱动配置', link: '/config/drivers' },
            { text: '传输配置', link: '/config/transports' },
            { text: '规则配置', link: '/config/rules' },
            { text: '派生标签配置', link: '/config/derived-tags' },
            { text: '完整示例', link: '/config/example' },
          ],
        },
      ],
      '/audits/': [
        {
          text: '审计记录',
          items: [
            { text: '功能审计（2026-10-19）', link: '/audits/backend-functionality-audit-2026-10-19' },
            { text: '质量审计（2026-10-21）', link: '/audits/redundancy-quality-audit-2026-10-21' },
            { text: '工程与质量中心', link: '/eng' },
          ],
        },
      ],
      '/exec-plans/': [
        {
          text: '执行计划',
          items: [
            { text: '质量提升计划（已完成）', link: '/exec-plans/completed/codebase-quality-program' },
            { text: '技术债跟踪', link: '/exec-plans/tech-debt-tracker' },
            { text: 'E2E 验证程序', link: '/exec-plans/active/e2e-verification-program' },
            { text: '已完成的计划', link: '/exec-plans/completed/production-hardening' },
            { text: '工程与质量中心', link: '/eng' },
          ],
        },
      ],
      '/api/': [
        {
          text: 'API 参考',
          items: [
            { text: '总览', link: '/api/overview' },
            { text: '驱动管理', link: '/api/drivers' },
            { text: '数据读写', link: '/api/data' },
            { text: '配置管理', link: '/api/config' },
            { text: '规则管理', link: '/api/rules' },
            { text: 'WebSocket', link: '/api/websocket' },
            { text: '告警', link: '/api/alerts' },
            { text: '统计监控', link: '/api/stats' },
            { text: 'API 契约（冻结快照）', link: '/api/COREC_API_CONTRACT' },
            { text: 'API 完整参考（冻结快照）', link: '/API_REFERENCE' },
          ],
        },
      ],
    },

    socialLinks: [
      { icon: 'github', link: 'https://github.com/CoreC-Dev/CoreC' },
    ],

    footer: {
      message: 'Released under the MIT License.',
      copyright: 'Copyright © 2026 CoreC',
    },

    search: {
      provider: 'local',
    },

    outline: {
      level: [2, 3],
      label: '本页目录',
    },

    docFooter: {
      prev: '上一页',
      next: '下一页',
    },

    lastUpdated: {
      text: '最后更新于',
    },

    darkModeSwitchLabel: '主题',
    sidebarMenuLabel: '菜单',
    returnToTopLabel: '回到顶部',
  },
})
