# Toolchain Reference

The hakase math renderer compiles LaTeX to transparent PNGs with
**tectonic** (Rust TeX engine, ~15 MiB) + a **poppler** PDF-to-PNG converter
(`pdftocairo` preferred, `pdftoppm` fallback, ~7 MiB). Total ~22 MiB -
about 1/10 the size of a full texlive install.

## Install per distro

| Distro/OS | Command |
|---|---|
| Arch Linux | `sudo pacman -S tectonic poppler` |
| Debian/Ubuntu | `sudo apt install tectonic poppler-utils` |
| Fedora | `sudo dnf install tectonic poppler-utils` |
| macOS | `brew install tectonic poppler` |
| Any (static binaries) | tectonic: download musl static release from `github.com/tectonic-typesetting/tectonic/releases` (~9.7 MiB compressed, zero deps); poppler: use the distro package |

## Verify

```bash
tectonic --version && pdftocairo -h | grep -i transp
```

## First-run warmup

tectonic downloads its bundle (packages + fonts) on the first compile, which
can take a minute and needs network. Warm the cache once so later renders are
offline:

```bash
printf '%s\n' '\documentclass{article}' '\begin{document}' 'Hello' '\end{document}' > /tmp/warmup.tex
tectonic -X compile --outdir /tmp /tmp/warmup.tex
```

The cache lives in `~/.cache/Tectonic`; delete it to force a re-download.

## Compile to transparent PNG

`scripts/compile.sh` wraps this:

```bash
# standalone .tex -> PDF (tectonic)
tectonic -X compile --outdir <dir> <file>.tex

# PDF -> transparent PNG, 300 DPI (pdftocairo, modern poppler)
pdftocairo -png -singlefile -transp -r 300 <file>.pdf <file>

# Fallback for older poppler without -transp on pdftocairo:
pdftoppm -png -r 300 -transp <file>.pdf <file>   # writes <file>-1.png
```

Outputs: `<file>.pdf`, `<file>.png` (or `<file>-1.png` with pdftoppm).

Notes:
- **`-transp` was removed from `pdftoppm` in poppler 26.x** - use
  `pdftocairo -transp` on modern systems; hakase's renderer probes both.
- Transparent PNGs are required for kitty rendering (the terminal background
  shows through); a white-background PNG will render as a white box.

## How hakase uses this (context)

- `mathrender.go` detects `tectonic` + `pdftocairo`/`pdftoppm` at startup via
  `detectMathToolchain()`; if either is missing, the kitty PNG path is
  disabled and display math renders as Unicode character grids instead
  (never fatal).
- Kitty protocol detection is env-based: `KITTY_WINDOW_ID`, `TERM=xterm-kitty`,
  or `TERM_PROGRAM` in {WezTerm, ghostty, kitty}.
- Display math `$$...$$` compiles to PNG once per unique equation (hash-cached
  per session) and transmits via kitty virtual placement; inline `$...$`
  always uses Unicode.

## Future / optional MCP integration

hakase is an MCP client (`config.json` `mcp.servers`). Optional LaTeX MCP
servers can be wired for Overleaf workflows or arXiv source fetching:

- **OverleafMCP** (`npx -y @mjyoo2/overleaf-mcp`): read/write Overleaf
  projects via git.
- **arxiv-latex-mcp**: fetch arXiv LaTeX source for accurate math
  interpretation (PDF chat struggles with math; the source is exact).
- **WebLatexMCP**: read/edit/compile/commit LaTeX in git-hosted projects.

These are optional conveniences - the local tectonic pipeline covers
compile-and-preview without any MCP server.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `Unable to find bundle` on first run | Network needed once; run the warmup above |
| `command not found` after install | Re-login or export PATH; on Arch use `/usr/bin/tectonic` |
| White box instead of equation in kitty | PNG lacks alpha - use `pdftocairo -transp`, not plain `pdftoppm -png` |
| Very slow first render | Bundle download; subsequent renders are cached |
| `rm ~/.cache/Tectonic` needed | Corrupt/partial bundle after interrupted download |
