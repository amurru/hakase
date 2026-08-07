# Verbatim Preamble Catalog

Use these preambles **verbatim** - do not improvise package selections.
Conventions synthesized from hameefy/claude-latex-skill (MIT) and
latex-agentic (MIT).

## A. Minimal math document (snippets, short notes)

```latex
\documentclass[12pt]{article}
\usepackage[margin=1in]{geometry}
\usepackage{amsmath, amssymb, amsthm, mathtools}
\usepackage{bm}
\usepackage{graphicx}
\usepackage[colorlinks=true, allcolors=blue]{hyperref}
\usepackage[capitalize, nameinlink]{cleveref}
\usepackage{booktabs, tabularx}
\usepackage{siunitx}
\usepackage{microtype}
\usepackage{todonotes}
\title{}
\author{}
\date{\today}
```

## B. Paper / report

```latex
\documentclass[11pt]{article}
\usepackage[margin=1in]{geometry}
\usepackage{amsmath, amssymb, amsthm, mathtools}
\usepackage{bm}
\usepackage{graphicx}
\usepackage{booktabs, tabularx, multirow}
\usepackage{siunitx}
\usepackage[colorlinks=true, allcolors=blue]{hyperref}
\usepackage[capitalize, nameinlink]{cleveref}
\usepackage{microtype}
\usepackage[numbers]{natbib}
\usepackage{todonotes}
\bibliographystyle{plainnat}
```

## C. Theorems (amsthm)

```latex
\theoremstyle{plain}   % italic body: theorem, lemma, corollary, proposition
\newtheorem{theorem}{Theorem}[section]
\newtheorem{lemma}[theorem]{Lemma}
\newtheorem{corollary}[theorem]{Corollary}
\newtheorem{proposition}[theorem]{Proposition}
\theoremstyle{definition} % upright body: definition, example, remark
\newtheorem{definition}[theorem]{Definition}
\newtheorem{example}[theorem]{Example}
\newtheorem{remark}[theorem]{Remark}
```

## D. Beamer slides

```latex
\documentclass{beamer}
\usetheme{Madrid}
\usepackage{amsmath, amssymb, amsthm, mathtools}
\usepackage{bm}
\usepackage{booktabs, tabularx}
\usepackage[capitalize, nameinlink]{cleveref}
\title{}
\author{}
\date{\today}
```

## E. Standalone equation (for PNG rendering)

```latex
\documentclass[12pt]{standalone}
\usepackage{amsmath}
\usepackage{amssymb}
\begin{document}
$<EQUATION>$
\end{document}
```

## Notation macros (add to any document with vectors/norms)

```latex
% Norms and absolute values (scaled delimiters)
\DeclarePairedDelimiter{\abs}{\lvert}{\rvert}
\DeclarePairedDelimiter{\norm}{\lVert}{\rVert}
% Inner product
\DeclarePairedDelimiter{\ip}{\langle}{\rangle}
% Expectation / variance
\newcommand{\E}{\mathbb{E}}
\newcommand{\Var}{\operatorname{Var}}
% Common sets
\newcommand{\R}{\mathbb{R}}
\newcommand{\C}{\mathbb{C}}
\newcommand{\N}{\mathbb{N}}
\newcommand{\Z}{\mathbb{Z}}
\newcommand{\Q}{\mathbb{Q}}
% Differential (upright)
\newcommand{\dd}{\mathrm{d}}
% Big-O
\newcommand{\bigO}{\mathcal{O}}
```

Usage: `\norm{v}`, `\abs{x}`, `\ip{u}{v}`, `\E[X]`, `\R^n`, `\int f(x)\,\dd x`.

## Package cheat sheet

| Package | Provides | Use for |
|---|---|---|
| `amsmath` | `align`, `equation`, `pmatrix`..`Vmatrix`, `cases`, `\DeclarePairedDelimiter` | Everything math |
| `amssymb` | `\mathbb`, `\leqq`, `\varnothing` | Symbol coverage |
| `amsthm` | `\newtheorem`, theorem styles | Theorems/lemmas |
| `mathtools` | `\coloneqq`, `\DeclarePairedDelimiter`, `\mathclap` | Delimiters, definition-equals |
| `bm` | `\bm{}` | Bold math (better than `\mathbf` for symbols) |
| `graphicx` | `\includegraphics` | Figures |
| `booktabs` | `\toprule`, `\midrule`, `\bottomrule` | Publication tables |
| `tabularx` | `X` column | Tables that fit `\textwidth` |
| `siunitx` | `\SI{}{}`, `\num{}` | Units, numbers |
| `cleveref` | `\cref`, `\Cref` | Smart cross-references |
| `natbib` | `\citep`, `\citet` | Citations (author-year) |
| `hyperref` | links, PDF metadata | Always last (except cleveref) |
| `microtype` | character protrusion | Print quality |
| `todonotes` | `\todo{}` | Placeholders for missing content |

## Ordering rule

`hyperref` goes AFTER most packages but BEFORE `cleveref`. `microtype` is
safe anywhere. Load order matters for `cleveref` vs `hyperref` and for
`mathtools` vs `amsmath` (mathtools loads amsmath).

## Tectonic compatibility

tectonic bundles most of CTAN, including all packages above. Unicode math
and `fontspec` (XeTeX) work. For `siunitx`/`biblatex` see the error playbook
if a feature is unavailable.
