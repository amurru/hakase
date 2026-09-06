# Browser MCP presets

hakase is an MCP client: any spec-compliant browser automation MCP server is a
config-only swap, and the browsing stack runs natively on Windows. Four
ready-to-use presets follow. Paste one block into the `mcp.servers` map of
`config.json` (project scope) or `~/.hakase/mcp.json` (user registry), then
keep `web_search`/`web_fetch` visible to `web_researcher` via
`tools.include`/`tools.exclude` as shown.

The legacy single-server `mcp_server_url` field still works and auto-migrates
to a server literally named `lightpanda`, so existing configs keep working.

## Built-in fallback (no MCP needed)

When no connected MCP server exposes research-capable tools (browser
automation or web search/fetch), hakase automatically exposes built-in
keyless tools to the orchestrator and `web_researcher`: `web_search`
(DuckDuckGo HTML results supplemented with Wikipedia matches, each labeled
by source) and `web_fetch` (page content as markdown via the keyless Jina
Reader, with a direct-fetch fallback; no JavaScript rendering or logins).
Connecting any browser/search MCP hides the fallback again - per run - so
the presets above always take precedence when present.

Config (project `config.json`):

```json
{
  "web_search": {
    "enabled": true,
    "force": false
  }
}
```

`enabled: false` disables the fallback and all of its outbound calls;
`force: true` keeps the fallback visible even when research MCP tools are
connected.

## Lightpanda (default when available - headless, fast, cheap)

Labeled **default-when-available**: lightest resource footprint, fully
headless, ideal for research/navigation tasks. Install from
[lightpanda.ai](https://lightpanda.ai), then start the MCP server in HTTP
mode (the MCP server defaults to stdio; `--port` switches it to the HTTP
endpoint the preset below connects to):

```bash
lightpanda mcp --port 9223
```

```json
{
  "mcp": {
    "servers": {
      "lightpanda": {
        "type": "http",
        "url": "http://localhost:9223/mcp",
        "tools": {
          "include": ["mcp_lightpanda_search", "mcp_lightpanda_markdown"]
        }
      }
    }
  }
}
```

## chrome-devtools-mcp (controlled real browser - Edge on Windows, Chrome elsewhere)

Drives a real Chrome-family browser through the DevTools protocol: richer DOM
inspection, screenshots, and profiling, at the cost of a full browser
process. On Windows it runs on **Edge** (`msedge`), which ships with the OS -
pass the browser binary via the supported `--executablePath` flag (the server
does not read `CHROME_PATH`):

```json
{
  "mcp": {
    "servers": {
      "browser": {
        "type": "stdio",
        "command": [
          "npx", "-y", "chrome-devtools-mcp@latest",
          "--executablePath", "C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe"
        ],
        "tools": { "exclude": ["mcp_browser_performance_start_trace"] }
      }
    }
  }
}
```

## @playwright/mcp (headless or headed automation)

Playwright's MCP server: deterministic selectors, multi-tab flows, and a
headless mode for CI-like environments. Remove the `--headless` flag for a
headed browser window.

```json
{
  "mcp": {
    "servers": {
      "browser": {
        "type": "stdio",
        "command": ["npx", "-y", "@playwright/mcp@latest", "--headless"],
        "tools": { "exclude": ["mcp_browser_browser_pdf_save"] }
      }
    }
  }
}
```

## @browsermcp/mcp (your real session - signed-in state)

Attaches to **your** everyday browser profile, so authenticated pages and
cookies are visible to the agent. Highest-fidelity option; also the highest
blast radius - you are pointing the agent at your live session.

Prerequisite: install the [Browser MCP Chrome
extension](https://chromewebstore.google.com/detail/browser-mcp-automate-your/bjfgambnhccakkhmkepdoekmckoijdlc),
then click **Connect** in the extension to link it to the local MCP server -
the server below only bridges to the extension, so authenticated access does
not work without it.

```json
{
  "mcp": {
    "servers": {
      "browser": {
        "type": "stdio",
        "command": ["npx", "-y", "@browsermcp/mcp@latest"],
        "tools": { "exclude": [] }
      }
    }
  }
}
```

## Shaping tools for web_researcher

The `web_researcher` sub-agent inherits whatever tools its MCP manager is
configured with. Use `tools.include` to pin the research surface to search +
fetch, and `tools.exclude` to drop destructive or noisy tools (`exclude`
wins over `include`). Names are namespaced `mcp_<server>_<tool>`, so the
server name in your config prefixes every tool it exposes.

## Runtime tradeoffs at a glance

| Preset | Browser model | Headless | Signed-in state | Best for |
| --- | --- | --- | --- | --- |
| Lightpanda | dedicated headless engine | yes | no | default research/navigation |
| chrome-devtools-mcp | real browser (Edge/Chrome) | optional | optional profile | deep DOM work, profiling |
| @playwright/mcp | real browser | optional | optional profile | deterministic multi-tab flows |
| @browsermcp/mcp | your live browser | no | yes | authenticated pages |
