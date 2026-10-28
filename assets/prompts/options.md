# /dross-options

Full editor for every dross-managed setting. Designed to be run rarely (after milestone changes, when adopting new conventions, when something feels stale) and to take its time. **Save-per-option:** every change persists immediately via the relevant `dross X set` — stopping mid-way never loses prior edits.

**Run this as a conversation, not a broadcast.** Follow the shared interaction playbook (`_interaction.md`, printed by the `dross interaction show` pre-flight step below): a section-pick gate first, then walk each setting one decision per turn — never one mega-form over every setting.

## 0. Pre-flight

1. Run `dross rule show` and `dross interaction show`; treat the rules as MUST-FOLLOW and follow the printed interaction playbook for every turn of this command.
2. Confirm `.dross/` exists. If not, suggest `/dross-init` (greenfield) or `/dross-onboard` (existing repo) and stop.
3. Capture current state once, up front:
   - `dross project show` — every project.toml field
   - `dross defaults show` — global defaults
   - `dross rule list` — merged rules
   - `dross env list` — settings.json env keys (values masked)
4. Track `changed: []` and `skipped: []` lists in your head; use them in the wrap-up.

## Section pick

Open with a single **section-pick gate** before touching any setting: one `AskUserQuestion` (multiSelect) listing the sections below, so the user picks which to review this run and a one-setting change never forces all sixteen. Lead with the sections most likely stale (e.g. after a milestone change: stack, runtime, repo). Walk only the chosen sections; skip the rest without asking.

Sections: project identity · stack · runtime · repo conventions · remote · paths · env files · global defaults · rules · profile · settings env vars · issue board · mutation adapters · machine-local settings · hooks · constraints and competition.

This gate is **its own turn**, distinct from the per-setting Keep · Change · Skip turn in "How each section works" below — never collapse the two into one mega-form over every setting.

## How each section works

For each field in a chosen section: state the current value, ask `AskUserQuestion` with options **Keep · Change · Skip section**. On Change, gather the new value and immediately run the listed `dross X set ...` command. On any error from `dross X set`, surface it and offer Retry / Skip. On Skip section, move to the next section.

For booleans: Yes / No. For CSVs: ask for comma-separated input. For secrets: see §11 — never ask the user to paste tokens in chat.

If you see a field in `dross project show` that isn't covered below, **stop** and tell the user — that's a schema-vs-prompt drift bug to fix, not something to silently invent prompts for.

Some fields dross **records but does not yet act on** — the value is stored and shown, and no dross code path or prompt reads it. Say so when the user sets one, so they aren't told a value works when it only persists. The list is pinned by `inertKeyReasons` in `internal/cmd/inert_config_test.go`, which fails the build if a settable key goes unread without a stated reason, so it cannot quietly grow:

Recorded only: `runtime.dev_command`, `runtime.stop_command`, `runtime.test_watch`, `runtime.lint_command`, `runtime.migrate_command`, `runtime.seed_command`, `runtime.shell_command`, `runtime.logs_command` (no verb runs them yet), `stack.languages`, `stack.frameworks`, `stack.type_checker`, `stack.linter`, `stack.formatter`, `stack.test_runner` (dross detects the stack from the filesystem), `paths.migrations`, `paths.schemas`, `paths.public`, `repo.layout`, `repo.workspaces`, `repo.root_run_dir`, `remote.public`, `goals.audience`, `env.secrets_location`.

## 1. Project identity

Fields: `project.name`, `project.description`, `project.version`, `goals.audience`, `goals.core_value`, `goals.non_goals` (csv), `goals.differentiators` (csv).

Persist with `dross project set <field> "<value>"`.

## 2. Stack

Fields: `stack.languages` (csv), `stack.frameworks` (csv), `stack.package_manager`, `stack.type_checker`, `stack.linter`, `stack.formatter`, `stack.test_runner`, `stack.e2e_runner`, `stack.profile`.

`stack.profile` is the matched stack-profile id and is normally set by `dross stack` rather than by hand — show it, and if it looks wrong point at `dross stack detect` to re-derive it instead of typing an id. `stack.package_manager` is load-bearing beyond convenience: a remote mutation run refuses rather than guessing when it is unset (see §14).

