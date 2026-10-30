# /dross-onboard

Adopt dross into an **existing** repo. Lighter than `/dross-init`: no scaffolding, just signal scan → confirm → capture → verify.

**Run this as a conversation, not a broadcast.** Follow the shared interaction playbook (`_interaction.md`, printed by the `dross interaction show` pre-flight step below): confirm each detected signal and captured setting one at a time, proposing a default the user accepts or corrects.

## 0. Pre-flight

1. Run `dross rule show` and `dross interaction show`; treat the rules as MUST-FOLLOW and follow the printed interaction playbook for every turn of this command.
2. Stop if `.dross/` already exists (suggest `dross onboard --force`).
3. Run `dross onboard`. It scans signal files (Dockerfile, package.json, lockfiles, tsconfig, go.mod, *.csproj, project.godot, .github/workflows) and writes a draft `project.toml`.

Read the printed "Detected" list and use it to inform the questions below.

## 1. Identity

Walk the identity fields **one field per turn** — never one bundled ask. For each, propose a default and confirm via `AskUserQuestion` (accept / change), then move on: name (default = directory name), one-line description, core value, audience, non-goals.

```
dross project set project.name        "<name>"
dross project set project.description "<description>"
dross project set goals.core_value    "<core value>"
```
Audience + non-goals: edit `project.toml` directly. Confirm the captured identity with a **one-line summary** — never paste `project.toml` back.

## 2. Stack confirmation

`dross project show` to display detected stack. Ask the user to confirm or correct:
- Languages
- Frameworks
- Package manager
- Test runner
- Type checker / linter / formatter

Set via `dross project set stack.*` for the simple fields. Locked choices go into `[[stack.locked]]` blocks (edit toml directly).

## 3. Runtime capture

For each command field below, show the detected default (read `package.json` scripts, `Makefile` targets, etc.) and ask the user to confirm the **exact** command they actually use:

- `dev_command`
- `test_command`
- `typecheck_command`
- `lint_command`
- `format_command`
- `build_command`
- `migrate_command`
- `seed_command`
- `shell_command`
- `logs_command`

If `runtime.mode = docker`, prefix correctly (e.g. `docker compose exec app pnpm test`). Persist via `dross project set runtime.<field> "<exact>"`.

Service URLs (app, db, redis, etc.) — edit `[runtime.services]` directly.

## 4. Remote / git host

`dross onboard` already detected `[remote]` from `git remote get-url origin` and overlaid `~/.claude/dross/defaults.toml`. Run `dross project show` and look at the `[remote]` block.

For each field, present the resolved value and ask "confirm or change?":
- **url** — should match the canonical clone URL. Azure DevOps: `https://dev.azure.com/<org>/<project>/_git/<repo>` (or `https://<org>.visualstudio.com/<project>/_git/<repo>`); onboard writes this https form even for an ssh origin.
- **provider** — github / forgejo / gitea / gitlab / bitbucket / azure-devops / none. If empty (self-hosted unknown host), ask the user.
- **public** — can a cloud-side agent (no VPN, no SSH key) `git clone` it? Default no for self-hosted forges and for azure-devops.
- **api_base** — REST base URL. github → `https://api.github.com`. Forgejo/Gitea → `https://<host>/api/v1`. azure-devops → the org URL, `https://dev.azure.com/<org>` (or `https://<org>.visualstudio.com`); on an Azure DevOps Server, the collection URL, `https://<host>/tfs/<Collection>`.
- **log_api** — does the instance expose CI logs via API?
- **auth_env** — env-var name (e.g. `FORGEJO_TOKEN`, `GITHUB_TOKEN`; for azure-devops `AZURE_DEVOPS_EXT_PAT`, the az devops CLI's variable). **Never the token value.**
- **api_version** — azure-devops only (`remote.api_version`). Unset on dev.azure.com / visualstudio.com (7.1). On an Azure DevOps Server, the server's version: 2019 → `5.0`, 2020 → `6.0`, 2022 → `7.0`.
- **reviewers** — csv of human reviewer usernames `/dross-ship` should auto-assign. Empty = none.

Persist any changes with `dross project set remote.<field> "<value>"` (booleans = `true`/`false`; reviewers = csv).

An **Azure DevOps Server** (on-premises; any host) is never autodetected — onboard derives only its url. Set `remote.provider` to `azure-devops`, `remote.api_base` to the collection URL and `remote.api_version` by hand, and keep `[remote].url` in its https form, `https://<host>/tfs/<Collection>/<project>/_git/<repo>`, even when origin is ssh (`ssh://<host>:22/...`): the host allowlist derives from that url, and the ssh form would refuse the https api_base.

If `~/.claude/dross/defaults.toml` doesn't exist yet (check with `dross defaults show`), ask: *"Save these as defaults so the next project pre-fills them?"* If yes, run `dross defaults save` — extracts provider/api_base/log_api/auth_env/reviewers from project.toml and writes them globally. URL and public flag are project-specific and never copied.

## 5. Functionality verification

Run each captured command, report pass/fail in a table. Same rules as `/dross-init` step 8. Failing rows must be either fixed or explicitly waived before continuing.

## 6. Conventions

Ask:
- Main branch (default detected from `git symbolic-ref refs/remotes/origin/HEAD` if available)
- Branch naming pattern (e.g. `feature/*`, `<initials>/<topic>`)
- Commit convention (conventional / freeform)
- Squash or merge?
- Monorepo? If yes, list workspaces.

Edit `[repo]` block directly.

## 7. Rules intake

`dross rule list --scope global` — show inherited rules.

Then ask the user, free-form: **"What has Claude or another AI done in this repo that you want to never happen again?"** Capture every answer as a project rule:
```
dross rule add --scope project "<exact rule text>"
```
Tag obvious cross-project ones with `--scope global` instead.

## 8. Goals + non-goals + competition

Optional but valuable. If skipped, mark a TODO and move on.

## 8.5 Telemetry

If `dross stats path` shows a defaults file without `[telemetry] asked_at` set (i.e. dross has never asked this user about telemetry on any project), prompt now.

`AskUserQuestion`: **"Dross can record local-only telemetry to `~/.claude/dross/telemetry.jsonl` (counts, durations, error classes — never file content). It helps you spot friction in your own dross usage. Enable?"** Options: `enable` (default) / `disable`.

Run `dross stats opt-in` or `dross stats opt-out` based on the answer. Both stamp `asked_at` so this prompt is shown at most once across all projects.

## 8.7 Architecture backfill

Backfill `ARCHITECTURE.md` from the codebase you just scanned, using the shared
engine — the same one `/dross-architecture` runs (the `backfill_trigger`
decision). Read `~/.claude/dross/prompts/architecture.md` and run it from its §1
(repo root is already known). It produces a feature-organized `ARCHITECTURE.md`
and returns here.

If the repo already has an `ARCHITECTURE.md`, leave it untouched — first-creation
only; don't clobber hand-written docs.

## 9. Wrap

Run `dross validate`. Print summary:
```
Onboarded. Detected and verified:
  - <languages, frameworks, package manager, runtime mode>
  - <N> commands runtime-verified
  - <N> rules loaded (M global, K project)
Next: /dross-status — see where to focus first.
```
Update state: `dross state touch "onboard complete"`.
