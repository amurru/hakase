# Project management in the web UI — research & recommended design

Research date: 2026-09-03. Companion: `project-registry.md` (P1-P5 done).
This is the **research** deliverable for the next phase: a project-management
surface in the hakase web UI (currently projects are registered via CLI or
API only; sessions bind via the New Session dialog). Sources: primary docs
from OpenHands, Devin, Cursor, GitHub Copilot, Codex, Claude Code web, GitHub
Codespaces, DevPod, Coder; Gitpod docs are offline (domain redirects to Ona)
so its lineage is covered via Ona + Codespaces.

**Implementation status (2026-09-03):** the recommended design below is
shipped as the web UI `/projects` page (UI-first over the P3 endpoints). The
deferred items are now also implemented:
- **Behind-upstream affordance** - `GET /api/projects/{id}/status` reports a
  ready checkout's branch/upstream, ahead/behind (after a bounded best-effort
  fetch, so opening the page refreshes counts), and the staged/modified/
  untracked/conflicts counts. The Projects page shows `N behind` / `N ahead`
  badges per ready row on open and refreshes them after each sync.
  `?fetch=0` gives the same read without network I/O for the chat chip.
- **Sync safety guards (server-side)** - `/sync` refuses with a 409 (never a
  `sync_error` transition) when an agent run is active on that project, when
  the checkout has uncommitted **tracked** changes (`ErrWorkingTreeDirty`;
  untracked files alone never block a pull), or when another sync is already
  running on the project (`ErrBusy`, per-project slot - the "page refresh
  mid-sync" double-run guard). Delete refuses under an active run or in-flight
  sync too.
- **Chat header chip extension** - the bound-project chip now shows the
  checkout's live branch and an amber dirty dot (counts in the tooltip), read
  local-only (`?fetch=0`) when the conversation opens.

See `webui/src/views/ProjectsView.vue`, `webui/src/views/ChatView.vue`,
`webui/src/stores/projects.ts`, `internal/registry/service.go`,
`internal/web/handlers/project.go`, and the CHANGELOG.

## 1. How the field does it

**Repo selection is the launch step, and it is immutable for that session.**
OpenHands (home screen: local folder browser *or* GitHub repo+branch picker),
Devin (repo selector on the new-session page, repos "added to Devin's
machine"), Cursor cloud agents and Claude Code web (pick repo at task start,
clone into an ephemeral env, deliver a branch/PR), Codex (pick a *persistent
per-repo environment*, then start tasks against it), Copilot coding agent
(repo dropdown in the prompt field, works in an Actions-run env via a draft
PR). No product re-points an already-contextualized conversation at a
different repo; switching projects means starting a new session/environment.

**Import is either "connect a provider and pick repos" or "paste a URL" — the
two coexist.** OpenHands offers `+ Add GitHub Repos` (GitHub App install,
fine-grained repo selection, short-lived tokens) *and* accepts a pasted git
URL in the same field. Repo import is gated by the **host identity/scope**
layer (GitHub App install scope, PAT repo scope, org Codespaces policy),
never by a URL allowlist stored in the product.

**Credentials ride the host, never the app config.** Every product confirmed:
git in the sandbox authenticates via the user's own OAuth/GitHub-App token,
an injected `GITHUB_TOKEN` + git credential helper (Codespaces), or SSH agent
forwarding (DevPod). Ona states it outright: "SCM credentials stay on
runners, not the management plane." Nothing stores per-repo passwords; cloud
products store only the session token needed to drive the integration.

**Sync is user-invoked and lightweight, not auto-pull-on-open.**
OpenHands' `GitControlBar` (repo + branch + push/pull/open-PR under the chat),
Replit's git panel, Codespaces' in-editor **Pull** + "N commits behind"
status-bar counter (with Git: Autofetch). Freshness is surfaced (ahead/behind,
dirty dot, last-synced), pulling stays explicit, and a dirty tree or running
agent guards the pull.

**Deletion is environment-scoped and always remote-safe.** Codespaces /
DevPod / Coder / Ona: deleting a codespace/workspace deletes only the local
environment; "the repo is untouched" is the universal guarantee, and
deletion prompts warn about unpushed work. Unlinking (revoking access /
dropping registration) is distinct from deleting on-disk state.

**Isolation = one environment per repo; workspace root is internal.**
Each project gets its own container/worktree/sandbox. The UI labels the
project by git identity (repo slug + branch); the absolute checkout path is
an internal/secondary detail. hakase's existing model (one managed checkout
under `~/.hakase/projects/<id>` per registered project, per-run sandbox
pinned to it since P5) already matches this.

