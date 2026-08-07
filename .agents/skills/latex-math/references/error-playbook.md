# LaTeX Error Playbook

Symptom -> cause -> fix reference for the most common LaTeX errors. Entries
adapted from the latex-agentic project's error playbook
(https://github.com/igorilic/latex-agentic, MIT) and extended for the hakase
tectonic toolchain.

## How to use

1. Compile with `tectonic -X compile --outdir <dir> <file>.tex`.
2. Isolate the **FIRST** error only. Use `grep '^!' <log>` or look for the
   `file:line:` prefix in nonstop-mode output.
3. Look up the symptom below, apply the minimal fix, recompile.
4. Later errors usually cascade from the first - fix one at a time, max 5
   iterations before asking the user.

## Compile/engine errors

| # | Symptom | Cause | Fix |
|---|---|---|---|
| 1 | `tectonic: command not found` | Engine not installed | `sudo pacman -S tectonic` / `brew install tectonic` / download static binary (see `toolchain.md`) |
| 2 | `Fatal error: Unable to find bundle` / network fetch on first run | Tectonic bundle not downloaded yet | First compile downloads it (needs network once). Run `tectonic -X compile` on a trivial doc to warm the cache. |
| 3 | `error: failed to run engine` / `panic` from tectonic | Corrupt cache or stale bundle | `rm -rf ~/.cache/Tectonic` and recompile (re-downloads). |
| 4 | `Couldn't find font ...` | Missing font for the engine | Install `fontconfig` + common fonts (`ttf-liberation`, `noto-fonts`). Tectonic bundles its own TeX fonts but system fonts are used for fontspec. |

## Syntax errors (most common)

| # | Symptom | Cause | Fix |
|---|---|---|---|
| 5 | `! Missing $ inserted.` | Math command outside math mode | Wrap in `$...$` (inline) or `\[...\]`/`equation` (display). |
| 6 | `! Extra }, or forgotten $.` | Unbalanced brace or `$` | Count braces in the line; the log shows the line. Balance them. |
| 7 | `! Missing } inserted.` | `\left` without matching `\right` (or mismatched types) | Every `\left(` needs a matching `\right)` on the same logical line. |
| 8 | `! Argument of \foo has an extra }.` | Macro got `}` before its argument completed | Check the macro call - often a missing `{` before an argument. |
| 9 | `! You can't use \eqno in math mode.` | `\begin{equation}` nested or misplaced | Use `equation` only at top level; for aligned use `align`/`aligned`. |
| 10 | `! Misplaced alignment tab character &.` | `&` outside an alignment env (tabular/align) | Escape as `\&` in normal text, or move into an alignment environment. |
| 11 | `! LaTeX Error: \begin{document} ended by \end{...}` | Missing `\end{document}` or wrong env order | Close all environments before `\end{document}`. |
| 12 | `! Undefined control sequence.` | Unknown command/typo | Check spelling. Common: `\matbb` vs `\mathbb`, `\lamba` vs `\lambda`. Add `\usepackage{amsmath,amssymb,amsthm,mathtools}` if the command lives there. |
| 13 | `! Environment ... undefined.` | Missing package for the environment | `align`/`equation` -> amsmath; `theorem` -> amsthm; `matrix` variants -> amsmath. |
| 14 | `! LaTeX Error: File 'xxx.sty' not found.` | Package not available to the engine | Tectonic bundles most CTAN packages. If missing, use an alternative or `\usepackage{xxx}` after confirming availability. |
| 15 | `! Package amsmath Error: \begin{aligned} allowed only in math mode.` | `aligned` outside math | Wrap in `\[ ... \]` or `equation`. |
| 16 | `! Package inputenc Error: Unicode character ... not set up` | Literal Unicode char with pdflatex-style engine | Tectonic is XeTeX - Unicode works. If the error persists, wrap the char in `\text{}` or use `\symbol{}`. |
| 17 | `! LaTeX Error: Too many unprocessed floats.` | Too many figures/tables queued | Add `\clearpage` or move some floats, reduce `\begin{figure}` count. |
| 18 | `! LaTeX Error: Something's wrong--perhaps a missing \item.` | `\item` missing in a list, or empty list | Add `\item` to `itemize`/`enumerate`/`description`, or remove the empty list. |
| 19 | `! LaTeX Error: \caption outside float.` | `\caption` in a non-float | Wrap figure/table content in `figure`/`table` environment, or use `caption` package's `\captionof`. |
| 20 | `! Dimension too large.` | Numeric overflow (e.g. `\resizebox` with bad ratio, huge `\scalebox`) | Reduce the value; use `tabularx` instead of manual widths. |

## Math-specific

| # | Symptom | Cause | Fix |
|---|---|---|---|
| 21 | `! Missing { inserted.` around `\frac` | `\frac` with fewer than 2 args | `\frac{a}{b}` needs exactly two braced args. |
| 22 | `! \sqrt needs one argument` / missing radicand | `\sqrt` with no `{...}` | `\sqrt{x}`, optional root `\sqrt[n]{x}`. |
| 23 | `! \sum is not defined` | `sum`/`int`/`prod` used without math mode | They are math-only; wrap in `$...$`. |
| 24 | `! Package amsmath Error: Multiple \label's` | Duplicate label | Make labels unique (`eq:energy`, `eq:mass`, ...). |
| 25 | `! LaTeX Error: There's no line here to end.` | `\\` at line start or after `\centering` | Use `\par` or blank line instead of `\\`. |
| 26 | `! Package amsmath Error: \begin{pmatrix} allowed only in math mode.` | Matrix env outside math | Wrap in `$...$`/`\[...\]`. |
| 27 | `! Extra alignment tab has been changed to \cr.` | Too many `&` in a matrix/table row | Match the column spec (`{cc}` = 2 columns, `{ccc}` = 3, ...). |
| 28 | `! Missing \right. inserted.` | `\left` with no matching `\right` across a line break | Close with `\right.` (invisible) or `\left.` at the other end. |
| 29 | `! Undefined control sequence.` for `\coloneqq`/`\norm`/`\abs` | Macro not defined | Add `\usepackage{mathtools}` (`\coloneqq`) or define `\norm`/`\abs` in the preamble (see `preamble-lib.md`). |
| 30 | `! Display math should end with $$.` | `$$` unbalanced (legacy mode) | Use `\[...\]` or `equation`; never mix `$$` with `equation`. |

## Bibliography (.bib)

| # | Symptom | Cause | Fix |
|---|---|---|---|
| 31 | `! Package natbib Error: Bibliography not compatible with author-year citations` | natbib option mismatch | Use `\usepackage[numbers]{natbib}` or switch to `biblatex` consistently. |
| 32 | `! Package biblatex Error: The 'backend=biber' package option is not supported.` | biblatex backend unavailable | Tectonic bundles biber support via `\usepackage[backend=biber]{biblatex}`; if it fails, fall back to natbib + `\bibliography{refs}`. |
| 33 | `Warning: Citation 'key' undefined` | Key missing in .bib or wrong key | Verify the key exists; run `bash SKILL_DIR/scripts/lint.sh refs.bib` (checks brace balance + duplicate keys). |
| 34 | `Warning: There were undefined references.` | `\cite`/`\ref` without a matching key/label | Run bibtex/biber again after adding the entry; check spelling. |
| 35 | `! Package biblatex Error: Cannot find control file` | biblatex needs multiple passes | Run `tectonic -X compile` twice (tectonic handles multi-pass automatically; if not, use `latexmk`). |

## Layout/quality

| # | Symptom | Cause | Fix |
|---|---|---|---|
| 36 | `Overfull \hbox (xxx too wide)` | Content wider than text area | Use `tabularx`, `\resizebox{\textwidth}{!}{...}`, or break the math with `\allowbreak`/aligned env. |
| 37 | `Underfull \hbox` (warning) | Ragged spacing (usually harmless) | Cosmetic; fix only if visibly bad - add `\hfill` or rephrase. |
| 38 | `! LaTeX Error: Cannot determine size of graphic` | Graphic file missing/wrong path | Check the path and extension (`.pdf`, `.png`, `.jpg`). |
| 39 | `! LaTeX Error: Unknown graphics extension` | Extension not registered | Use `\usepackage{graphicx}` and a supported extension. |
| 40 | `! TeX capacity exceeded` | Too many macros/boxes (infinite recursion usually) | Look for a self-referencing macro (`\def\foo{\foo}`) or runaway argument. |

## Tectonic-specific notes

- Tectonic runs silently by default; add `-Z continue-on-errors` to collect
  all errors instead of stopping at the first.
- Tectonic does not support `\includegraphics` with system paths outside the
  project dir unless `-Z shell-escape` is set (avoid unless needed).
- On Arch, tectonic is in the `extra` repo (`pacman -S tectonic`); poppler
  tools (`pdftocairo`/`pdftoppm`) are in `poppler`.
- If a package genuinely does not exist in the tectonic bundle, prefer a
  bundled equivalent over shell-escape workarounds.
