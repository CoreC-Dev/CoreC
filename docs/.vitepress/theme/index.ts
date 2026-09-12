import DefaultTheme from 'vitepress/theme'
import type { Theme } from 'vitepress'
import DiagramFrame from './components/DiagramFrame.vue'
import './style.css'

export default {
  extends: DefaultTheme,
  enhanceApp({ app }) {
    app.component('DiagramFrame', DiagramFrame)
  }
} satisfies Theme