## 2. Recommended design for hakase

Mapping the field patterns onto what hakase already ships (registry +
`/api/projects` CRUD + sync + per-session binding + per-run sandbox pinning),
the coherent next surface is a **Projects page** (not a heavy sidebar):

- **Projects page (list/management):** card/table per registered project —
  name (git identity), source URL, ref, status (ready / cloning / sync_error),
  checkout path as secondary line/tooltip, last-synced time, and per-row
  actions: **Sync now** (`POST /{id}/sync`), **New chat on this project**
  (creates a bound session), **Unlink** (drop the entry) and **Delete**
  (drop + remove checkout, DP-10), the destructive ones behind a
  confirmation that warns "the remote repository is never touched".
  **Register** dialog = name + clone URL (+ optional ref) calling
  `POST /api/projects`; failure leaves a `sync_error` row with the bounded
  error visible so the row itself is the retry surface.
- **"Behind upstream" affordance:** on the Projects page show ahead/behind
  per ready project when the user opens it (cheap `git fetch` +
  `rev-list --left-right --count` server-side), not auto-pull. Keep the
  existing explicit Sync as the pull action; refuse sync while the tree is
  dirty or an agent run is active on that project.
- **Chat header chip (already shipped in P4):** project name chip; extend it
  with the branch and a dirty indicator read from the session snapshot, and
  treat "switch project" as *start a new session from that project* — never
  re-target the running conversation (matches every product surveyed).
- **Status column semantics:** ready / cloning / sync_error already exist;
  add a `syncing` guard so a page refresh mid-sync doesn't double-run, and
  surface sync_error with the stored bounded stderr text.

### Non-goals (keep)

- No GitHub/OAuth provider integration in v1 of this surface — "paste URL"
  is the primary import and is fully consistent with the field (OpenHands
  accepts pasted URLs too); a provider picker can layer on later.
- No per-project credentials (DP-8): keep the host-credential model.
- Deleting never force-pushes or touches the remote (already DP-10).

### Verify

`cd webui && pnpm build && pnpm test`, handler tests for any new/changed
endpoints (e.g. an ahead/behind or "sync guard" endpoint), manual smoke:
register via UI → row goes ready → new chat on it → header chip → external
push → Sync now → behind indicator clears → Delete confirms and never
touches the remote.

Sources: [OpenHands GitHub install](https://docs.openhands.dev/openhands/usage/cloud/github-installation),
[OpenHands integrations](https://docs.openhands.dev/openhands/usage/settings/integrations-settings),
[OpenHands home screen (DeepWiki)](https://deepwiki.com/OpenHands/OpenHands/12.2-home-screen-and-workspace-selection),
[Devin repo setup](https://docs.devinenterprise.com/onboard-devin/new-repo-setup),
[Cursor Cloud Agent](https://cursor.com/docs/cloud-agent),
[Copilot coding agent](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/cloud-agent/use-cloud-agent-on-github),
[Codex cloud environments](https://learn.chatgpt.com/docs/environments/cloud-environment),
[Claude Code on the web](https://code.claude.com/docs/en/claude-code-on-the-web),
[Codespaces source control](https://docs.github.com/en/codespaces/developing-in-a-codespace/using-source-control-in-your-codespace),
[Codespaces lifecycle](https://docs.github.com/en/codespaces/getting-started/understanding-the-codespace-lifecycle),
[Codespaces repo access](https://docs.github.com/en/codespaces/managing-codespaces-for-your-organization/managing-repository-access-for-your-organizations-codespaces),
[DevPod credentials](https://devpod.sh/docs/developing-in-workspaces/credentials),
[Ona source control](https://ona.com/docs/ona/source-control/overview),
[Coder templates](https://coder.com/docs/@v2.31.5/admin/templates/open-in-coder).