`[[stack.locked]]` blocks live in `project.toml` as TOML arrays — surface them read-only here; if the user wants to add/remove a locked choice, point at editing `project.toml` directly (CLI doesn't model array-of-tables yet).

## 3. Runtime

Fields: `runtime.mode`, `runtime.dev_command`, `runtime.stop_command`, `runtime.test_command`, `runtime.test_watch`, `runtime.e2e_command`, `runtime.typecheck_command`, `runtime.lint_command`, `runtime.format_command`, `runtime.build_command`, `runtime.migrate_command`, `runtime.seed_command`, `runtime.shell_command`, `runtime.logs_command`.

For each command, show what's currently set. Empty = "not configured". Encourage **exact** strings, including docker prefixes.

`[runtime.services]` is a TOML map — surface read-only; for changes, edit `project.toml` directly.

## 4. Repo conventions

Fields: `repo.layout`, `repo.root_run_dir`, `repo.workspaces` (csv), `repo.git_main_branch`, `repo.branch_pattern`, `repo.commit_convention`, `repo.squash_merge` (bool).

## 5. Remote / git host

Fields: `remote.url`, `remote.provider`, `remote.public` (bool), `remote.api_base`, `remote.log_api` (bool), `remote.auth_env`, `remote.auth_user`, `remote.auth_scheme`, `remote.project_id`, `remote.api_version`, `remote.reviewers` (csv).

Four of those are provider-conditional — surface them only when the provider calls for them, and say why:

- `remote.auth_scheme` — `private-token` (default) · `bearer` · `basic`. Only `basic` needs `remote.auth_user`. On azure-devops an empty scheme sends HTTP Basic with an empty user and the PAT; `bearer` sends an Entra ID token instead, and `private-token` and `basic` have no effect there (`dross doctor` flags them).
- `remote.auth_user` — the account **user/email** for HTTP Basic (bitbucket, and jira on the board side). It is a username, never a token; the token stays in the env var named by `auth_env`. azure-devops ignores it — its Basic credential has an empty user — and `dross doctor` flags one that is set.
- `remote.project_id` — gitlab only: numeric project-id override when it can't be derived from the URL.
- `remote.api_version` — azure-devops only: the `api-version` sent on every call. Empty means `7.1`, which Azure DevOps Services takes; an Azure DevOps Server caps it by release — `5.0` for Server 2019, `6.0` for 2020, `7.0` for 2022.

On azure-devops the org/collection, project and repo all derive from `remote.url` around its `_git` segment, so there is no identity field to set. `remote.api_base` is the org URL (`https://dev.azure.com/<org>`, or `https://<org>.visualstudio.com` on a legacy host) or, on a Server, the collection URL (`https://<host>/tfs/<collection>`). An on-prem TFS reachable only over plain http stays refused: dross sends a token over https alone, and `allow_hosts` widens the set of hosts, never the scheme.

Cross-check: if `remote.url` is set, run `git remote get-url origin` and warn if it differs.

If the user has updated `provider`, suggest `dross defaults save` at the end of this section so future projects pre-fill from these values.

## 6. Paths

Fields: `paths.source`, `paths.tests`, `paths.e2e`, `paths.migrations`, `paths.schemas`, `paths.i18n`, `paths.public`.

Verify each non-empty path exists with `ls -d` and warn (don't block) on missing dirs.

## 7. Env files

Fields: `env.files` (csv, ordered), `env.secrets_location`, `env.gitignored` (bool).

For each file in `env.files`, check it exists and is in `.gitignore` if `env.gitignored = true`.

## 8. Global defaults (~/.claude/dross/defaults.toml)

Show output of `dross defaults show`. Ask whether to:
- **Save current project's [remote] as defaults** → `dross defaults save`
- **Skip** — leave defaults untouched

## 9. Rules

Show `dross rule list --scope project` and `dross rule list --scope global`. Ask:
- Add a project rule? → `dross rule add --scope project "<text>"`
- Add a global rule? → `dross rule add --scope global "<text>"`
- Disable / re-enable an existing rule? → `dross rule disable <id>` / `enable <id>`
- Promote a project rule to global? → `dross rule promote <id>`
- Remove a rule? → `dross rule remove <id>` (confirm twice)

## 10. Profile

`dross profile show` to display merged profile. If the user wants edits, edit `.dross/profile.toml` directly — profile.toml is hand-curated free-form TOML (no dotted-path CLI yet).

## 11. Settings env vars (~/.claude/settings.json)

`dross env list` already ran in pre-flight. For each key the project might use (the project's `remote.auth_env` first, then any common ones like `GITHUB_TOKEN`, `FORGEJO_TOKEN`):

- If **set** (`set (length N)`): ask "Update?" Yes/No. If yes: tell the user *"Run `dross env set <KEY>` in your own shell — that prompts for the new value with input hidden. Don't paste tokens here."* Wait for confirmation before continuing.
- If **NOT SET** (and the project depends on it): same — point at `dross env set <KEY>`.

**NEVER** ask for, accept, or repeat a token value in this conversation. Tokens leave the user's keyboard, go directly to settings.json via `dross env set`, and never enter Claude Code's context.

If the user asks to add an entirely new env var dross doesn't currently know about, that's still fine — `dross env set FOO` works for any KEY.

## 12. Issue board

Fields: `board.provider` (forgejo · gitea · gitlab · youtrack · jira · github), `board.base_url`, `board.auth_env`, `board.auth_user`, `board.project`, `board.github_project`, `board.enabled` (bool), `board.milestone_mode` (version · agile · epic).

`board.auth_user` is jira's account **email** for HTTP Basic — a username, never a token. `board.github_project` is a GitHub Projects v2 board node id. `board.project` is the tracker's project short-name/key, except on github where it is `owner/repo`.

`[board.state_map]` (dross lifecycle state → tracker state value) and `[board.fields]` (`state`, `type`, `fix_versions` field-name overrides) are TOML maps — surface read-only; for changes, edit `project.toml` directly.

`board.enabled` has its own verbs — `dross issue enable` / `dross issue disable` — so use those rather than writing the field. If it is on, offer to run `dross issue pull` as a read-only liveness check: it exercises the base URL, the token in `board.auth_env` and the project key in one call. A board that has quietly stopped authenticating otherwise shows up only as a zero in the `/dross-status` inbox line.

## 13. Mutation adapters

Fields: `mutation.adapters` (csv), `mutation.gremlins.timeout_coefficient`, `mutation.stryker.workdir`.

`[mutation]` is **not settable through `dross project set`** — it has no dotted-path writer. Show the current values and, for a change, edit `.dross/project.toml` directly, then re-run `dross validate`.

`mutation.adapters` is the allowlist of adapters this repo runs. Leaving it empty means *all* adapters are eligible, which is usually wrong in one specific way: `dross doctor` then probes every toolchain, so a Go-only repo fails readiness over a missing `npx` and `dotnet`. Set it to the adapters the repo's languages actually need.

## 14. Machine-local settings (.dross/local.toml)

`.dross/local.toml` is **gitignored**: none of it appears in `dross project show`, and none of it travels with the repo — a fresh clone carries none of these values. That is what makes this section worth walking. Every setting here was written by a one-shot command nobody remembers running, and nothing else surfaces them.

Read any key with `dross local get <key>` (empty output = unset).

**Plain settable keys** — `dross local set <key> "<value>"`:

- `quick_base` — the branch `/dross-quick` forks off on this machine. Machine-local because it tracks whatever you happen to be working from, not a property of the repo.
- `allow_hosts` — csv of extra hosts this machine authorizes dross to reach. It lives here rather than in `project.toml` for a security reason worth repeating to the user: a committed allowlist would be self-authorizing, so a tracked `local.toml` is refused *unread* rather than merged.

**Remote mutation.** Mutation testing can run on another machine: `dross verify` rsyncs this working tree to a host and runs the adapters there. A host named in the tracked `project.toml` is refused by name — only this untracked file can authorize one.

**Current state** — run `dross mutation remote status` and show what it prints (`not granted`, the granted `host:workdir`, or a refusal if `local.toml` is tracked).

**The grant.** The two keys behind it are `mutation_remote_host` and `mutation_remote_workdir` — name them, so a user who opens `local.toml` and finds them knows which verb owns them. `dross local set` refuses both by design, so this section does **not** write them; it invokes the verb that does:

- **Grant / change** → `dross mutation remote grant <host> <workdir>`
- **Revoke** → `dross mutation remote revoke`
- **Keep**

The verb prints the host and workdir it is about to authorize before writing them. Read that line back to the user rather than summarizing it: a grant is code execution on that machine, as them, and a grant they did not read is a rubber stamp.

After any grant change, run `dross doctor` and surface its `Remote mutation:` line — it reports whether the host is reachable and whether the configured adapters' toolchains are installed there. Learning that here costs a second; learning it mid-verify costs a pushed tree and an empty report.

**Tuning knobs** — these *are* settable, via `dross local set <key> "<value>"`:

- `mutation_workers` — how many mutants run at once. **Unset is not zero**; it means the adapter picks its own default (half the local cores, or half the *remote's* cores under a grant). Only set a number if a run has actually misbehaved — a hardcoded value sizes a 32-core host's run by this laptop.
- `mutation_test_cpu` — CPUs each mutant's test run may use. Unset = 1. Raising it multiplies against `mutation_workers`; over-subscribing the box produces spurious timeouts that read as surviving mutants.
- `remote_scratch_base` — absolute path on the REMOTE host for the throwaway build cache a mutation run compiles into. Empty derives it from the granted workdir's parent, which is the right guess and not a guarantee: on one host that parent turned out to be a 75 GB root volume while the 300 GB volume provisioned for the work sat elsewhere, and a run filled root at ~1.3 GB/min. Set this when the derived location is the wrong volume. (`DROSS_SCRATCH_BASE` is the same lever for THIS machine, and `DROSS_SCRATCH_MIN_FREE_GB` sets the free-space floor a run refuses to start below — default 20, `0` disables.)
- `mutation_remote_env` — csv of variable **NAMES** a remote run needs (e.g. `DATABASE_URL,NODE_ENV`). Names only, never `NAME=value`: dross reads each value from its own environment at run time and stores none of them. A name listed here that is unset locally is an error, not an empty export. Ask for names the way you would any CSV — they are not secrets, so this one is safe to type in chat, unlike §11.

**Trusted test command** — the same consent shape as the grant, a different verb, and the one most likely to be silently stale. `trusted_test_command` records a hash of the `runtime.test_command` you consented to; change that command in `project.toml` and consent lapses, because you never agreed to the new one.

Run `dross trust --check`. Exit 0 means trusted — say so and move on. Non-zero means missing or stale: show the user the **exact** `runtime.test_command` line from `project.toml` and tell them to run `dross trust` themselves. Never run it on their behalf — the gate exists so a human reads the line the repo supplied, and an agent granting it defeats the whole mechanism.

**Test lanes and their grants.** A `[[runtime.test_lane]]` block is a name, a list of match globs and the command that tests the files they match; `dross test --files <paths>` then runs only the lanes a file set actually hits, which is what keeps a docs edit from paying for the whole suite. Lanes are opt-in — a repo that declares none runs `runtime.test_command` exactly as before.

Declare them through the CLI, never by hand-editing `project.toml`:

- **Show** → `dross test lane list` (prints each lane's globs *and* its command line)
- **Add** → `dross test lane add <name> --match <glob> --command "<cmd>"` (`--match` is repeatable)
- **Edit any field** → `dross test lane edit <name> --match <glob> | --command "<cmd>" | --prepare "<cmd>" | --selector <style> | --selector-template "<tmpl>" | --selector-join <sep> | --empty-exit <code> | --toolchain <binary> | --install "<cmd>"` (pass `""` to any of them to clear it)
- **Install a lane's toolchain** → `dross test lane install <name> [--apply] [--local]` (dry-run by default)
- **Remove** → `dross test lane remove <name>`

Every field a lane has is editable in place. Only the lane's **name** is remove-then-re-add, and it is the one field that has to be: names key the consent store under `trusted_lane_commands`, so a name cannot move without the grant moving with it. `remove` drops the grant, so routing any other change through remove-then-re-add would report a lane the user HAS trusted as one they never have — collapsing STALE into ABSENT, which is the distinction the consent ladder exists to preserve. `dross test lane edit` keeps the grant and stales it instead, so `dross trust --lane` says the line CHANGED. `--toolchain` and `--install` are the two fields outside the command grant — toolchain names binaries rather than a command, install carries its own grant — so changing either leaves the lane's test runs GRANTED. An edit that would leave the lane malformed (a blank command, an empty match list, a glob that does not compile, a template with nothing to place) is refused before anything is written, through the same rules `dross validate` reports, and the gate reads the proposed lane ALONE — so a lane broken by a hand-edit elsewhere in `project.toml` never blocks the edit that would fix it.

**Bootstrapping a lane before it runs.** `dross test lane add ... --prepare "<cmd>"` gives a lane a line that runs after the tree sync and before that lane's own command, on the same host and transport — `make build` on a cold remote, `docker compose up -d` before an integration lane. It is opt-in per lane, never deduplicated across lanes (idempotence is the declared contract, so two lanes sharing a prepare each run their own), and skipped entirely for a lane that will not spawn. A prepare that fails skips only its own lane's tests and exits `7`, which is not a red suite. The lane's single consent grant covers the prepare and the command together, so adding one stales the grant and `dross trust --lane <name>` prints both lines before writing.

**Installing a lane's toolchain.** `dross test lane install <name>` closes the gap on the machine that HAS it — the granted host when the lane would have run there, this machine when this machine is short of the binary — and every line says which one it acted on. `--local` forces the install here whichever side the probe found the gap on. Dry-run by default; `--apply` is the only path that changes a machine, and a `dross test` fallback never installs as a side effect — it just names this verb in the same breath as the fallback line. The command is never guessed: it comes from a built-in recipe (package-class tools going into a runtime that already exists) or from the lane's own `install` line, which REPLACES the built-in recipe for that lane. A language runtime is refused by name for the host's owner to install; a tool with neither is reported without failing the command. `dross remote bootstrap` covers the same ground for the whole host, planning every declared lane's toolchain alongside the mutation adapters from one probe. Nothing is written by an install — the next run routes that lane on the probe's answer alone.

**Scoping a lane to the files that hit it.** `dross test lane add ... --selector <style>` makes the lane append its own matched paths to its command, in one of three shapes: `path` (each file), `dir` (each parent directory), `go-package` (`./<dir>/...`). A task touching one file then costs that package rather than the lane's whole suite. Omit it and the lane runs its command untouched, which is what every lane written before this did — it is opt-in per lane, and `dross test lane list` prints `selector:` only for the lanes that declare one.

**Choosing which machine a lane runs on.** Each lane's *toolchain* — the binaries it needs on whichever host runs it — is derived from the first token of its `command` and `prepare` lines, so it works on every lane already declared. Before a `--files` run spawns anything, dross probes the granted host once for the union of every matched lane's toolchain; a lane whose binaries the host lacks is announced by name and runs on THIS machine instead, reporting its own suite result, while its neighbours still go remote. That is deliberately not the same thing as an unreachable host: a host that never answered sends the WHOLE run home with the existing `could not reach <host>` line and blames no lane's toolchain, while a reachable host missing a binary names that binary and moves only its own lane. A lane whose toolchain is on neither machine does not spawn at all — it exits `8`, because a `command not found` reported as a red suite sends you hunting a bug in code no runner ever loaded. Nothing is written by any of this: the fallback is per-run, so a host that gains the binary runs that lane remotely again with no user action. `dross test lane add|edit --toolchain <binary>` (repeatable) overrides the derived list wholesale, which is the fix for the lines first-token derivation gets wrong; `dross test lane list` shows every lane's effective set, and `dross doctor`'s Remote section reports each lane's toolchain against the granted host so a fallback is visible before a run hits it.

`--empty-exit <code>` (repeatable) names the exit status this lane's runner uses for "collected no tests" — pytest's `5`. That lane is then reported as a **selector miss** rather than a red suite, and does not fail the gate; a run where every matched lane missed still exits `5` rather than reporting green. It requires `--selector`: an unscoped lane runs its whole suite, so the code could never fire. Both flags are refused before anything is written if the style is unknown or the code is one that already means something else (`0` is success, `255` is ssh transport failure), so the CLI cannot write a lane `dross validate` would then reject.

**Placing the paths on a line the styles cannot shape.** `--selector-template "<tmpl>"` names WHERE the matched paths land instead of appending them, for a runner the three styles cannot reach: `--selector-template "--package {path}"` gives cargo its repeated flag, `--selector-template "-R {paths}" --selector-join "|"` gives `ctest -R` one joined regex. `{path}` repeats the whole template once per derived path; `{paths}` substitutes them all into a single instance — separate arguments by default, one argument joined by `--selector-join` when that is declared. The template needs a `--selector`: the style decides what SHAPE the values take (files, dirs, Go packages), the template decides where they go, and a template with no style has nothing to place. A template containing neither placeholder is refused — it would scope nothing while claiming to — and any other `{...}` run is ordinary template text, so a regex quantifier like `a{2,3}` is safe to write.

The template's own text is fenced by **consent alone**, exactly as `command` and `prepare` are: it is folded into the lane's consent line, so adding or changing a template or a join leaves the grant STALE rather than letting an unread line run under a grant issued before it. Substituted paths are shell-quoted and option-safed on the way in, so a matched path can never become a second command or an option dross did not choose to pass.

**A scoped lane on a whole-tree command is a warning.** Declaring a selector or a template on a command whose last token is `./...` or `.` prints a ⚠ naming the lane and the token — at declaration time and on every `dross validate` run — because appending a scoped selector to a whole-tree token runs the union, so the "scoped" run would silently be the whole suite. The lane is still written and validate still exits 0: it is well-formed, and the combination is occasionally deliberate.

Appending a selector does **not** disturb consent: the grant covers the lane's declared `command`, so a lane trusted before it grew a selector keeps running, and an ungranted one still refuses. A `--selector-template` is different and deliberately so — it is arbitrary user text on the spawned line, so it IS part of the consent line.

Each lane's command carries its **own** grant, keyed by lane name under `trusted_lane_commands`. That key is not writable through `dross local set` — same reason as `trusted_test_command` — so consent goes through `dross trust --lane <name>`, which prints the line before recording it. `dross trust --lane <name> --check` reports granted / absent / stale for one lane, and `dross doctor` lists every lane's state under `Exec consent:`.

A lane's `install` line is granted SEPARATELY, under `trusted_lane_installs`, through `dross trust --lane-install <name>`. That key is not writable through `dross local set` either — the reason is strictly larger there, since a generic key-writer would authorize changing a machine without ever showing you the line it was about to run on it. The split is deliberate: folding the install line into the lane's test fingerprint would staleness-refuse a lane's ordinary test runs the moment an install line was added — a line that has never executed breaking a gate that was passing — and installing has a different blast radius from running a suite. dross's own built-in install recipes need no grant; only a line this repo supplied does.

Per lane, not per repo, is the point: a one-character edit to a docs lane's command must not revoke the Go lane the pre-commit gate runs. Editing *or renaming* a lane revokes only that lane, and removing a lane drops its grant so a name re-added later starts untrusted rather than inheriting one.

## 15. Claude Code hooks

`dross hooks ensure` idempotently wires the four dross-owned hooks into user-level `settings.json`: `PreCompact` + `SessionStart`, and the tool-call gate pair — `PreToolUse` (`dross gate check`) and `PostToolUse` (`dross gate record`). The `SessionStart` hook is what prints the "you are here / next command" re-entry line at the top of a fresh session; without the gate pair the tool-call gates are off and `dross doctor` fails.

Offer to run it. It is safe to re-run — that is what idempotent means here — so the honest default when the user is unsure is to run it rather than to inspect `settings.json` by hand. If the re-entry line has stopped appearing at session start, this is the fix.

## 16. Constraints and competition (read-only)

`[constraints]` (a string map) and `[[competition]]` (an array of tables) are captured by `/dross-init` and `/dross-onboard` and have no dotted-path writer. Show them if non-empty so the user can see what the project is carrying; for changes, point at editing `.dross/project.toml` directly.

Do not invent these if absent — an empty `[constraints]` is the normal state for most repos.

## 17. Wrap

1. `dross validate` — surface any schema problems and offer to fix interactively.
2. `dross doctor` — surface any drift between `[remote]` and reality.
3. Print a compact one-line-per-category summary — never paste the full `project.toml` back:
   ```
   Reviewed 5 of 16 sections.
   Changed: project.description, runtime.test_command, remote.auth_env (3)
   Skipped: profile, paths (2)
   No changes: 7
   ```
4. `dross state touch "options reviewed"`.
5. End with a bottom-anchored `Next:` line:
   ```
   Options reviewed. Next: /dross-status — confirm where you are, then back to the spec→plan→execute loop.
   ```
