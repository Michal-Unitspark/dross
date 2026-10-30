# Dross

A leaner successor to [GSD](https://github.com/gsd-build/get-shit-done) for working with Claude Code on real projects.

> **Status:** v1.0 — the full plan → execute → verify → ship loop runs on phase-branch isolation (`dross phase create` auto-checks out `phase/<id>` and records the branch it forked from; `dross phase complete` finalizes post-merge against that recorded base, never an inferred one), with a milestone-branch topology layered on top (phase PRs squash-merge into `milestone/<version>`; the milestone itself lands as a merge commit — in `main`, or in the milestone branch it was stacked on while that one is still unmerged). Mutation testing covers TS/JS/Svelte (Stryker) and Go (Gremlins); C# (Stryker.NET) is fixture-tested but undogfooded. Every interactive command is a propose-and-react conversation — one decision per turn — under the `dross-interaction-contract` rule (see [Interaction](#interaction)). Since v0.3 the loop has grown considerably: slug-based phase identity with `dross phase {insert,move,rename}` lifecycle verbs (v0.4) and `dross task {add,remove,edit}` plan-editing verbs (v0.10); deferred-item routing and triage across specs (v0.5); container/IaC scanning, a GitLab ship provider, an ARCHITECTURE.md comprehension layer, and self-update via `dross update` (v0.6); non-interactive `dross ship --auto` and the milestone-branch model (v0.7); a native Claude Code statusline (v0.8); and release trust — minisign-signed releases with verify-before-swap self-update, `/dross-watch` (read-only board + phase-drift digest), and additional board backends (v0.9). Opt-in issue-board sync across six backends (Forgejo/Gitea/GitLab/YouTrack/Jira/GitHub — milestones/phases/quicks → board issues, inbound triage via `/dross-inbox`, proven end-to-end against Jira Cloud) lives behind `dross issue enable`; `/dross-pause` + `/dross-resume` capture and replay a mid-phase handoff; `/dross-plan` auto-runs an independent plan review (`--no-review` to skip) and offers `--panel` — a 3-lens planner panel merged by a cold judge; and context-free `/dross-secure` / `/dross-quality` audits (real scanners/analyzers + adversarial refute-panels) scaffold a remediation phase. See the per-milestone log under [Roadmap](#roadmap) for the full arc.

> Scope: Dross is built for my workflow. It's public because there's no reason not to be, but I'm not marketing it and I'm not trying to grow it into a general-purpose tool. The roadmap is a flat list because my todo list is — if Dross ever picks up users, I'll think about structure (semver, milestones, contribution guidelines) then.

> Contributing: I'm unlikely to accept feature PRs that don't match how I personally use this. Bug fixes and small quality-of-life improvements are welcome; new features probably aren't, unless we've talked first. If Dross is almost what you want but not quite, fork it — that's what AGPL is for, and you'll move faster owning your own copy than waiting on me.

## Why

Dross is built around three design pivots:

1. **Lean prompts.** Target ≤300 lines per slash command, with most state in machine-parseable TOML rather than prose Markdown — so a command boots cheaply and subagent spawns stay bounded and explicit.
2. **Pair-mode execute by default.** Code is authored *with* you, not delivered *to* you. Subagent spawns are kept to genuinely independent work (parallel mutation runs, multi-language audits).
3. **Test efficacy as a first-class gate.** It's not enough that tests *exist* — dross checks that tests *catch breakage*, via mutation testing (Stryker / Gremlins), coverage delta, and an LLM judge mapping each acceptance criterion to a specific test.

## The name

Dross is the AI sidekick from Will Wight's [Cradle](https://www.willwight.com/cradle) series — a Presence that lives in the protagonist's head, compiling battle plans, predicting opponents, crafting illusions, and handling "unimportant thoughts" to free up his bandwidth. Sarcastic, dramatic, fond of his person.

## Concept

```
intent ─► SPEC ─► PLAN ─► CODE ─► TESTS ─► EFFICACY PROOF ─► VERIFY
         (lock)  (waves)  (atomic   (per     (mutation +      (goal-
                          commit)   task)    coverage)        backward)
```

## Interaction

Every interactive dross command runs as a **conversation, not a broadcast**. The
contract is **propose-and-react, one decision per turn**: a command surfaces a
single decision, proposes the default it would pick, and lets you accept or steer
— never a wall of batched questions, and never a composed artifact (a spec, a
plan, a config) dumped back for blanket approval. Written artifacts are confirmed
with a one-line summary, not pasted in full.

So `/dross-spec` walks acceptance criteria one at a time rather than asking for all
seven at once:

```
spec › c-3  "returns 401 when the token is missing"
        accept · reword · drop ?            ‹ you pick, then it moves to c-4
```

That's the whole loop — you stay in it, steering as you go, instead of reviewing a
finished blob at the end. The invariant is the `dross-interaction-contract` rule
(`dross rule show`); the how-to playbook lives in `assets/prompts/_interaction.md`
and is delivered to each command verbatim via `dross interaction show`.

## Layout

```
cmd/dross/         Go CLI entrypoint
internal/          project, state, rules, profile, phase, milestone, changes, verify, mutation, codex, architecture, security, quality, stack, board
assets/commands/   Slash command markdown (installed to ~/.claude/skills/dross-<name>/SKILL.md)
assets/prompts/    Prompt instructions (installed to ~/.claude/dross/prompts/)
docs/dross.1       Man page — `man ./docs/dross.1`; print via `mandoc -T pdf docs/dross.1 > dross.pdf`
```

### Per-project artefacts

```
.dross/
├── project.toml      # vision, stack, runtime, paths, env, goals
├── rules.toml        # project-scoped rules (additive to global)
├── state.json        # machine-local: current position, version, activity log — gitignored, never committed
├── profile.toml      # optional project-specific profile overrides
├── debug/            # machine-local /dross-debug sessions (<slug>.md) — gitignored by their own .gitignore, never committed
├── milestones/
└── phases/
    └── <slug>/           # bare-slug identity (v0.4); ordering lives in the milestone's phases array
        ├── spec.toml
        ├── plan.toml
        ├── pr-triage.toml # tracked, rides the phase PR: one verdict per review comment (`dross pr resolve`), never a comment body
        ├── changes.json   # auto, written during execute
        ├── tests.json     # auto, written during verify
        └── verify.toml    # auto, written during verify
```

### Global install layout (after `make install`)

```
~/.local/bin/dross                     # CLI binary

~/.claude/skills/                      # one skill dir per slash command
├── dross-init/SKILL.md                # symlink → assets/commands/dross-init.md
├── dross-onboard/SKILL.md
├── dross-milestone/SKILL.md
├── dross-spec/SKILL.md
├── dross-plan/SKILL.md
├── dross-plan-review/SKILL.md
├── dross-execute/SKILL.md
├── dross-verify/SKILL.md
├── dross-quick/SKILL.md
├── dross-ship/SKILL.md
├── dross-review/SKILL.md
├── dross-respond/SKILL.md
├── dross-secure/SKILL.md
├── dross-quality/SKILL.md
├── dross-architecture/SKILL.md
├── dross-inbox/SKILL.md
├── dross-pause/SKILL.md
├── dross-resume/SKILL.md
├── dross-debug/SKILL.md
├── dross-status/SKILL.md
├── dross-options/SKILL.md
└── dross-rule/SKILL.md

~/.claude/dross/
├── defaults.toml                      # cross-project pre-fills + telemetry toggle
├── profile.toml                       # cross-project user profile (planned, not yet wired)
├── rules.toml                         # cross-project rules
├── telemetry.jsonl                    # local-only event log (see Telemetry section)
└── prompts/                           # symlink → assets/prompts/
```

Symlinks mean edits to `assets/` in the dross repo apply immediately — no re-install on prompt tweaks.

## Install

### Quick install (recommended)

```sh
curl -fsSL https://raw.githubusercontent.com/Rivil/dross/main/install.sh | sh
```

This downloads the latest release binary for your platform (`darwin`/`linux` × `arm64`/`amd64`), verifies its SHA-256 against the release `checksums.txt`, drops `dross` on your PATH (`~/.local/bin`), and runs `dross install` to materialize the slash commands and prompts into `~/.claude`. No Go toolchain or git checkout required.

If `~/.local/bin` isn't on your PATH, add it:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

Then in any Claude Code session, `/dross-init` (greenfield) or `/dross-onboard` (existing repo).

### Windows

```powershell
irm https://raw.githubusercontent.com/Rivil/dross/main/install.ps1 | iex
```

`install.ps1` is the PowerShell analog of `install.sh`: it detects your architecture (`amd64`/`arm64`), downloads the latest Windows release `.zip`, verifies its SHA-256 against `checksums.txt` **before** placing anything on PATH, extracts `dross.exe` into `%USERPROFILE%\.local\bin` (overridable via `DROSS_BIN_DIR`), adds that dir to your user PATH, and runs `dross install`. Prefer a manual download? Grab `dross_<version>_windows_<arch>.zip` from [releases](https://github.com/Rivil/dross/releases), verify it against `checksums.txt`, extract `dross.exe` onto your PATH, then run `dross install`.

Once installed, `dross update` self-updates on Windows too (it fetches the signed Windows `.zip`, verifies signature + checksum, and atomically swaps `dross.exe`).

> The self-update, archive extraction, and SHA-256 verification are unit-tested in Go, but `install.ps1` and a real end-to-end Windows binary run have not been exercised on a Windows host in this repo — treat the first Windows run as maintainer-verified, not CI-verified.

### Updating

```sh
dross update          # update to the latest release, if it is newer
dross update --check  # report the available version without applying
dross update --force  # reinstall the latest regardless of version
```

`dross update` fetches the latest GitHub release, verifies the tarball's SHA-256 against `checksums.txt` (refusing on mismatch), atomically replaces the running binary, then re-syncs the embedded slash commands + prompts.

#### Upgrading a repo that still tracks `.dross/state.json`

`state.json` holds machine-local position — current phase, version, activity history. It used to be tracked, and is now gitignored: a tracked copy gets dragged forward into every later tree by the squash, so one machine's position silently replays onto another's.

If your repo has branches cut before that change, the first branch switch onto one of them will **refuse** rather than proceed:

```
refusing to switch to <branch>: it would overwrite your live .dross/state.json.
```

That is the guard working. git overwrites an *ignored* working-tree file without complaint, so dross stops instead of letting the switch replace your live copy. The refusal prints the remedies in cheapest-first order, and the cheapest one that applies is the one to take:

- **Fast-forward** — offered when the branch is merely behind its remote and the remote no longer carries the tracked copy. Nothing to move aside.
- **Move aside and restore** — copy the live file, switch, copy it back. Always works, changes nothing permanently.
- **Untrack it for good** — `git rm --cached .dross/state.json` on that branch, and commit. This is the one that ends the problem: once no branch carries a tracked copy, the refusal can never fire again.

`dross doctor` reports the same condition, with the same fix, before you hit it.

#### `project set` and `state set version` are now surgical — retire the per-repo bans

Every dross command that writes `project.toml` — `dross project set`, `dross project set --unset`, `dross state set version`, `dross issue enable/disable`, `dross test lane add/edit/remove`, `dross stack apply` — now goes through one lossless write path: the file is patched on its raw bytes, so only the targeted line changes and every comment, hand-added key, indentation choice and line ending survives byte-for-byte. Lists (`stack.languages`), tables (`board.state_map`, `board.fields`) and array-of-tables blocks (`[[stack.locked]]`, `[[runtime.test_lane]]`) are patched the same way, one key or one block at a time; a whole-file re-encode only happens when the file does not exist yet.

Earlier versions re-encoded the whole struct on every write and dropped anything the schema did not model, which is why some repos carry a rule banning these commands (codriver.nvim's `r-01`, for example). Those bans are **safe to retire**: run `dross rule remove <id>` and let the commands write the file again.

### Manual binary download

GoReleaser publishes archives for `darwin/arm64` (primary), `darwin/amd64`, `linux/arm64`, `linux/amd64`, and `windows/arm64`+`windows/amd64` on every `v*` tag. Grab the matching archive from [releases](https://github.com/Rivil/dross/releases) — `.tar.gz` on macOS/Linux, `.zip` on Windows — extract, drop the `dross` binary (`dross.exe` on Windows) on your PATH, then run `dross install` to set up the slash commands and prompts.

### From source

```sh
make build       # builds ./dross for current arch (with commit + build date in `dross version`)
make test        # go test -count=1 ./...
make install     # builds + installs binary, then `dross install --link` (symlinks slash commands & prompts)
make doctor      # verifies install: PATH, binary freshness, symlink targets — exits non-zero on any issue
make uninstall   # removes binary, all dross-* skills, and the prompts symlink
make release-snapshot  # local goreleaser dry-run — produces dist/, never tags or pushes
```

After `make install`, ensure `~/.local/bin` is on your PATH:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

## Available commands

| Command | Description | Status |
|---|---|:---:|
| `dross init` | Bootstrap `.dross/` (greenfield) | ✅ |
| `dross onboard` | Adopt an existing repo (signal scan) | ✅ |
| `dross project {show,get,set}` | Read/write `project.toml` fields by dotted path. Every `[board]` field is settable — including `board.github_project`, individual `board.state_map.<status>` entries, and the `board.fields.{state,type,fix_versions}` tracker-field-name overrides, each addressed one key at a time. `set --unset <path>` clears a field written by mistake (a scalar, a single `state_map` entry, or one field override) without hand-editing TOML; `show` also accepts `--json` (bare document, no `#` header) | ✅ |
| `dross state {show,get,set,touch,bump}` | Read/write `state.json` (`bump internal` increments the 4th version segment). `get` takes one or more field names — one prints its bare value, two or more emit a single keyed JSON object; `show --json` is accepted for symmetry (state is already JSON) | ✅ |
| `dross local {get,set}` | Read/write `.dross/local.toml` — gitignored, machine-local values that must not ride cumulative history. Keys: `quick_base`, the branch a standalone `/dross-quick` committed to, which `ship` and `phase complete` reconcile alongside the phase's own base; and `allow_hosts`, comma-separated additions to the derived API host allowlist. `allow_hosts` is machine-local *because* it is the escape hatch — a committed one would let a cloned repo authorize its own API host, so dross refuses to read a `local.toml` git reports as tracked | ✅ |
| `dross remote {grant,status,revoke,bootstrap}` | Authorize (or inspect) a host that mutation runs execute on. `dross mutation remote grant <host> <workdir>` rsyncs this working tree there and runs the mutation adapters on that machine, as you — so it prints the host and workdir it is about to authorize **before** it writes them, and it is the only writer of `mutation_remote_host` / `mutation_remote_workdir`. Those two are deliberately absent from `dross local set`: a generic key-writer would let an agent grant code execution on another machine without ever showing you what for. The grant lives in the gitignored `.dross/local.toml`, so a clone carries none, a `local.toml` git reports as tracked is refused unread, and a remote host in the tracked `project.toml` is refused by name. Three companion keys **are** settable with `dross local set`: `mutation_workers` and `mutation_test_cpu` (parallelism knobs — unset keeps NumCPU/2 and 1, sized off the remote's core count when one is granted), and `mutation_remote_env`, a comma-separated allowlist of **variable NAMES** — never name=value pairs. dross reads their values from the surrounding environment at run time, pipes them to the remote over ssh stdin, and stores none of them; an allowlisted name that is unset locally is an error rather than an empty export. `dross remote grant <host> <workdir> --add` authorizes an **additional** host, keeping the one already granted: the extra candidates live under `[[remote_pool]]` in the same gitignored store, with the scalar grant as candidate zero, and declared order is the preference. Runs walk that list and take the first host that answers — a candidate that cannot be **reached** is skipped and announced, one that answered and **failed** is not, because re-trying elsewhere for a different answer is how a real failure gets laundered into a pass. Every surface that reports or acts on "the remote host" resolves it by walking that same list, so `dross doctor`, `dross remote status`, `dross remote bootstrap` and `dross test lane install` can never name a machine the next run would not use: with a pool declared and the first host down, all of them name the second. That is why `dross remote status` probes rather than echoing config, and why it reports **`granted, but unreachable`** — distinct from `not granted` — when the authorization stands and nothing answered; the remedies differ, and telling you to re-grant a host that is merely down is the wrong one. `dross remote bootstrap [--apply]` provisions **every** granted candidate, not just the one a run would choose — per-lane routing assumes the pool is in the state bootstrap puts it in, so bringing up one host leaves the next run's lane coming home for a tool nothing installed. Each candidate is planned from its **own** probe and reported in its own section — what installed, what was already present, and which machines could not be reached; an unreachable candidate is reported and skipped rather than aborting the rest, but any host left short moves the exit code and is named. It probes — in ONE round trip per host — for the tools the configured `[mutation].adapters` **and** every declared `[[runtime.test_lane]]` need, so a host's readiness for lanes and for mutation is never two separate answers, and installs the **packages** it can into a runtime that already exists there (gremlins via a pinned `go install`, a lane's toolchain from the built-in recipe table or that lane's own `install` line), while a missing language **runtime** (go, node, dotnet) is refused by name for its owner to install rather than installed. A lane tool with no recipe and no install line is reported without failing the run; an ungranted lane `install` line is a refusal, not an attempt — version policy and PATH ownership are not a mutation run's call. Dry-run by default, printing the exact command per missing tool; `--apply` installs; a provisioned host is a no-op; one tool's failure never aborts the rest and any refusal or failure exits non-zero. A run that needs the host also **preflights** it before pushing the tree, and a host it cannot reach falls back to a local run that says so — `verify.toml` records `measured_on`, so a local score and a remote one are never indistinguishable after the fact — it names **every** machine a run used, not just the first, and each `[[summary.leg]]` carries its own `measured_on`, so a run whose legs landed on different candidates says which numbers came from where rather than reading like a single-host run; a host that ran and failed does not fall back | ✅ |
| `dross rule {add,list,remove,promote,disable,enable,show}` | Two-tier rules system | ✅ |
| `dross phase {create,checkout,list,show,complete,insert,move,rename,number,migrate,red-proof}` | Phase directories + slug-based lifecycle. `create` auto-checks out a `phase/<id>` branch off the base so phase work never lands on main, and records that base — plus the commit it pointed at, the phase's durable fork point — in the phase's `changes.json`; `red-proof set <phase-id> --sha <sha> --doc <path> [--replay <cmd>]` pins the commit a phase's red proof was captured at alongside the doc that replays it — and optionally the command that replays it — validating all of them rather than letting a machine-checked record be hand-edited; `red-proof repoint [<phase-id>] [--apply]` repairs a pin whose commit origin can no longer reach, rewriting both the record and every occurrence in the replay doc to the owning phase's fork point, dry-run by default and leaving a sound pin untouched — where a replay is recorded and consented (`dross trust --replay <phase-id>`) it is re-run at the proposed commit and the repair is refused unless the proof still goes red, and otherwise the repair is reported unverified rather than implying it was checked. `dross ship` runs the same repair automatically for a pin its own squash-merge would orphan, so the rewrite rides that phase's PR; `checkout` switches to an existing `phase/<id>` through the guard that refuses a branch whose tracked `state.json` would overwrite the live machine-local one (it never creates the branch); `complete` finalizes after squash-merge (ff base, write the completion record, delete `phase/<id>` locally **and on origin**) against **that recorded base** rather than one inferred from the active milestone — and it is the sole writer of the completed-state transition, since `dross ship` only marks the phase `shipped` and leaves `current_phase` set until a merge is confirmed. Last, `dross phase complete` finalizes the board (with `[board].enabled` on): every task card closes at `task-complete` and the phase card at `complete`, and any card an earlier run never created is created closed. A board that cannot be reached never undoes the completion — complete exits non-zero naming `dross issue phase finalize <id>`, the same finalizer as a retry. It ends by stating the resulting branch topology, which `dross status` also carries as a standing `branch:` line — a phase with no record refuses and names `--base <branch>`, and the branch switch happens only after every check passes, so a refusal moves no local ref. `complete --recover` heals a diverged base by resetting it to origin and restoring `.dross/` from the phase branch's tip; it follows the recorded base, so it works under a milestone and never touches a branch the phase wasn't forked from; `list` prints every phase directory in milestone-array order, marking each done phase `✓ ` (two spaces when not) and ending with an `N/M done` footer — doneness read from the phase's own completion record, the same reader `dross status` and `dross milestone progress` count with, never a `verify.toml` verdict; a slug carried onto two milestones' roadmaps lists once, at its earlier position (`dross doctor` names it). `dross phase list --milestone <version>` scopes the listing to that milestone's `phases` array in roadmap order, including slugs with no phase directory (marked `(not scaffolded)` and counted in the denominator) and reporting that milestone's done/total; an unknown version is an error naming it. `insert`/`move`/`rename` edit a milestone's phase array and on-disk identity; `number` prints a phase's ordinal; `migrate` converts legacy NN-slug phases to bare slugs; `show` also accepts `--json` (bare document, no `#` header) | ✅ |
| `dross milestone {create,list,show,get,set,add,remove,replace,progress,complete,prune}` | Milestones with dotted-path edits (set scalars, add/`remove`/`replace` on list fields — `remove` and `replace` address an entry by its exact value, mirroring `add`, and `replace` keeps the entry's position so fixing a phase name is not a reorder; a value that isn't there is an error, not a silent no-op). `create` cuts `milestone/<version>` **conditionally** — from the current milestone's branch while that branch is still unmerged, from the main branch once it has merged, is gone, or there's no current milestone — and records that branch as the milestone's `base`, read back verbatim ever after rather than re-inferred (`dross milestone get base`; `create` is its sole writer, `--base <branch>` forces the cut point). `complete` opens the integration PR against **that recorded base** while it is still unmerged and against main once it has merged, so a stacked milestone's PR never shows its parent's commits as its own; `--finalize` records `[milestone].status = complete` **before** it fast-forwards main + deletes the milestone branch, accepting a merge into the recorded parent as well as into main; that marker makes the command safe to re-run — a second `--finalize` reports already-finalized and exits 0 (naming any leftover branch for `prune`) rather than asking a merge question the deleted branch can no longer answer, and a branch that is simply gone is reported as gone rather than as unmerged. Neither `--finalize` nor `prune` will delete a branch an unmerged stacked milestone still records as its base, or (when the provider is reachable) one that is an open PR's base — the check announces itself when it has to be skipped. `progress [version] [--json]` reports status, done/total, `remaining` and `unscaffolded` in one call — a phase counts done only when its own `changes.json` records it shipped or complete (a slug with no phase directory never does), so a milestone cannot close over work that was listed and never built; every arm exits 0, it is dispatch data rather than a gate. `show` also accepts `--json` (bare document, no `#` header) | ✅ |
| `dross task {next,list,show,status,add,remove,edit,move}` | Inspect and edit tasks within a plan. `list [phase-id]` prints the whole plan — id, wave, status, title — as an aligned table or `--json`, defaulting to `state.current_phase`; `add` appends or inserts (`--after`/`--before`) a task with a fresh high-water id; `remove` is dependency-safe (`--force` strips dependents); `edit` is a partial field update; `move` re-orders a task relative to another. `show` also accepts `--json`. `plan.toml` is mutated only through these verbs, never hand-edited | ✅ |
| `dross changes {record,show}` | Per-phase append-only log of what was touched; `show` also accepts `--json` (bare document, no `#` header) | ✅ |
| `dross verify <phase>` | Run mutation tests + write tests.json + verify.toml skeleton. The score covers **only the phase's own changed files** — every report is filtered against the phase's change set (git diff ∪ changes.json), so a survivor in an untouched file of the same package neither fails the phase nor inflates its score; filtered survivors are kept under `out_of_scope` in tests.json, and every survivor — in-scope or not — carries exactly one lifecycle state (`in-diff` / `routed` / `accepted` / `unclassified`), so an unclassified one is FLAGged for draining rather than re-listed inertly forever. `[summary].mutation_status` is `measured` / `unmeasurable` / `skipped` / `out-of-scope` so `/dross-verify` can tell a real low score from a 0/0 artefact and skip the score thresholds when there's nothing to measure. Every run records the tree it measured — `measured_commit` and `measured_tree` in verify.toml's `[verify]` table: the work tree with uncommitted edits, `.dross/` and `ARCHITECTURE.md` left out — so a change after the verdict can be told apart from it. On a granted host the leg takes the **host lock** first — one `flock` on `/tmp/dross-host.lock`, held once for the whole run across every package — so two mutation legs aimed at one host never overlap, whichever repo, workdir or unix user dispatched them: a leg that finds the host held **waits**, printing who holds it (`waiting on <project>/<phase> run <id> (pid N) since <time>`) once and a one-line heartbeat every few minutes, and runs the moment the holder releases. `--no-wait` refuses instead, naming the holder, with exit `15` and nothing written. A holder that dies — process killed, laptop suspended, host rebooted — releases the lock with **nothing to clear**: the lock is the kernel's, held on an open file descriptor, so there is no stale lock to remove by hand and no cleanup step. The host needs `flock` (util-linux); `dross doctor` reports it | ✅ |
| `dross verify <phase> --detach` | Start the mutation run on the granted host and return immediately, instead of holding the session for the length of the leg. The run is spawned under `setsid` so it outlives the ssh connection — the link can drop, the laptop can close. `--at <HH:MM>` (next occurrence) or an RFC3339 instant defers the start so a long leg lands off-hours; the wait happens on the host, against the **host's clock**, so nothing needs this machine awake to trigger it. Refuses when no granted host is reachable rather than falling back to a local run, because a local run is exactly what the flag exists to avoid. One run per phase: a second `--detach` is refused and names the one in flight. The detached job takes the host lock **inside** itself, after any `--at` sleep and before it starts the tool, so every detached run begins as `scheduled` and reads as such until it actually holds the host; a host without `flock` is refused before the tree is pushed | ✅ |
| `dross verify results <phase>` | Collect a detached run and write tests.json + verify.toml through the same pipeline an attached run uses, so a detached verdict is not a second-class one. Reads the report from the host **recorded at dispatch**, never from whichever host is granted now — a pool reordered between dispatch and collection can never make results arrive from a machine that did not measure them. Writes nothing unless the run finished. The tree it records is the one pushed at dispatch, never the collect-time one, and a file changed between dispatch and collect is named. Exit status is a contract, one code per state: `10` scheduled (not started yet), `11` running, `12` the run exited non-zero having produced nothing measurable, `13` the host was unreachable, `14` the run's directory is gone. Only `13` and `14` are problems; `10` and `11` mean the run is alive and fine. A scheduled run says **why** it has not started — waiting for its `--at` instant, or `waiting on <project>/<phase> run <id>` when another run holds the host lock — read from the same round trip, still exit `10`: waiting its turn is not a new state and not a failure | ✅ |
| `dross verify status [--cancel <phase>]` | List the detached runs this machine dispatched — phase, run id, host, dispatch time, state — reading the record without needing the network, and adding the host's live state only when it answers (anything unconfirmed is labelled `recorded`, not presented as observed). A run whose state is `scheduled` gets a second line naming what it waits on — the holder of the host lock (`waiting on <project>/<phase> run <id> (pid N) since <time>`), `not started yet` on a free lock, or `(could not read the host lock)` when the probe carried no answer. `--cancel <phase>` kills the run's process group on the host that holds it and drops the record; an unreachable host still clears the record, and says plainly that the run may still be running there | ✅ |
| `dross verify scope <phase>` | Print the last run's range provenance from tests.json: the in-scope files with their raw hunks, then per leg the effective ranges the tool was told (`start-end (<construct>)` — the top-level AST construct each range was widened to) and every whole-file fallback with its named reason (`adapter-lacks-range-runner`, `scope-has-no-hunks`, `file-absent-from-hunks`, `malformed-range`, `ast-unavailable`). A leg that ranged nothing never prints as ranged, and a record written before provenance existed says so. `--json` emits the same record verbatim; exits non-zero naming `dross verify <phase>` when no run exists | ✅ |
| `dross verify finalize <phase>` | Record resolved verdict from verify.toml as a telemetry outcome event (after `/dross-verify`) | ✅ |
| `dross status` | Where am I — project, phase, last activity, suggested next step. On the phase branch, a passing verdict that predates changed files is marked `stale` with their count, and the next step becomes `/dross-verify`. A `debug:` line names every open debug session | ✅ |
| `dross debug {new,list,close}` | The session files `/dross-debug` keeps under `.dross/debug/` (gitignored, machine-local). `new <slug>` scaffolds one from the fixed template; `list` prints each session's state (`open`, `needs-replan`, `resolved`, `abandoned`), hypothesis count, failed fixes since the last re-plan and last update; `close <slug> --fixed` needs two Resolution signals and a Prevention, and prints a `dross rule add` line without running it; `--abandoned --reason <why>` closes without them. A closed session stays on disk | ✅ |
| `dross profile {show,seed}` | User profile (with GSD import); `show` also accepts `--json` (bare document, no `#` header) | ✅ |
| `dross test [selector...]` | Run this repo's test suite — the one place dross spawns `runtime.test_command`, gated on `dross trust` and routed to a granted remote host when there is one. Trailing arguments are appended as a package/path selector. `dross test --files <path>` (repeatable) instead resolves the given repo-relative paths against the declared `[[runtime.test_lane]]` blocks and runs **only** the lanes they hit, so a pre-commit gate costs the code the task touched rather than the whole suite. In a repo that declares lanes but **no** `runtime.test_command`, a bare `dross test` runs every declared lane's own command, unscoped — no derived selector — each through its own grant, `prepare`, toolchain and locality path, worst outcome winning, so a lanes-only repo still has one command that measures its whole tree; where `runtime.test_command` is set the bare run is that command, byte-identical, and lanes never add to it. On a granted host the suite takes the same host lock a mutation leg does, but with a **bounded** wait: `--wait <dur>` (default `10m`; `--wait 0` spawns at once) is how long it waits for a leg to release the host, after which it spawns anyway and says so in one line — `waited 10m … running alongside it, sharing the host's cores` — and exits with the suite's own code. The lock exists to keep mutation measurements honest; a suite run under load is slow, not wrong, so a task gate is never held for the length of a mutation leg. A host without `flock` is warned about once and the suite runs unlocked; `--local` never touches the lock. Exit status is a contract: `1` red suite, `2` a `--files` path outside the repo, `3` host unreachable, `4` incomplete transfer, `5` the file set matched no lane, or every matched lane's selector collected nothing (nothing was measured — never reported green), `6` a matched lane is untrusted or stale, `7` a matched lane's `prepare` failed, so that lane's tests never ran, `8` a matched lane's toolchain is on **neither** the granted host nor this machine, so the lane never spawned — a missing binary is not a red suite. A lane whose toolchain the chosen host lacks does not fail and does not automatically come home: it goes to the first **other granted candidate** that has it, and runs on this machine only when no candidate does — local is the last resort, not the second option, because coming home for a tool the pool is holding is a slower run measured on the wrong machine. Every move is announced before the lane runs, naming the lane, the host it went to, the host it did not, and the binary that decided it; a run whose lanes landed on more than one machine also says so in one line — `this run is split across N machines — <lane> on <host>, …`, counting this machine — because two runs measured on different candidates are not interchangeable evidence and a transcript that read identically would hide that. Decided per lane from **one probe per candidate**, asked for the union of every matched lane's toolchain, taken before the tree sync and stopped as soon as some candidate covers every tool — so a whole-suite run still costs exactly one probe, a run where nothing can go remote never pays for the transfer, and the tree is pushed once to each host a lane is actually going to and to no other. A lane whose toolchain is on **no** candidate and not on this machine is still refused rather than spawned (exit `8`), and the refusal names every host that lacked it. Where outcomes co-occur the worst one wins, ordered by how badly it would mislead: transport > partial > prepare > toolchain > red > refused > nothing-measured | ✅ |
| `dross test lane {add,list,edit,remove,install,preview}` | Declare test lanes without hand-editing `project.toml`. `dross test lane add <name> --match <glob> --command "<cmd>"` writes one block — `--match` is repeatable, `**` crosses directories and a trailing `/` means everything beneath; `dross test lane list` prints each lane's globs **and** its command line, since that is the value consent binds to. `dross test lane remove <name>` also drops that lane's grant, so a name re-added later starts untrusted rather than inheriting one. A lane may also declare `--selector <style>` — `path` | `dir` | `go-package` — and dross appends the matched paths, in that shape, to the lane's command, so a task touching one file runs `go test './internal/cmd/...'` rather than the lane's whole suite; the lane header prints the exact line that ran, selector included. Repeatable `--empty-exit <code>` names the exit status this lane's runner uses for "collected no tests" (pytest's `5`): that lane is then reported as a **selector miss** naming the lane and the selector, and does not fail the gate — though a run where *every* matched lane missed still exits `5` rather than reporting green. Both flags are opt-in and validated before the write, and `dross test lane list` prints them only for the lanes that declare them; a lane declaring no selector runs its command untouched. The consent grant still covers the lane's declared `command`, so appending a selector neither invalidates an existing grant nor lets an ungranted lane run. A lane may declare `--prepare "<cmd>"`: a bootstrap line — `make build`, `docker compose up -d` — that runs after the tree sync and **before** that lane's own command, on the same host and through the same transport, so a cold remote host is prepared exactly as this machine would be. It is not deduplicated across lanes: idempotence is the declared contract, so two lanes sharing a prepare each run their own. A prepare that fails skips **only** its own lane's tests and exits `7`, distinct from a red suite — the run's other lanes still run. The lane's single consent grant covers the prepare **and** the command together, so adding or changing a bootstrap line stales the grant rather than riding one issued for the command alone, and `dross trust --lane <name>` prints both lines before writing. A lane may declare `--toolchain <binary>` (repeatable) to override the binaries it needs on the host that runs it; omitted, the list is **derived** from the first token of its command and prepare, so locality works on every lane already declared without an edit, and the override is the escape hatch for the lines derivation gets wrong (`FOO=1 go test`, a wrapper script, `mise exec`). `dross test lane list` prints `toolchain:` for **every** lane, marked *derived* or *overridden*, so the probe set is inspectable without reading `project.toml`. A lane may also declare `--selector-template "<tmpl>"` — where the matched paths land instead of being appended, for a runner the three styles cannot shape: `--selector-template "--package {path}"` gives cargo its repeated flag, `--selector-template "-R {paths}" --selector-join "|"` gives `ctest -R` one joined regex. `{path}` repeats the whole template once per derived path; `{paths}` substitutes them all into a single instance, as separate arguments unless `--selector-join <sep>` collapses them into one. A template requires a `--selector` (the style shapes the values, the template places them) and must contain a placeholder, or it would scope nothing while claiming to; any other `{...}` run is ordinary template text, so a regex quantifier like `a{2,3}` is safe. Unlike a bare selector, a template **is** part of the consent line — it is arbitrary text on the spawned line — so adding or changing one stales the grant, and substituted paths are shell-quoted and option-safed so a matched path can never become a second command. `dross test lane list` prints `selector-template:` and `selector-join:` beside a lane's `selector:` — through the same renderer `lane add` and `lane edit` echo, so there is no second rendering to drift — which means every field that shapes a lane's derived line is readable from the listing; both stay opt-in, printed only for the lanes that declare them. Declaring a selector or template on a command whose last token is `./...` or `.` prints a ⚠ naming the lane and the token, at declaration time and on every `dross validate` run, because appending a scoped selector to a whole-tree token runs the union — the "scoped" run would silently be the whole suite. The lane is still written and validate still exits 0. `dross test lane edit <name>` sets or clears **any** of a lane's fields in place — `--match`, `--command`, `--prepare`, `--selector`, `--selector-template`, `--selector-join`, `--empty-exit`, `--toolchain`, `--install` — keeping the lane's name, its position in the document **and** its grant. Only the lane's **name** is remove-then-re-add, because names key the grant store and cannot move without the grant moving. A change to the consent line reads as *stale* rather than *never trusted*, which is the honest reading remove-then-re-add threw away; `--toolchain` and `--install` sit outside that grant and leave it alone. Pass `""` to any flag to clear it. An edit that would leave the lane malformed is refused before anything is written, through the same rules `dross validate` reports — and the gate reads the proposed lane alone, so a lane broken elsewhere in `project.toml` never blocks the edit that would fix it. `dross test lane install <name> [--apply] [--local]` installs the toolchain a lane is missing, on the machine that is missing it — the granted host when the lane would have run there, this machine when this machine is the one short of the binary — and every line says which machine it acted on. `--local` forces the install here whichever side the probe found the gap on, for a granted host that is up but is not the machine you want provisioned. Dry-run by default: it prints the exact command it would run and changes nothing, and `--apply` is the only path that touches a machine — a `dross test` fallback never installs as a side effect, it just names this verb in the same breath as the fallback. An install command is never guessed: it comes from a built-in recipe (package-class tools going into a runtime that already exists) or from the lane's own `--install "<cmd>"` line, which replaces the built-in recipe for that lane; a language runtime is refused by name for the host's owner to install, and a tool with neither a recipe nor an install line is reported — "add one or install it by hand" — without failing the command. A declared `--install` line carries its **own** grant, `dross trust --lane-install <name>`, separate from the lane's test grant: adding one to a lane that already runs green never stales its ordinary test runs, and an ungranted line is refused rather than executed. Nothing is written by an install: the next run routes that lane on the probe's answer alone. `dross test lane preview [paths...] [--files <path>] [--lane <name>] [--no-probe] [--json]` shows what the gate **would** do with a file set and spawns nothing — no lane command, no `prepare`, no tree sync. It resolves the paths through the same code `dross test --files` resolves through, so the line it prints is the line that would run rather than a second derivation beside it. There is no lane positional: trailing paths join the `--files` set and `--lane <name>` narrows the answer to one declared lane. With no paths at all the file set is the working tree's uncommitted changes — staged, unstaged and untracked — and it says how many files it took. Each rendered lane carries its consent state (`granted`, `stale`, `absent`) so a line that would be refused at run time is visible as refused before it is run, and where it would run: the granted host, `local` with the absent binary named when the lane would fall back, or `unresolved` naming the configured host when it was not probed (`--no-probe`, which opens no connection at all) or could not be reached — never `local`, since only a probe can prove a fallback. `--json` emits the same findings as a bare document, no `#` header, in the same vocabulary the transcript prints. Preview **always exits 0** except on a real fault — an unreadable `project.toml`, an unknown `--lane` name: `no lane matches`, a path outside the repository, a path that is no longer on disk and a lane whose selector scoped to nothing are all printed findings, each naming the path and its reason, so a verb that ran no test can never be wired into CI as a gate. Lanes are opt-in — a repo declaring none runs `runtime.test_command` byte-identically, with or without `--files` | ✅ |
| `dross trust [--check\|--lane <name>\|--lane-install <name>\|--run <name>\|--replay <phase-id>]` | Consent to dross spawning a command on this machine, recorded as a hash in the gitignored `.dross/local.toml` — a clone carries none, and editing the command revokes it. Bare, it covers `runtime.test_command` (`trusted_test_command`); `dross trust --lane <name>` covers one test lane's command (`trusted_lane_commands`, keyed by lane name, so one stale lane refuses only itself rather than blocking every other lane's gate); `--lane-install <name>` covers that lane's `install` line (`trusted_lane_installs`, a **separate** grant — installing changes a machine and running a suite does not, so adding an install line to a lane that already runs green must not stale the gate that was passing); `--run <name>` a `[runtime]` slot; `--replay <phase-id>` a recorded red-proof replay. Every form prints the exact line **before** it writes, and none of the grant keys is writable via `dross local set` — a generic key-writer would let an agent grant code execution without ever showing you what for. `--check` exits 0 when current and distinguishes absent from stale otherwise; `dross doctor` reports every grant's state, including one row per lane | ✅ |
| `dross gate {check,record,off,on,status}` | Claude Code tool-call gates. `check` (PreToolUse) and `record` (PostToolUse) are the hook verbs, wired into the user-level `settings.json` and refused when the agent runs them by hand. `dross gate off <name> [--for 1h]` lifts a misbehaving gate for at most 24h — a workflow gate (`plan-edit`, `curated-shrink`, `commit-green`, `pair-approval`, `solo-review`) for the current dross repo only, a secret gate (`secret-stream`, `secret-read`) machine-wide, and the output says which; `gate-off-guard` and `tamper-guard` cannot be lifted. The agent's Bash tool cannot run `gate off`, so a lift is always a human's, from their own terminal. `gate on <name>` ends a lift early; `gate status` lists every gate with its scope and any lift's expiry, and names an unreadable override store rather than showing every gate quietly on | ✅ |
| `dross validate` | Schema-check every artefact | ✅ |
| `dross codex <file>` | Polyglot code insight — symbols, refs, siblings, recent activity. Go via stdlib `go/ast`; TS/TSX/Svelte/C#/GDScript via `ast-grep` shell-out (graceful no-op if ast-grep not on PATH) | ✅ |
| `dross security {detect,run,scaffold,findings}` | Deterministic surface of the `dross-secure` audit — run dirs, scanner detection, findings→spec scaffold, cross-run `findings` ledger (list/state/reconcile); audit orchestration lives in `secure.md` | ✅ |
| `dross quality {detect,run,scaffold,findings}` | Deterministic surface of the `dross-quality` audit — run dirs, analyzer detection, maintainability-risk scaffold, cross-run `findings` ledger; orchestration in `quality.md` | ✅ |
| `dross stack {detect,show,list,apply,loadout}` | Stack profiles — detect the stack, show/list profiles, `apply` re-syncs `[runtime]`, `loadout` emits the agent loadout block. Embedded built-ins + `~/.claude/dross/profiles/` drop-ins (user wins). Go/TypeScript/Kotlin/Dart/Svelte/SQL profiles ship, plus a marker-file Docker profile; `show` also accepts `--json` (bare document, no `#` header) | ✅ |
| `dross doctor` | Project-level health check: foundational files exist (`project.toml`, `rules.toml`), `state.json` is untracked, `project.toml` and `state.json` agree on the version, no stale `milestone/*` branches, `[remote]` ↔ git origin matches, `auth_env` exported, `.gitattributes` marks `.dross/` linguist-generated, no phase commits leaked onto main, every recorded red proof still pins a commit **reachable from origin** (a pin whose commit no `origin/*` ref contains is an issue naming the doc, the SHA and the owning phase's fork point to repoint to; a shallow clone or a repo with no origin refs is a warning that leaves the exit code alone, since an absence of history is not a verdict). The Remote section probes the granted host for `flock` beside the adapter tools — missing, it is an issue in the host lock's own words (`flock is not installed on <host> — the host lock needs it`, util-linux) — and with it present reads the lock through the same `flock -n` probe a run uses: `host lock free`, or an advisory `host busy: held by <project>/<phase> run <id> (user, pid) since <time>` naming the current holder, which is the lock working rather than a fault. The **Pin currency** section checks the pins Dependabot cannot reach — a `go install …@vX.Y.Z` in a workflow `run:` block or composite action, setup-node's Node version (inline or via `node-version-file:`), goreleaser-action's `version:`, go.mod's `toolchain` — against each upstream's release list: a pin with a newer release on its own line that has been out more than 7 days is a warning naming the version to bump to, an unpinned site warns, and an unreachable upstream or a newer major is a plain line. It is always on with a short per-host timeout and never moves the exit code, so an upstream release can't block `/dross-ship`. In the dross repo the same checker runs strictly in the weekly **Pin currency** workflow (`.github/workflows/pin-currency.yml`, `go run ./cmd/pincheck`). There a stale, unpinned or unreachable pin turns the run red, and a write-scoped bump job rewrites the stale pins and opens one PR (or updates the open one). It dispatches CI on the PR's branch and never force-pushes over a commit the bot didn't write | ✅ |
| `dross defaults {show,save}` | Read/write `~/.claude/dross/defaults.toml` (cross-project pre-fills); `show` also accepts `--json` (bare document, no `#` header) | ✅ |
| `dross env {list,set,unset}` | Manage env keys in `~/.claude/settings.json` (hidden input, never echoed) | ✅ |
| `dross ship <phase-id>` | Push `phase/<id>` to the provider, open PR, request reviewers. Provider's squash-merge collapses per-task commits. Refuses a pass verdict whose measured tree no longer matches the work tree — a file outside `.dross/` and `ARCHITECTURE.md` changed, appeared or went away since the run — naming the changed files; `--force-unverified` overrides. A verdict recorded before trees were kept ships with a freshness-unknown warning | ✅ |
| `dross ship comment` | Post a markdown comment to a PR via provider (used by /dross-review) | ✅ |
| `dross pr {comments,resolve,reply}` | The review comments on a phase's PR (backs `/dross-respond`), on every provider `dross ship` reaches. Every verb binds the PR to its same-repo `phase/<id>` head and refuses unless HEAD is that branch. `comments <pr> [--all]` lists the conversation comments, inline comments (with `path:line`) and review summaries still needing a verdict — or edited since theirs — each as one metadata line (id, kind, author, bot/human/unknown, status, `seen=<digest>`) and its body fenced as untrusted data, redacted; a `/dross-review` comment you posted is split into one item per finding, and your own replies are left out. `resolve <pr> <id> --seen <digest>` records exactly one verdict — `--accept --title` (a new task in the phase plan), `--reject --reason`, or `--route --title --target` (a deferred item) — with exactly one piece of evidence, `--at <path:line>` or `--cmd <c> --output-file <path\|->` (the command is recorded with its output's sha256, never run), into the tracked `pr-triage.toml`. `reply <pr>` drafts one comment listing the unposted rejections and prints the exact AskUserQuestion label that approves it; `--post` sends it only when that label's human answer is recorded | ✅ |
| `dross ship recover` | One-shot migration tool for legacy repos with phase commits on main or `.dross/` stripped from prior PRs — fetch + reset + restore `.dross/` + commit, atomically | ✅ |
| `dross protect [--apply\|--check <branch>]` | Branch protection on GitHub: a `dross: main` ruleset (no deletion, no force push, PR required, every `pull_request` job of origin/main's workflows a required check) and a `dross: milestones` ruleset (PR required into `milestone/*`), both with an empty bypass list — admin included. Previews by default; `--apply` writes both, turns on `allow_auto_merge` and reads main back, exiting non-zero on any gap. Warns when `allow_merge_commit` is off (chore and milestone PRs need it). `--check <branch>` prints exactly `protected`, `unprotected`, `unknown` or `not checked (<provider>)` and always exits 0 — what the quick/ship/milestone prompts route on | ✅ |
| `dross issue {enable,disable,phase sync,phase finalize,task sync,task pull,milestone sync,backlog sync,quick,pull,dismiss,link,list,reap}` | Opt-in issue-board sync (Forgejo/Gitea/GitLab/YouTrack/Jira/GitHub). Mirrors milestones/phases/quicks → board issues (idempotent), pulls inbound issues for triage. Off by default; configured under `[board]` (`provider` + `base_url` + `auth_env`, plus `auth_user` for Jira). `[board.fields]` overrides the tracker-native field names sync writes to (`state`, `type`, `fix_versions`) so a renamed field — or a non-English tracker UI — syncs without a code change; unset keys keep today's literals. **Proven end-to-end against Jira Cloud (2026-07-25)** — full issue milestone sync → issue phase sync → issue pull round-trip; other backends wired but not yet dogfooded. `dross issue reap` sweeps mirrors the forward lifecycle left stranded: it classifies every dross-authored card from the on-disk record (never the card's own state), discovers cards no board.json namespace links via the `dross` marker label, prints the plan by default and writes only under `--apply`, isolates per-card failures, and reverses the last run with `--undo`. It also creates the cards a completed phase is missing — the phase card or any task card no tracker lookup finds — at their terminal state, closed, through the same finalizer `dross phase complete` runs. A routed deferred item's mirror closes only on its disposition record (its survivor accepted or measured away, or a criterion of its complete destination that absorbed it), never because the destination shipped. `dross doctor` and `/dross-watch` report the stranded count. | ✅ |
| `dross stats {show,path,opt-in,opt-out}` | Aggregates over the local telemetry log; toggle the recorder; `show` also accepts `--json` (bare document, no `#` header) | ✅ |
| `dross architecture check` | Inspect/repair `ARCHITECTURE.md` symbol links — reports drift, `--fix` re-resolves `file:line` targets | ✅ |
| `dross deferred {list,route,unroute,dismiss,add,absorb}` | Inspect and route deferred items across phase specs; `add` files a new one from the command line — into the current phase's spec, or a project-level store when there's no usable phase home. Routing to a complete phase is refused. `dross deferred absorb <source> <idx> --criterion <c-N>` records that a criterion of the item's target phase absorbed it — the record backlog sync and reap close its board mirror on | ✅ |
| `dross survivor drain` | Answer "does this repo have standing debt?" — classify every surviving mutant across **every** Go package (no diff scoping, unlike `dross verify`) and report the ones carrying no disposition, exiting non-zero if any remain. A survivor routed to the phase being drained counts outstanding, so the backlog can't be closed by re-routing it to itself, and a package the adapter wrote no report for is a hard failure rather than a package that looks clean. `--report` classifies a recorded gremlins report; `--packages` narrows the scope | ✅ |
| `dross survivor {accept,route,list,retire}` | Drain surviving mutants: `accept` records one in the tracked repo-level `.dross/survivors.toml` with a mandatory reason (the only state that silences it), `route` parks it as a deferred item carrying its identity key plus a destination phase, `list --stale` surfaces acceptances whose subject is gone, `retire <key>...` (or `retire --stale`) removes them again so the store never needs hand-editing — `--stale` retires exactly the acceptances whose subject is gone and never one that merely couldn't be checked. Identity is file + op + a hash of the mutated line's normalized text, so it survives line drift but never a genuinely changed line | ✅ |
| `dross techdebt` | Scan tracked files for tech-debt markers + size heuristics (`.dross/techdebt/<id>`) | ✅ |
| `dross watch` | Read-only digest of board inbound, phase drift and open bot/ship PRs since the last tick (backs `/dross-watch`) | ✅ |
| `dross statusline` | Render the Claude Code status line (reads status JSON on stdin) | ✅ |
| `dross update {--check,--force}` | Self-update to the latest signed GitHub release — verifies minisign signature + SHA-256 before atomically swapping the binary, then re-syncs commands/prompts | ✅ |
| `dross version` | Print version, commit, and build date | ✅ |

**Slash commands:**

| Command | Status |
|---|:---:|
| `/dross-init` | ✅ |
| `/dross-onboard` | ✅ |
| `/dross-rule` | ✅ |
| `/dross-milestone` | ✅ (on a protected `milestone/*` — `dross protect --check` reads `protected` — `.dross` chores reach the branch through a chore PR, which merges before the integration PR opens; `--finalize` still deletes the branch) |
| `/dross-spec` | ✅ |
| `/dross-plan` | ✅ (`--panel` for 3-lens planner panel + cold judge; auto-runs plan review unless `--no-review`) |
| `/dross-plan-review` | ✅ (own context — cold subagent; also auto-run by `/dross-plan`) |
| `/dross-execute` | ✅ |
| `/dross-verify` | ✅ |
| `/dross-quick` | ✅ (one-shot task with atomic commit + test gate; bumps internal version. Standalone on a protected base — `dross protect --check` reads `protected` or `unknown` — it works on `quick/<version>` and opens a PR into the base that carries its `.dross` bookkeeping too, so no chore PR is needed) |
| `/dross-status` | ✅ |
| `/dross-pause` | ✅ (capture a handoff before stopping — thread + next action + open loops) |
| `/dross-resume` | ✅ (replay the handoff, prune what's done) |
| `/dross-debug` | ✅ (debug across sessions — one hypothesis per probe, evidence in a gitignored `.dross/debug/<slug>.md` before the next, BLOCKED after three failed fixes, fixes routed to `/dross-quick` or a task; the SessionStart line names an open session) |
| `/dross-inbox` | ✅ (triage inbound board issues → phase / milestone / quick / dismiss) |
| `/dross-watch` | ✅ (read-only heartbeat — board inbound + phase-drift digest, ends with one suggested next command; a ship PR with review comments waiting shows `· <n> untriaged — /dross-respond <pr>` on its line) |
| `/dross-options` | ✅ |
| `/dross-ship` | ✅ (CI watch + merge gate + branch cleanup; on a protected base — `dross protect --check` reads `protected` — `.dross` chores go through an auto-merging chore PR, and `dross phase complete` waits for it rather than offering `--recover`) |
| `/dross-review` | ✅ (4-lens subagent panel: security / quality / tests / spec-fidelity) |
| `/dross-respond` | ✅ (pair-only triage of a phase PR's review comments: each claim checked against the code, then accepted as a task, rejected with a reason or routed as deferred; the rejections go back in one reply posted only on the user's recorded approval) |
| `/dross-secure` | ✅ (context-free multi-pass security audit: real scanners + adversarial refute-panel; scaffolds a remediation phase) |
| `/dross-quality` | ✅ (multi-pass code-quality audit: real analyzers + refute-panel over substantive maintainability dimensions; scaffolds a remediation phase) |
| `/dross-architecture` | ✅ (generate/refresh the feature-organized `ARCHITECTURE.md` from a scan of code + git history) |

Legend: ✅ working · 🚧 stub / partial · ⏳ not started

## Roadmap

- [x] Skeleton: types, CLI, rules system, init/onboard, validate
- [x] Tests: round-trip, merge, parser, validate checks
- [x] `/dross-spec` and `/dross-plan` slash commands
- [x] `/dross-execute` (pair-mode default, `--solo` opt-in) + task/changes CLI helpers
- [x] `/dross-verify` + Stryker adapter for TS/JS/Svelte mutation testing
- [x] Gremlins adapter for Go mutation testing
- [x] GoReleaser cross-compile (darwin/arm64 primary, +amd64, linux arm64/amd64) on `v*` tags
- [x] `[remote]` capture in init/onboard with two-tier defaults (Forgejo / GitHub / Gitea / Bitbucket)
- [x] `/dross-options` full settings editor + secret-safe `dross env` for `~/.claude/settings.json`
- [x] `/dross-ship` — squash + filter `.dross/`, provider-aware PR open (GitHub + Forgejo), human reviewer assignment
- [x] `/dross-ship` CI watch + merge gate + branch cleanup — watches provider checks, fixes failures, prompts to merge, deletes remote and local PR branches
- [x] `/dross-milestone` — slash command + `dross milestone {get,set,add}` dotted-path edits, Brief.md-aware scoping
- [x] `/dross-spec` smart no-args routing — offers to scaffold the next phase when nothing else is in flight
- [x] `dross stats` + local-only telemetry — single-developer event log to surface friction, opt-out via `dross stats opt-out` or `DROSS_NO_TELEMETRY=1`
- [x] Builtin `.dross/` commit-hygiene rule baked into every prompt's pre-flight
- [x] Ship filter rewrite — runs in an ephemeral worktree so the user's gitignored `.dross/` is never destroyed
- [x] Ship `--preserve-history` — alternative filter that keeps per-task commits, `.dross/` stripped from each
- [x] `/dross-review` four-lens subagent panel — spawns security / code-quality / test-efficacy / spec-fidelity reviewers in parallel and posts an aggregated comment to the PR
- [x] Mutation adapter: Stryker.NET (C#) — modeled from public Stryker.NET docs, JSON shape shared with Stryker.JS, fixture-tested; real-world verify pending a C# project to dogfood against
- [x] Codex polyglot indexer — Go via stdlib `go/ast`, TS/TSX/Svelte/C#/GDScript via `ast-grep` shell-out. Graceful degradation when ast-grep isn't installed (other commands keep working). HTML/CSS get sibling + git-log enrichment only (no symbols)
- [x] `/dross-quick` — one-shot task with atomic commit + `runtime.test_command` gate, pair-mode only. Bumps `state.version`'s internal counter (`dross state bump internal`). Works inside a phase (recorded as `quick-N` in `changes.json`) or standalone
- [x] Telemetry signal upgrades — finer error classifier (no_phase / no_spec / no_plan / verify_state / mutation / provider / unknown_field / cli_args / cancelled / check_issues), cmd path captured even when cobra fails to resolve, `dross status` surfaces unfinalized verify verdicts, doctor emits outcome events instead of bucketing as `err=other`
- [x] Issue-board sync (opt-in) — `dross issue {enable,milestone sync,phase sync,quick,pull,dismiss,link}` mirrors dross planning onto a Forgejo/Gitea board: milestone → board milestone, phase → issue (with a task checklist rendered from `plan.toml`), quick → standalone issue. Status flows via a `dross` marker + `dross/status:*` label and closes on ship. Outbound push is wired into the milestone/plan/execute/verify/ship/quick prompts as a no-op-when-disabled CLI call; inbound bugs/feature-requests are pulled by `/dross-inbox` (and surfaced as a passive count in `/dross-status`) and triaged into a phase / milestone backlog / quick / dismiss. Links live in `.dross/board.json`; reuses the `ship` provider config (`api_base`/`auth_env`). GitHub issues backend deferred (`gh issue`)
- [x] Phase-branch model — `dross phase create` auto-checks out `phase/<id>` off main; `dross phase complete` ff-merges main and deletes the local branch; `dross ship` pushes `phase/<id>` directly (no synthetic squash) and the provider's squash-merge collapses per-task commits on merge. Removes the divergence pattern that previously required manual recovery commits. `.dross/** linguist-generated=true` scaffolded into `.gitattributes` on init/onboard so review UIs collapse planning artefacts without filtering them from history. Doctor checks foundational files, the linguist attr, and phase commits leaked onto main. `dross ship recover` retained as one-shot migration for repos already in the divergent state.
- [x] Handoff pause/resume — `/dross-pause` drafts a living handoff at `.dross/handoff.md` (thread + next action + open loops, gitignored, single file), `/dross-resume` replays it and prunes done items in place, and `dross status` nudges when one is open. Closes the "stop mid-phase, next session the brain blanks out" gap that mechanical state (`current_phase`, task progress) doesn't cover
- [x] Verify verdict hardening — `[summary].mutation_status` (`measured | unmeasurable | skipped`) distinguishes a real low score from a 0/0 artefact, so a phase whose changes fall entirely outside the project's Stryker scope (or runs with `--skip-mutation`) no longer false-fails the 0.60 threshold; `/dross-verify` now bases the verdict on criterion coverage alone when nothing was measurable. Forgejo/Gitea `dross issue phase sync` no longer spams `cannot unmarshal array into ... issueResponse` — the labels-PUT response is now correctly treated as a `LabelList` instead of an issue. New `no_milestone` error bucket peels bare `dross milestone show` failures out of the opaque `other` pile.
- [x] Plan quality loops — `/dross-plan --panel` fans out three cold lens planners (risk-first / MVP-first / verification-first) over the locked spec in parallel, a fourth cold judge merges them (winner-as-skeleton + grafts) and surfaces lens *disagreements* as the steering agenda instead of auto-resolving them; artifacts kept in `.dross/phases/<id>/panel/`. `/dross-plan` now auto-runs the independent plan review (own-context cold subagent) after `plan.toml` is locked, with one bounded fix-and-re-review cycle on blocking findings; `--no-review` opts out. Panel costs ~4-5× a single-pass plan — meant for new subsystems / non-obvious task graphs, not 2-task UI phases
- [x] Spec gray-area discussion — `/dross-spec`'s locked-decisions step is no longer a passive "any decisions?" prompt. It analyses the phase against project goals, milestone constraints, locked stack, and the acceptance criteria, then surfaces 3–4 *phase-specific* gray areas (concrete labels, never generic categories), lets the user `multiSelect` which to pin down, and deep-dives each one at a time — outcomes land as `[[decisions]]` (locked, with a real `why`) or `[[deferred]]`. Skips anything already settled by `stack.locked` or a prior phase's decision; routes scope-creep into deferred ideas. Ported from GSD's `discuss-phase` question phase, folded into the existing spec flow rather than added as a separate command/artifact

### Milestone v0.1 — comprehension, security & quality surfaces (complete)

- [x] `ARCHITECTURE.md` comprehension layer — a single feature-organized doc at repo root (one entry per capability, never per phase/module) with a fixed entry template (heading + one-line + symbol links + provenance). Seeded greenfield at `dross init`, backfilled by `/dross-architecture` from a code + git-history scan, and kept current by `/dross-ship` folding each phase's landmarks into the matching feature entry in place
- [x] `dross status` non-spine action surfaces — when the spec→ship spine has nothing runnable, status surfaces idle-gated action areas (security / quality / tech-debt) instead of dead-ending; gated so it only shows between phases, not mid-flow
- [x] `/dross-secure` + `dross security` — context-free, read-only multi-pass security audit: real scanners (govulncheck/gosec/gitleaks/semgrep/trivy/osv-scanner…) plus an adversarial refute-panel over cold subagents, emitting a verified findings ledger that scaffolds a remediation phase. Tool-grounded (no LLM-guessed findings), no `--fix`
- [x] `/dross-quality` + `dross quality` — comparable multi-pass code-quality audit: real analyzers (gocyclo/dupl/deadcode/errcheck/ineffassign + agnostic scc/jscpd) over substantive maintainability dimensions, refute-panel verified, scaffolds a remediation phase. Diverges from secure on a downrank-only (never-suppress) context model
- [x] Stack profiles + `dross stack` — declarative per-stack profiles (embedded built-ins + `~/.claude/dross/profiles/` drop-ins, user wins on id) that tune runtime commands, the security/quality tool loadout, and the agent loadout to a detected stack. Signal-scored detection → matched id or `unsupported`; `apply` re-syncs `[runtime]`; `loadout` emits a markdown block the execute prompt injects inline. Adding a stack is a single TOML drop-in. Go-first

### Milestone v0.2 — multi-language stack profiles (complete)

- [x] Embedded profiles for Kotlin / Dart / Svelte / SQL + `extLang` detection additions, each feeding its dedicated scanners/analyzers into `dross-secure` / `dross-quality`
- [x] Marker-file stack detection (`Dockerfile` / compose) so a Docker profile's tools run on repos with no source extension

### Milestone v0.3 — conversational command UX across the dross loop (complete)

- [x] Interaction contract — a single `dross-interaction-contract` rule ("propose-and-react, one decision per turn") plus a reusable prompt snippet at `assets/prompts/_interaction.md`, delivered to each command verbatim via `dross interaction show`; documented as a first-class behaviour in the README
- [x] Core-loop retrofit — `/dross-spec`, `/dross-plan`, `/dross-execute`, `/dross-verify` surface decisions one at a time with a proposed default, never batching unrelated questions or dumping a composed artefact for blanket approval
- [x] Setup-command retrofit — `/dross-init`, `/dross-onboard`, `/dross-options`, `/dross-milestone`, `/dross-quick`, `/dross-inbox`, `/dross-rule` rewritten to the same propose-and-react choreography
- [x] Audit + README — a grep-verifiable audit checklist maps every interactive command to its decision points and confirms the pattern; the interaction model is documented in the README

### Milestone v0.4 — phase identity & lifecycle (complete)

- [x] Stable slug phase ids — phase identity is now the bare slug (`phases/auth`), not an `NN-slug` ordinal. Ordering moved to the milestone's `phases` array, so reordering no longer renames directories or rewrites ids. `dross phase migrate` converts legacy `NN-slug` phases in place, with back-compat resolution for old ids
- [x] Phase-lifecycle commands — `dross phase {insert,move,rename}` (plus `number`) edit a milestone's phase array and on-disk identity as first-class operations: `insert` scaffolds a phase at a position, `move` reorders within the array, `rename` retargets the directory + spec id + milestone entry + deferred targets + branch, all guarded against shipped-phase collisions

### Milestone v0.5 — spec & backlog flow (complete)

- [x] Deferred-item routing — deferred ideas carry a `target` (a phase slug they should re-surface in) or stay "someday"; `dross deferred` lists and routes them, and `dross validate` refuses a dangling target
- [x] Deferred-triage gaps — a `dismissed` state retires an item as wontfix/done (distinct from "someday" and "routed"), `dross deferred unroute`/`dismiss` complete the lifecycle, and the spec gray-area walkthrough routes scope-creep into deferred items instead of losing it

### Milestone v0.6 — surface depth & loop hardening (complete)

- [x] Multi-language analyzer catalogs + deepened container/IaC scanning — per-stack scanner/analyzer loadouts feeding `dross-secure` / `dross-quality`, with Dockerfile/compose/IaC surfaces (hadolint/trivy/checkov-class tools) wired in
- [x] Secure/quality findings lifecycle — audit findings persist with a state machine (open → fixed/accepted/false-positive) across re-runs instead of re-surfacing every pass
- [x] Ship/complete recovery hardening — `dross ship recover` + `dross phase complete --recover` heal the diverged-main and dirty-tree failure states with one command instead of manual `.dross/` surgery
- [x] ARCHITECTURE.md enhancements + comprehension layer — richer feature entries and a scan-driven backfill/refresh (`dross architecture check` catches symbol-link drift)
- [x] GitLab ship provider + YouTrack board backend — a third PR provider and a third issue-board backend alongside GitHub and Forgejo/Gitea
- [x] Self-update & distribution — `dross update` fetches, verifies, and atomically swaps the running binary, then re-syncs commands/prompts
- [x] Gray-area walkthrough — the spec locked-decisions step deep-dives phase-specific gray areas one at a time into `[[decisions]]` / `[[deferred]]`

### Milestone v0.7 — branch topology & non-interactive ship (complete)

- [x] `dross ship --auto` — a non-interactive fast-path (skips body preview / reviewers / merge gate) suitable for scripts and loops; `--json` emits `{url, number, result}`
- [x] Milestone-branch model — phase PRs squash-merge into `milestone/<version>` when a milestone is active; the milestone itself lands in `main` as a merge commit (not a squash) so `main` keeps per-phase history, finalized by `dross milestone complete`
- [x] Interaction include-first framing — an in-scope candidate is simply included; a borderline one is an either/or that leads with "add to current phase"; only one with a clear home elsewhere leads with "defer it". Standardized across spec/plan (replaced the original defer-first lead)

### Milestone v0.8 — Claude Code surface integration (complete)

- [x] Native statusline — `dross statusline` renders the Claude Code status line from status JSON on stdin (project, milestone, phase, progress), installed via `dross install`

### Milestone v0.9 — backlog burn-down: trust, findings & integrations (complete)

- [x] Release trust & distribution — minisign-signed releases with verify-before-swap in `dross update` (public key embedded; signing key + password in CI secrets); Homebrew + Windows distribution paths
- [x] Ship-time ARCHITECTURE.md autogen — `/dross-ship` self-heals an absent doc and folds each phase's landmarks into the matching feature entry in place
- [x] `/dross-watch` — a read-only heartbeat: board inbound + phase-drift digest since the last tick, ending with one suggested next command (`watch-pr-ci-status` extends it to PR/CI state)
- [x] Additional board backends + verify-merge-before-completion + PR-record-reaches-base — more issue-board providers, plus ship/verify ordering fixes so a phase can't complete before its merge lands and the PR record reaches the base branch

### Milestone v0.10 — workflow ergonomics: task lifecycle & telemetry clarity (complete)

- [x] Task-lifecycle commands — `dross task {add,remove,edit}` edit `plan.toml` through guarded verbs: `add` appends or inserts (`--after`/`--before`) with a fresh per-plan high-water id (a freed id is never reissued), `remove` is dependency-safe with a `--force` strip, `edit` is a partial field update, and every mutation is gated by a pre-write integrity guard that leaves the file byte-unchanged on rejection
- [x] Task reordering, landmark comma-fix, telemetry bucket graduation + taxonomy overhaul, ship clean-tree, verify auto-finalize

### Milestone v1.0 — hardening: every claim proven (complete)

- [x] Self-audit pass — each documented guarantee re-checked against the code that is supposed to enforce it, with the gaps closed rather than noted
- [x] Board sync proven against a live Jira Cloud instance rather than fixtures alone
- [x] README truth pass — claims the tool does not honour removed instead of softened

### Milestone v1.1 — friction pass: the logs stop repeating themselves (complete)

- [x] Recurring friction surfaced by the telemetry log turned into fixes, so the same error stops being re-diagnosed by hand every session

### Milestone v1.2 — branch-model truth: the base is a fact, not a guess (complete)

- [x] A phase's base branch is recorded at fork time and read back verbatim, never re-derived from git topology — the divergence that previously needed manual recovery commits cannot recur

### Milestone v1.3 — finish the tool: the standing backlog, drained (complete)

- [x] Mutation scoping to the phase's own diff, plus a survivor lifecycle where every survivor is in-diff, routed, or accepted-with-reason and an accepted one is never re-emitted
- [x] Milestone lifecycle closes itself — `dross milestone complete --finalize` owns the status flip and is idempotent
- [x] Honest board sync, honest config: a board failure surfaces instead of counting as zero, and every documented config value either changes behaviour or is rejected
- [x] Off-box mutation runs on a consented remote host, with per-run scratch build caches wiped on every exit path
- [x] Board mirroring at task granularity, driving the tracker's own workflow field

### Milestone v1.4 — the backlog burn: every parked idea, routed or killed (complete)

- [x] Test-suite hermeticity enforced, not just documented — a test reading the real repo's gitignored `.dross` fails locally instead of only in CI
- [x] `dross task add/edit --test-contract` and `dross phase create --adopt` — nothing dross supports needs hand-editing an artefact
- [x] Every `dross issue pull --json` failure reaches the envelope, setup failures included, instead of crashing a consumer
- [x] Per-leg mutation scores persisted in `verify.toml`, so a small leg scoring badly is no longer invisible inside a large leg's good pooled number
- [x] Upgrade note for the `state.json` checkout refusal, and a roadmap guard that fails when a milestone's recorded status and the README disagree
- [x] `dross run <name>` executes every configured runtime command, and plan review runs in effort tiers
- [x] The TypeScript mutation leg runs on every PR in CI, on a pinned toolchain no developer installs by hand
- [x] Board→dross inbound mirroring — dragging a task's card moves that task in `plan.toml`, and a task edited on both sides is refused with both values named
- [x] Several authorized mutation hosts with first-ready-wins selection, and a run that refuses a full scratch volume instead of quietly filling one
- [x] Five parked ideas dismissed on measurement rather than deferred again — scanner scratch caches, gremlins workdir pinning, remote runtime installation, remote-run latency, remote test-watch

### Milestone v1.5 — signal truth: one answer to "is this done", and a board that agrees with it (complete)

- [x] One doneness reader behind `dross status`, `dross milestone progress` and `dross phase list` — the completion record, never a verify verdict
- [x] Phases finished before `changes.json` carried a status marked done through a supported verb, not a hand-authored file
- [x] `dross issue pull` returns only issues dross did not author, and a shipped phase's task cards leave task-in-review by themselves
- [x] `/dross-plan` walks every unresolved gray area one turn each, with no multiSelect pre-selection

### Milestone v1.6 — lane truth: the test gate runs what the task actually touched (complete)

- [x] A task's pre-commit gate runs only the `[[runtime.test_lane]]` whose `match` globs its files, names the lane and command it ran, and never invokes the other lane's runner
- [x] A lane whose toolchain is absent on the granted remote runs locally, says so every time — naming the toolchain and host — and offers to install it there
- [x] Task paths are translated into each lane runner's selector form, and a selector matching nothing is reported as a miss rather than a failing gate
- [x] A lane may declare a `prepare` command between sync and test, so remote build prerequisites are dross's job rather than hidden in each repo's test command

### Milestone v1.7 — symmetric controls: every path to a dangerous operation goes through the gate (active)

- [ ] Every command that spawns the repository's own code goes through the exec-consent gate, proven by a test that enumerates spawn sites rather than a hand-maintained list
- [ ] A path loaded from a tracked artifact is containment-checked before any file I/O, on the read path as well as the write path, through one shared implementation
- [ ] No raw subprocess output is persisted to `verify.toml` or rendered into a PR body — a failed leg records its exit status and a pointer, and the live diagnostic stays on stderr
- [ ] A failed push is retried on the next run because its gate compares local against origin rather than against the local index
- [ ] Running dross's own scans against dross reports zero self-referential findings — no testdata languages, no identity hashes read as secrets, no scanner flagging its own marker definitions
- [ ] No reachable stdlib vulnerability ships, and the release build measures the dependency graph it actually ships
- [ ] Mutation ranges are AST-aware: a range covers the enclosing syntactic construct, so no mutant covering a changed line goes ungenerated
- [ ] No persisted artifact or published PR body carries recognisable secret material — captured output is not persisted, and what is persisted is scanned
- [ ] `internal/cmd` holds cobra wiring only; every domain it carries today lives in a package of its own, provable by import direction
- [x] Review-comment ingest — `/dross-respond <pr>` triages a phase PR's review comments across all four providers: `dross pr comments` lists them fenced as untrusted data, `dross pr resolve` records one evidenced verdict each (accept → task, reject → reason, route → deferred) in a tracked `pr-triage.toml` that never stores a comment body, `dross pr reply` posts the rejections once on a recorded human approval, `dross ship` refuses while an accepted comment's task is undone, and `/dross-watch` shows each ship PR's untriaged count
- [x] Debug sessions — `/dross-debug` keeps a systematic investigation in a gitignored `.dross/debug/<slug>.md` (one hypothesis per probe, evidence before the next, hard stop after three failed fixes, two independent signals to close), `dross debug {new,list,close}` gates it, and `dross status` plus the SessionStart line name any open session

### Milestone v1.8 — Azure DevOps provider: ship and board sync reach a fifth forge (active)

- [ ] Azure DevOps works as a ship/PR provider alongside GitHub, GitLab, Forgejo/Gitea and Bitbucket

### Milestone v1.9 — workflow depth: ideas ported from superpowers, GSD and gsd-core (planning)

- [ ] Success criteria not yet written — scope with `/dross-milestone v1.9`

## Telemetry

Dross records local-only usage events at `~/.claude/dross/telemetry.jsonl`. The intent is single-developer self-observation — a dogfood log you can read back later to find where the tool gets in your way.

**What's recorded.** One JSONL event per `dross` invocation (command path, duration, exit code, error class) plus outcome events from `verify` (mechanical run emits `verdict=pending` plus `mutation_status` tag; `dross verify finalize <phase>` later emits the resolved `pass | partial | fail` plus mutation score), `ship` (provider, result, force-flag use), `phase create` (ordinal), and `doctor` (result = `passed` | `issues_found`, issue count). All events carry a 12-character SHA-256 hash of the absolute repo path so per-project trends are visible without exposing the path itself.

**Error buckets.** When a CLI invocation exits non-zero, the error is classified into one of: `incomplete_root`, `no_root`, `no_phase`, `no_spec`, `no_plan`, `no_milestone`, `dirty_tree`, `merge_pending`, `wrong_branch`, `verify_state`, `mutation`, `provider`, `board`, `env_token`, `unknown_subcommand`, `unknown_field`, `unknown_flag`, `arg_count`, `landmark_parse`, `cli_args`, `cancelled`, `check_issues`, `state_io`, `config_io`, `already_exists`, `invalid`, `missing`, `permission`, `git`, `network`, `other`. For most classified buckets the raw message is never recorded — the bucket already describes the failure. Two kinds of bucket are exceptions and carry a redacted, length-capped copy of the message in `err_detail`: the catch-all `other`, because the unclassified tail is otherwise undiagnosable (countable but opaque), and the identifier-only buckets `unknown_subcommand`, `unknown_field`, `unknown_flag`, `arg_count` and `landmark_parse`, whose messages are bounded CLI tokens you typed — the rejected subcommand, field path, flag, arg count or landmark pair — so the bucket shows *what* was wrong, not just that something was. The home directory is collapsed to `~` so absolute paths don't leak. As patterns surface in `other`, they graduate into named buckets; a graduated bucket only keeps its detail when the message is a bounded identifier.

Classification walks an ordered rule table first-match-wins, arranged in tiers: root/scaffold state (`incomplete_root`, `no_root`), then workflow-specific buckets (phase/plan/spec/verify/mutation/provider/board and friends), then the CLI surface (cobra's unknown-command/flag/arg shapes, landmark and field parses), then the generic safety-net buckets — where the more diagnostic bucket wins: `already_exists`, `permission`, `git`, `network`, then `invalid`/`missing` — with `other` as the fallback when nothing matches. The order is enforced in code: a rule that would make a later rule unreachable fails the build.

**What's NOT recorded.** Anything you typed. No criterion text, no decision text, no commit messages, no PR titles or bodies, no reviewer names, no file contents, no repo URLs. Counts and small enums only — plus the redacted `err_detail` on the allowlisted buckets above, which holds dross's own (path-redacted) error string, never your input.

**Privacy posture.** Local file. No network. No daemon. No third party. Default ON; `/dross-init` and `/dross-onboard` ask once and stamp `asked_at` so you're never re-prompted across projects.

**Toggles.**
```sh
dross stats opt-out       # disable; persisted in defaults.toml
dross stats opt-in        # re-enable; same file
DROSS_NO_TELEMETRY=1      # authoritative kill-switch (env var; overrides on-disk config)
dross stats path          # print the log file path
```

**Reading it back.**
```sh
dross stats               # default `show` — top commands, error buckets,
                          #   force-flag count, verify verdicts, ship results,
                          #   doctor runs
dross stats show --since 7d
dross stats show --since 2026-05-01
```

The schema is versioned and stable; the log is append-only and rotates at 10 MB. Safe to share in conversations or pastebins for ad-hoc analysis since it carries no project-identifying content.

## License

[AGPL-3.0](LICENSE).

## Acknowledgements

Dross is conceptually inspired by [GSD](https://github.com/gsd-build/get-shit-done) by TÂCHES (Lex Christopherson), distributed under the MIT License. No code or prompt text is copied; this is a clean Go reimplementation built around different design pivots (lean prompts, pair-mode execution, mutation testing as a first-class gate). If you want the full-featured, well-trodden tool, GSD is excellent — Dross is a fork of the *idea*, not the implementation.
