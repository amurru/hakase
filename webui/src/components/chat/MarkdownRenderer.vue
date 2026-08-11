<script setup lang="ts">
import MarkdownIt from 'markdown-it'
import DOMPurify from 'dompurify'
import { computed } from 'vue'

const props = defineProps<{
  content: string
}>()

const md = new MarkdownIt({
  html: false,
  linkify: true,
  typographer: true,
  breaks: true,
})

const rendered = computed(() => {
  if (!props.content) return ''
  const raw = md.render(props.content)
  return DOMPurify.sanitize(raw, {
    ADD_TAGS: ['iframe'],
    ADD_ATTR: ['target', 'allow', 'allowfullscreen', 'frameborder'],
  })
})
</script>

<template>
  <div
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
  />
</template>

<style>
.markdown-body pre {
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
</style>
