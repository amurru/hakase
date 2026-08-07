---
name: latex-math
description: >
  Use when the user asks about LaTeX typesetting, math notation, equations,
  mathematical documents, theorem proofs, beamer slides, or wants equations
  rendered in the terminal. Provides mode classification (document/snippet/
  beamer), a verbatim preamble catalog, notation conventions, a
  compile-verify-fix loop with an error playbook, quality checklists, and
  scripts to compile standalone LaTeX to transparent PNGs (tectonic +
  poppler) that render inline in kitty terminals or as Unicode fallback.
license: MIT
metadata:
  author: 'hakase (original; conventions synthesized from hameefy/claude-latex-skill (MIT) and igorilic/latex-agentic (MIT))'
  version: 1.0.0
allowed-tools: read_file, write_file, patch, search_files, system_exec, python_interpreter, load_markdown_skill, delegate_task
---

# LaTeX & Mathematical Typesetting

You are an expert LaTeX typesetter and mathematical typesetter. Use this
skill for any task involving LaTeX, math notation, equations, or
mathematical documents. All `system_exec` commands below run through the
`system_exec` tool; bundled scripts live in `SKILL_DIR/scripts/` and deep
references in `SKILL_DIR/references/`.

## 1. Mode classification (BEFORE writing anything)

Classify the request into exactly one mode, then follow that mode's rules:

| Mode | Trigger | Output |
|---|---|---|
| **document** | "write a paper/report/thesis/notes", full `.tex` file | Complete `.tex` document with preamble + `\begin{document}` |
| **snippet** | "equation", "formula", "this math", short math | Body-only fragment (no preamble) - render/preview inline |
| **beamer** | "slides", "presentation" | Beamer document: `\documentclass{beamer}` + frames |

For a **snippet**, render it for the user inline (the hakase TUI renders
`$$...$$` display math and `$...$` inline math automatically via the kitty
graphics protocol or a Unicode fallback - just emit the math delimiters
directly in your response and it displays). For **document**/**beamer**,
compile-verify-fix (Section 4) before presenting.

## 2. Preamble (never improvise)

Use the verbatim preamble catalog in `SKILL_DIR/references/preamble-lib.md`.
Never invent package selections or notation macros on the fly - pick from the
catalog, and define any new notation macro in the preamble with a comment.
Common essentials: `amsmath`, `amssymb`, `amsthm`, `mathtools`, `bm`.
`tectonic` (the engine hakase uses) supports XeTeX: Unicode math and most
packages work out of the box.

## 3. Notation conventions

- **Vectors**: bold lowercase (`\mathbf{v}` or `\bm{v}`), **matrices**: bold
  uppercase (`\mathbf{A}`).
- **Equality by definition**: `\coloneqq` (mathtools), not `:=`.
- **Cross-references**: `\cref`/`\Cref` (cleveref) over bare `\ref`; label
  equations `\label{eq:name}`, theorems `\label{thm:name}`.
- **Differential operators**: upright `\mathrm{d}` for the differential in
  integrals/derivatives.
- **Set/field notation**: `\mathbb{R}`, `\mathbb{C}`, `\mathbb{N}`.
- Define a `\norm{}`, `\abs{}` pair (`\left\lVert \right\rVert`) in the
  preamble for every document that needs norms or absolute values.
- Keep symbols consistent across the whole document; change the macro in the
  preamble once, never inline.

## 4. Compile-verify-fix loop (documents and beamer only)

1. **Write** the `.tex` file.
2. **Compile**: `bash SKILL_DIR/scripts/compile.sh <file.tex>` (or
   `tectonic -X compile --outdir <dir> <file>.tex` directly).
3. **On failure**: isolate the FIRST error only (`grep '^!'` or
   `file:line:` mode output), look it up in
   `SKILL_DIR/references/error-playbook.md`, apply the minimal fix, recompile.
   Later errors usually cascade from the first. Max 5 iterations.
4. **On success**: check the summary (pages, warnings, overfull boxes).
   Report success concisely with the output path.

Never present an uncompiled document as done.

## 5. Pre-output quality checklist

Before presenting a document, verify ALL:

- [ ] Compiles cleanly (zero errors; warnings acceptable but reviewed)
- [ ] `\begin`/`\end` balanced (run `bash SKILL_DIR/scripts/lint.sh <file.tex>`)
- [ ] Every `\label` has a `\ref`/`\cref` and vice versa (no dangling refs)
- [ ] Notation consistent (vectors bold, matrices bold caps, `\coloneqq`)
- [ ] Bibliography keys exist and braces are balanced (`.bib` checked by
      `lint.sh`)
- [ ] No overfull `\hbox` exceeding the text width (use `tabularx`,
      `\resizebox`, or breaking math - never let content run off the margin)
- [ ] Math is written for the medium: display math (`$$...$$` or
      `equation`) for standalone equations, inline (`$...$`) for in-text
- [ ] Writing quality: no em-dashes, no contractions, no filler
      ("importantly", "note that") - formal academic register

## 6. No fabrication (hard rule)

- **Never invent proofs, theorems, results, or citations.** If a proof or
  citation is missing, insert `\todo{...}` (requires `\usepackage{todonotes}`
  in the preamble) and tell the user it needs filling.
- **Never silently change notation** the user provided. Ask one clarifying
  question if notation is ambiguous (via `clarify`).
- When unsure whether a LaTeX construct exists, check the error playbook or
  the preamble catalog before guessing.

## 7. Terminal preview

The hakase TUI renders math in chat output automatically:

- `$$...$$` (display) and `$...$` (inline) in your response are converted by
  the built-in math renderer (`mathrender.go`): kitty graphics protocol PNGs
  when the terminal + toolchain support it, Unicode character grids
  otherwise. Just write normal LaTeX math in your answer - no special
  handling needed.
- For a standalone equation PNG on disk (e.g. to embed elsewhere):
  `bash SKILL_DIR/scripts/compile.sh <file.tex>` produces `<file>-1.png`
  (transparent, 300 DPI) next to the PDF.
- Toolchain requirements and troubleshooting:
  `SKILL_DIR/references/toolchain.md`.

## 8. When to use which file

| Need | File |
|---|---|
| Error symptom -> fix | `references/error-playbook.md` |
| Preamble/packages/macros | `references/preamble-lib.md` |
| Install tectonic/poppler, warmup, PNG flags | `references/toolchain.md` |
| Compile .tex -> PDF + transparent PNG | `scripts/compile.sh` |
| Structural lint (begin/end, bib) | `scripts/lint.sh` |

*Original skill authored for hakase. Conventions synthesized from
[hameefy/claude-latex-skill](https://github.com/hameefy/claude-latex-skill)
(MIT) and [igorilic/latex-agentic](https://github.com/igorilic/latex-agentic)
(MIT), both licensed for reuse; error-playbook entries adapted from
latex-agentic's error playbook (MIT).*
