# /dross-init

Bootstrap dross in a **greenfield** repo. Walk the user through vision → market → stack → scaffold → runtime → verify → rules. Run once per project; expect ~30 minutes.

**Run this as a conversation, not a broadcast.** Follow the shared interaction playbook (`_interaction.md`, printed by the `dross interaction show` pre-flight step below): walk vision → stack → runtime one decision at a time, proposing a default the user accepts or steers — never one batched questionnaire.

## 0. Pre-flight

1. Run `dross rule show` and `dross interaction show`; treat the rules as MUST-FOLLOW and follow the printed interaction playbook for every turn of this command.
2. Confirm cwd is the intended project root. If `.dross/` already exists, stop and tell the user to use `/dross-onboard` or `dross init --force`.
3. Run `dross init`. It creates `.dross/`, the empty `project.toml`, `rules.toml` and a machine-local `state.json` (gitignored, never committed), and seeds `profile.toml` from GSD if available.

## 1. Vision

Walk the identity fields **one field per turn** — never one bundled questionnaire. For each, propose a sensible default and ask via `AskUserQuestion` (accept / change), then move to the next:

1. Project name (default = directory name)
2. One-sentence description
3. Core value — what changes for the user when this exists
4. Target audience
5. Three explicit non-goals (things this project will NOT do)

Save each as it lands:
```
dross project set project.name           "<name>"
dross project set project.description    "<description>"
dross project set goals.core_value       "<core value>"
```
Non-goals + audience: edit `.dross/project.toml` directly under `[goals]`. Confirm the captured identity with a **one-line summary** — never paste `project.toml` back.

## 2. Market scan

Use `WebFetch` to look up 2-3 similar tools by description. Summarise each in one line. Add to `[[competition]]` blocks in `project.toml`. Ask the user for 1-3 differentiators and write them to `goals.differentiators`.

## 3. Stack choice

Propose 2-3 viable stacks based on the description. For each: 3 lines max — what it is, why it fits, the cost. Surface trade-offs honestly. If the merged profile shows locked stack preferences (sveltekit, drizzle, betterauth, paraglide, pnpm, self-hosted), use those by default and only diverge if the project description requires it.

After the user picks, lock each non-trivial choice with a `why`:
```
[[stack.locked]]
choice = "..."
why    = "..."
locked_at = "<today>"
```
Edit `project.toml` directly for these blocks.

## 4. Remote / git host

`dross init` already tried to detect the canonical git remote via `git remote get-url origin`. Run `dross project show` and look at the `[remote]` block.

If `[remote]` is missing or empty (no git origin yet — fresh greenfield), ask:
- **URL** — `https://<host>/<owner>/<repo>` of where this code will live.
- **Provider** — github / forgejo / gitea / gitlab / bitbucket / azure-devops / none. If host is `github.com` → `github`; `codeberg.org` → `forgejo`; `gitlab.com` → `gitlab`; `bitbucket.org` → `bitbucket`; `dev.azure.com` or `<org>.visualstudio.com` → `azure-devops`; otherwise ask.
- **Public** — can a cloud-side agent (no VPN, no SSH key) `git clone` it? Default no for self-hosted forges.

