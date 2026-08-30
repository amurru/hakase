# Browser MCP presets

hakase is an MCP client: any spec-compliant browser automation MCP server is a
config-only swap, and the browsing stack runs natively on Windows. Four
ready-to-use presets follow. Paste one block into the `mcp.servers` map of
`config.json` (project scope) or `~/.hakase/mcp.json` (user registry), then
keep `web_search`/`web_fetch` visible to `web_researcher` via
`tools.include`/`tools.exclude` as shown.

The legacy single-server `mcp_server_url` field still works and auto-migrates
to a server literally named `lightpanda`, so existing configs keep working.

## Lightpanda (default when available - headless, fast, cheap)

Labeled **default-when-available**: lightest resource footprint, fully
headless, ideal for research/navigation tasks. Install from
[lightpanda.ai](https://lightpanda.ai) and start it before running the agent
(serves the MCP endpoint on `localhost:9223` by default).

```json
{
  "mcp": {
    "servers": {
      "lightpanda": {
        "type": "http",
        "url": "http://localhost:9223/mcp",
        "tools": {
          "include": ["mcp_lightpanda_web_search", "mcp_lightpanda_web_fetch"]
        }
      }
    }
  }
}
```

## chrome-devtools-mcp (controlled real browser - Edge on Windows, Chrome elsewhere)

Drives a real Chrome-family browser through the DevTools protocol: richer DOM
inspection, screenshots, and profiling, at the cost of a full browser
process. On Windows it runs on **Edge** (`msedge`), which ships with the OS.

```json
{
  "mcp": {
    "servers": {
      "browser": {
        "type": "stdio",
        "command": ["npx", "-y", "chrome-devtools-mcp@latest"],
        "env": { "CHROME_PATH": "C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe" },
        "tools": { "exclude": ["mcp_browser_performance_trace"] }
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
        "tools": { "exclude": ["mcp_browser_pdf"] }
      }
    }
  }
}
```

## @browsermcp/mcp (your real session - signed-in state)

Attaches to **your** everyday browser profile, so authenticated pages and
cookies are visible to the agent. Highest-fidelity option; also the highest
blast radius - you are pointing the agent at your live session.

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
