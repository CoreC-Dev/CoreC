<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed } from 'vue'

const props = defineProps<{
  src: string
  title?: string
  height?: string
}>()

// 基于 VitePress 的 base 自动拼接，避免写死 /CoreC/ 之类子路径导致 404
const resolvedSrc = computed(() =>
  props.src.startsWith('/')
    ? import.meta.env.BASE_URL.replace(/\/$/, '') + props.src
    : props.src
)

const isFullscreen = ref(false)
const modalRef = ref<HTMLElement | null>(null)

function openFullscreen() {
  isFullscreen.value = true
  document.body.style.overflow = 'hidden'
}

function closeFullscreen() {
  isFullscreen.value = false
  document.body.style.overflow = ''
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && isFullscreen.value) {
    closeFullscreen()
  }
}

onMounted(() => {
  document.addEventListener('keydown', onKeydown)
})

onUnmounted(() => {
  document.removeEventListener('keydown', onKeydown)
  document.body.style.overflow = ''
})
</script>

<template>
  <div class="diagram-frame-wrapper">
    <!-- 预览区 -->
    <div class="diagram-preview" @click="openFullscreen">
      <iframe
        :src="resolvedSrc"
        :style="{ height: height || '720px' }"
        frameborder="0"
        loading="lazy"
      ></iframe>
      <div class="diagram-overlay">
        <div class="diagram-overlay-badge">
          <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <path d="M8 3H5a2 2 0 0 0-2 2v3M21 8V5a2 2 0 0 0-2-2h-3M3 16v3a2 2 0 0 0 2 2h3M16 21h3a2 2 0 0 0 2-2v-3" />
          </svg>
          <span>点击全屏查看</span>
        </div>
      </div>
    </div>

    <!-- 全屏浮窗 -->
    <Teleport to="body">
      <div
        v-if="isFullscreen"
        ref="modalRef"
        class="diagram-modal"
        @click.self="closeFullscreen"
      >
        <div class="diagram-modal-header">
          <span class="diagram-modal-title">{{ title || '架构图' }}</span>
          <div class="diagram-modal-actions">
            <a
              :href="resolvedSrc"
              target="_blank"
              rel="noopener"
              class="diagram-modal-btn"
              title="在新标签页打开"
            >
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <path d="M15 3h6v6M10 14L21 3M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" />
              </svg>
            </a>
            <button
              class="diagram-modal-btn"
              @click="closeFullscreen"
              title="关闭 (Esc)"
            >
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <line x1="18" y1="6" x2="6" y2="18" />
                <line x1="6" y1="6" x2="18" y2="18" />
              </svg>
            </button>
          </div>
        </div>
        <div class="diagram-modal-body">
          <iframe
            :src="resolvedSrc"
            frameborder="0"
          ></iframe>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.diagram-frame-wrapper {
  margin: 24px 0;
}

.diagram-preview {
  position: relative;
  width: 100%;
  border: 1px solid var(--vp-c-border);
  border-radius: 8px;
  overflow: hidden;
  cursor: pointer;
  transition: border-color 0.2s;
}

.diagram-preview:hover {
  border-color: var(--vp-c-brand-1);
}

.diagram-preview iframe {
  width: 100%;
  display: block;
  pointer-events: none;
}

.diagram-overlay {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: flex-end;
  justify-content: center;
  padding-bottom: 16px;
  background: linear-gradient(to bottom, transparent 85%, rgba(0,0,0,0.08));
  opacity: 0;
  transition: opacity 0.2s;
}

.diagram-preview:hover .diagram-overlay {
  opacity: 1;
}

.diagram-overlay-badge {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 14px;
  background: var(--vp-c-bg);
  border: 1px solid var(--vp-c-border);
  border-radius: 20px;
  font-size: 13px;
  font-weight: 500;
  color: var(--vp-c-text-1);
  box-shadow: 0 2px 8px rgba(0,0,0,0.12);
}

/* 全屏浮窗 */
.diagram-modal {
  position: fixed;
  inset: 0;
  z-index: 9999;
  background: rgba(0, 0, 0, 0.7);
  backdrop-filter: blur(4px);
  display: flex;
  flex-direction: column;
  animation: diagram-fade-in 0.2s ease;
}

@keyframes diagram-fade-in {
  from { opacity: 0; }
  to { opacity: 1; }
}

.diagram-modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 20px;
  background: var(--vp-c-bg);
  border-bottom: 1px solid var(--vp-c-border);
  flex-shrink: 0;
}

.diagram-modal-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--vp-c-text-1);
}

.diagram-modal-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.diagram-modal-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 36px;
  height: 36px;
  border: 1px solid var(--vp-c-border);
  border-radius: 8px;
  background: var(--vp-c-bg);
  color: var(--vp-c-text-2);
  cursor: pointer;
  transition: all 0.15s;
  text-decoration: none;
}

.diagram-modal-btn:hover {
  color: var(--vp-c-brand-1);
  border-color: var(--vp-c-brand-1);
}

.diagram-modal-body {
  flex: 1;
  overflow: hidden;
  padding: 4px;
}

.diagram-modal-body iframe {
  width: 100%;
  height: 100%;
  border: none;
  border-radius: 4px;
}
</style>