Then for fields the global defaults didn't seed (check `dross project get remote.auth_env` etc.):
- **api_base** — REST base URL. github → `https://api.github.com`. Forgejo/Gitea → `https://<host>/api/v1`. Confirm with user.
- **log_api** — does this instance expose CI logs via API? (User's Forgejo customisation: yes.)
- **auth_env** — env-var name holding the API token. **Never the token value.** For github use `GITHUB_TOKEN`; for the user's Forgejo: `FORGEJO_TOKEN`.
- **reviewers** — comma-separated list of human reviewer usernames `/dross-ship` should auto-assign. Skip if the user doesn't want this.

Persist with `dross project set remote.<field> "<value>"`. For booleans use `true` / `false`; reviewers is csv.

If `~/.claude/dross/defaults.toml` doesn't exist yet (check with `dross defaults show`), ask: *"Save these as defaults so the next project pre-fills them?"* If yes, run `dross defaults save` — extracts provider/api_base/log_api/auth_env/reviewers from project.toml and writes them globally. URL and public flag are project-specific and never copied.

## 5. Rules import

`dross rule list --scope global` — show what already applies. Ask: "any project-specific rules to add up front? (e.g. 'always run db migrations via docker compose exec', 'never push to main')"
For each rule: `dross rule add --scope project "<text>"`.

## 6. Scaffold

Run the actual scaffold commands:
- TS/SvelteKit: `pnpm create svelte@latest <name>`
- Go: `go mod init <module>`
- C# .NET: `dotnet new <template>`
- Godot: open editor manually; we just write a placeholder `project.godot`

For docker-from-day-1 projects, write a starter `Dockerfile` and `docker-compose.yml` based on the chosen stack.

Confirm each scaffold command before running. Show output. If anything fails, stop and surface it.

## 7. Runtime capture

Now that the scaffold exists, derive runtime commands from `package.json` / `Makefile` / detected files. For each: present detected default, ask user to confirm or correct.

Set with:
```
dross project set runtime.mode              "<docker|native>"
dross project set runtime.dev_command       "<exact command>"
dross project set runtime.test_command      "<exact command>"
dross project set runtime.typecheck_command "<exact command>"
dross project set runtime.lint_command      "<exact command>"
dross project set runtime.format_command    "<exact command>"
dross project set runtime.build_command     "<exact command>"
dross project set runtime.migrate_command   "<exact command>"
```

Service URLs (`runtime.services`) and paths (`paths.*`) are written by editing `project.toml` directly.

## 8. Functionality verification

For each captured command, run it and report:
- `dev_command` → background, then `curl <runtime.services.app.url><health>` expects 2xx within 30s. Stop the dev server after.
- `test_command` → expect exit 0
- `typecheck_command` → expect 0 errors
- `lint_command` → expect 0 errors

Print a table:
```
✓ dev_command          : health 200 in 4.2s
✓ test_command         : exit 0, 47 passed
✗ typecheck_command    : 3 errors
                         → user to fix or correct the command
```
Don't proceed until 100% green or the user explicitly waives a row.

## 9. Repo init

If not already a git repo: `git init`, create `.gitignore` from a sensible default for the chosen stack.

When §7 recorded a `runtime.test_command`, run the suite before the initial commit. The commit gate admits a code commit only once a full `dross test` has gone green on exactly the tree being committed. First check consent with `dross trust --check`. If it is not trusted yet, show the user the exact `runtime.test_command` line and let them grant it by running `dross trust` themselves — never on their behalf. Then:
```
dross test
```
On red, fix the scaffold (or ask the user) and re-run before committing. With no test command and no test lane the gate stays silent, so skip this step.

Write the initial commit:
```
git add . && git commit -m "chore: initialise project via dross"
```
Ask before committing. A gate refusal is a stop: read its rule and remedy, and never route around one.

## 9.5 Telemetry

Dross records local-only usage events at `~/.claude/dross/telemetry.jsonl` to help the user spot friction in their own workflow. Default ON; never sent anywhere. Logs shapes (counts, durations, error classes) — never user-typed content.

If `dross stats path` shows a defaults file with `[telemetry] asked_at` already set, skip this step (user has already been prompted on a prior project).

Otherwise, ask via `AskUserQuestion`: **"Dross can record local-only telemetry to `~/.claude/dross/telemetry.jsonl` (counts, durations, error classes — no file content, no project paths). It helps you spot friction in your own usage. Enable?"** Options: `enable` (default) / `disable`.

Run `dross stats opt-in` or `dross stats opt-out` based on the answer. Both stamp `asked_at` so we don't ask again on the next project.

## 10. Wrap

Run `dross validate`. Should be green. Print:
```
Project bootstrapped.

Next:
  /dross-milestone v0.1   — scope the first milestone (title, success criteria, non-goals, phases)
  /dross-spec             — clarify the first phase (run after the milestone is scoped)
```
Update state: `dross state touch "init complete"`.
