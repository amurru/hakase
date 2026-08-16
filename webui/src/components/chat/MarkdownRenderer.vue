<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { renderMarkdown } from '@/lib/markdown'
import { useMermaid } from '@/composables/useMermaid'

const props = defineProps<{
  content: string
  streaming?: boolean
}>()

const rootEl = ref<HTMLElement>()
const mermaid = useMermaid()

const rendered = computed(() => renderMarkdown(props.content))

function afterRender() {
  nextTick(() => {
    if (!rootEl.value) return
    attachCopyButtons(rootEl.value)
    mermaid.hydrate(rootEl.value)
  })
}

onMounted(afterRender)
watch(() => props.content, afterRender)
onBeforeUnmount(() => mermaid.dispose())

// ---------------------------------------------------------------------------
// Copy-to-clipboard on fenced code blocks. markdown-it renders `<pre><code>`,
// so we walk each pre and append a copy button (re-attached on every render
// delta; guarded to avoid double-adding).
// ---------------------------------------------------------------------------

function handleCopy(e: Event) {
  const btn = (e.target as HTMLElement).closest<HTMLElement>('.copy-code-button')
  if (!btn) return
  const pre = btn.closest<HTMLElement>('pre')
  const code = pre?.querySelector('code')
  if (!code) return

  const text = code.innerText
  
  // Try clipboard API first, fallback to textarea method
  const copyPromise = navigator.clipboard?.writeText(text)
  
  if (copyPromise) {
    // Modern browsers: use clipboard API, show copied state only on success
    copyPromise.then(() => {
      btn.classList.add('copied')
      window.setTimeout(() => btn.classList.remove('copied'), 1500)
    }).catch(() => {
      // Clipboard API failed, try fallback
      copyWithFallback(text)
    })
  } else {
    // No clipboard API, use fallback immediately
    copyWithFallback(text)
  }

  function copyWithFallback(text: string) {
    const ta = document.createElement('textarea')
    ta.value = text
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    ta.remove()
    if (btn) {
      btn.classList.add('copied')
      window.setTimeout(() => btn.classList.remove('copied'), 1500)
    }
  }
}

function attachCopyButtons(root: HTMLElement) {
  for (const pre of root.querySelectorAll<HTMLElement>('pre')) {
    if (pre.classList.contains('mermaid-error-code')) continue
    if (pre.querySelector('.copy-code-button')) continue
    if (!pre.querySelector('code')) continue

    const btn = document.createElement('button')
    btn.type = 'button'
    btn.className = 'copy-code-button'
    btn.setAttribute('aria-label', 'Copy code')
    btn.innerHTML = `
      <svg class="copy-icon" xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>
      <svg class="check-icon" xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>
    `
    btn.addEventListener('click', handleCopy)
    pre.appendChild(btn)
  }
}
</script>

<template>
  <div
    ref="rootEl"
    class="markdown-body prose prose-invert prose-sm max-w-none
           prose-headings:tracking-tight prose-headings:text-foreground
           prose-p:text-foreground/90 prose-p:leading-relaxed
           prose-a:text-primary prose-a:no-underline hover:prose-a:underline
           prose-strong:text-foreground prose-strong:font-semibold
           prose-code:text-amber-300/90 prose-code:font-mono prose-code:text-[0.85em]
           prose-code:bg-muted prose-code:px-1.5 prose-code:py-0.5 prose-code:rounded
           prose-code:before:content-none prose-code:after:content-none
           prose-pre:bg-transparent prose-pre:p-0 prose-pre:my-4
           prose-li:text-foreground/90
           prose-blockquote:border-l-primary/40 prose-blockquote:text-muted-foreground
           prose-hr:border-border"
    v-html="rendered"
    @click="handleCopy"
  />
</template>

<style>
.markdown-body pre {
  position: relative;
  background: hsl(var(--muted));
  border-radius: 0.5rem;
  padding: 1rem;
  overflow-x: auto;
  margin: 1rem 0;
}

.markdown-body pre code {
  background: transparent !important;
  padding: 0 !important;
  color: hsl(var(--foreground));
  font-size: 0.8125rem;
  line-height: 1.7;
}

.markdown-body table {
  width: 100%;
  border-collapse: collapse;
  margin: 1rem 0;
  font-size: 0.8125rem;
}

.markdown-body th,
.markdown-body td {
  border: 1px solid hsl(var(--border));
  padding: 0.5rem 0.75rem;
  text-align: left;
}

.markdown-body th {
  background: hsl(var(--muted));
  font-weight: 600;
}

.markdown-body img {
  border-radius: 0.5rem;
  max-width: 100%;
}

.markdown-body a {
  color: hsl(var(--primary));
}

.markdown-body ul {
  list-style-type: disc;
  padding-left: 1.5rem;
}

.markdown-body ol {
  list-style-type: decimal;
  padding-left: 1.5rem;
}

/* Copy button: hidden until the block is hovered. */
.markdown-body .copy-code-button {
  position: absolute;
  top: 0.5rem;
  right: 0.5rem;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 1.75rem;
  height: 1.75rem;
  border-radius: 0.375rem;
  border: 1px solid hsl(var(--border));
  background: hsl(var(--muted));
  color: hsl(var(--muted-foreground));
  cursor: pointer;
  opacity: 0;
  transition: opacity 0.15s ease, color 0.15s ease;
}
.markdown-body pre:hover .copy-code-button {
  opacity: 1;
}
.markdown-body .copy-code-button .check-icon {
  display: none;
  color: hsl(var(--chart-2));
}
.markdown-body .copy-code-button.copied .check-icon {
  display: block;
}
.markdown-body .copy-code-button.copied .copy-icon {
  display: none;
}

/* Mermaid rendering & fallback. */
.markdown-body .mermaid-render {
  margin: 1rem 0;
  overflow-x: auto;
  display: flex;
  justify-content: center;
  background: hsl(var(--muted));
  border-radius: 0.5rem;
  padding: 1rem;
}
.markdown-body .mermaid-render svg {
  max-width: 100%;
  height: auto;
}

.markdown-body .mermaid-placeholder {
  margin: 1rem 0;
  border-radius: 0.5rem;
  background: hsl(var(--muted));
  min-height: 3rem;
}

.markdown-body .mermaid-error-chip {
  display: inline-block;
  margin: 0.5rem 0 0 0.5rem;
  padding: 0.125rem 0.5rem;
  font-size: 0.6875rem;
  font-weight: 600;
  letter-spacing: 0.02em;
  text-transform: uppercase;
  color: hsl(var(--destructive-foreground));
  background: hsl(var(--destructive));
  border-radius: 0.25rem;
}

.markdown-body .mermaid-error-code {
  margin: 0.5rem;
  padding: 0.75rem;
  background: hsl(var(--muted));
  color: hsl(var(--muted-foreground));
  font-family: ui-monospace, monospace;
  font-size: 0.75rem;
  line-height: 1.6;
  white-space: pre-wrap;
  overflow-x: auto;
  border: 1px solid hsl(var(--border));
  border-radius: 0.375rem;
}
</style>