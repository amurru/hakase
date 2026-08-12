declare module 'markdown-it' {
  import MarkdownIt from 'markdown-it'
  export default MarkdownIt
  export = MarkdownIt
}

declare module 'markdown-it/lib/index.mjs' {
  import MarkdownIt from 'markdown-it'
  export default MarkdownIt
}
