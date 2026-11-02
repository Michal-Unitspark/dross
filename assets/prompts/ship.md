# /dross-ship

Open a PR for a verified phase. Pushes the `phase/<id>` branch to the provider (GitHub or Forgejo), opens the PR, and requests human reviewers. The provider's squash-merge collapses per-task commits on merge.

**Run this as a conversation, not a broadcast.** Follow the shared interaction playbook (`_interaction.md`, printed by the `dross interaction show` pre-flight step below): surface one decision per turn — the §2 body override, §3 reviewers, and §6 merge gate are each their own `AskUserQuestion`, never bundled. The §1 PR-body preview is the deliberate exception to the no-dump rule: it's outward-facing content about to be published, so the user sees it in full before authorizing the post.

## 0. Pre-flight

1. `dross rule show` and `dross interaction show` — treat the rules as MUST-FOLLOW and follow the printed interaction playbook for every turn of this command.
2. `dross status` — confirm a current phase exists; resolve its id (or accept user's `<phase-id>` argument).
3. `dross doctor` — must exit 0. Surfaces:
   - missing `[remote]` config
   - mismatched git origin vs `[remote].url`
   - unset `$auth_env`
   - missing `.dross/** linguist-generated=true` in `.gitattributes` (so review UIs collapse planning artefacts)
   - phase commits leaked onto local main (legacy state — heal with `dross ship recover` first)
   If anything's off, stop and have the user fix before continuing.
4. Read `.dross/phases/<phase-id>/verify.toml` — `[verify].verdict` must be `pass`. If not, stop. A pass is also refused when it is **stale**: `dross ship` compares the tree the run measured (`[verify].measured_tree`) with the work tree, and a file outside `.dross/` and `ARCHITECTURE.md` that changed, appeared or went away since the run means the verdict no longer covers what would ship — it names those files; re-run `/dross-verify <phase-id>`. A verdict recorded before trees were kept ships with a freshness-unknown warning. Override either refusal only if the user explicitly accepts the risk: `dross ship --force-unverified`.
5. **Verify HEAD is on `phase/<id>`** with `git symbolic-ref --short HEAD`. `dross ship` requires this — the phase branch is what gets pushed. If not on it: `dross phase checkout <phase-id>` (it should exist from `dross phase create`). Use the dross verb, never raw git — it switches through the guard that refuses a branch whose tracked `.dross/state.json` would overwrite your live machine-local one.
6. `git status --porcelain` — must be empty. If dirty, ask user to commit or stash first.

## 0.5 Non-interactive fast-path (`--auto`)

If `--auto` is in this command's arguments, run to completion non-interactively — suitable to call from a script or loop. Skip every interactive turn below:

- **§1 body-preview dump** — skipped; nothing is shown for approval.
- **§2 body override** — skipped; no AskUserQuestion, the generated body is used.
- **§3 reviewers** — skipped; no AskUserQuestion, zero reviewers are requested for this run (the configured `remote.reviewers` default is left untouched).
- **§3.5 landmark merge + ARCHITECTURE.md auto-backfill** — both skipped under `--auto` (they show a diff / drive LLM generation and ask for an OK, which a non-interactive run can't answer); fold landmarks in — and self-heal an absent doc — later via an interactive ship or `/dross-architecture`.

§0 pre-flight still runs — the verify-pass gate is **not** bypassed (add `--force-unverified` too only if you must ship unverified). Then shell out directly:

```
dross ship --auto <phase-id>
```

Add `--json` for machine-readable output to capture in the caller: `dross ship --auto --json <phase-id>` prints a single `{url, number, result}` object on stdout and suppresses the narration.

`--auto` opens the PR and **returns without merging** — do not drive the §5 CI-watch or §6 merge gate; the caller (or a later interactive `/dross-ship`) owns the merge. Report the PR URL and stop.

Everything below (§1–§7) is the interactive path, taken when `--auto` is absent.

## 1. Preview

Run `dross ship --no-push <phase-id>` for a dry run (no push, no PR). To preview the review diff: `git diff <main>..phase/<id>`. To preview the PR body: `dross ship --print-body <phase-id>`.

Show the user — the PR body is outward-facing content about to be published, so show it **in full** here (the deliberate `ship_body_preview` exception to the no-dump rule):
- the resolved title (`phase <id>: <spec title>` by default)
- the resolved body in full (`dross ship --print-body <phase-id>` — prints the generated markdown without building or pushing)
- the reviewers list (`dross project get remote.reviewers`)

## 2. Body override (optional)

Ask the user (`AskUserQuestion`): **Use generated body, or write your own?**

If override:
1. Drop a starter file at `.dross/phases/<phase-id>/pr-body.md` containing the generated body (the user edits it).
2. Wait for the user to confirm they're done editing.
3. Pass `--body-file .dross/phases/<phase-id>/pr-body.md` to `dross ship`.

## 3. Reviewers

Read the configured list (`dross project get remote.reviewers`) and drive a single propose-and-react turn rather than silently writing config. `AskUserQuestion`: **"Request reviewers `<alice,bob>`?"** — lead with `use these` (the configured default), offer `change` (user names the list) and, if the list is empty, `none for now`. Only on `change` run `dross project set remote.reviewers "alice,bob"` before shipping. (Per-PR overrides aren't supported in v1 — the project default applies, so this write updates the project default.)

## 3.5 Merge landmarks into ARCHITECTURE.md

Before pushing, fold this phase's landmarks into the architecture doc so the PR
carries an up-to-date `ARCHITECTURE.md` (c-6).

1. `dross changes show <phase-id>` prints JSON; each task record carries a typed
   `landmarks` array of `{feature, symbol, loc, what}` objects (written by execute
   §1f). Read those **structured fields** directly — do not parse a `notes` string
   for the landmark (the free-form `notes` field is no longer the landmark
   carrier).
2. **Self-heal an absent doc.** If `ARCHITECTURE.md` is absent at repo root, the
   repo predates the doc — **automatically backfill it now**, don't punt to a
   manual run: read `~/.claude/dross/prompts/architecture.md` and run that backfill
   generation (scan the code + git history, emit the feature-organized doc from the
   skeleton/`EntryTemplate`) so this ship self-heals the repo, then continue to the
   landmark merge below. There is no Go backfill engine — the prose is generated by
   this prompt-driven flow. **Non-blocking:** if the backfill can't run or fails,
   skip it with a one-line note and continue — a missing doc must never block the
   ship. (Under `--auto` this whole §3.5 — backfill included — is skipped per §0.5,
   since a non-interactive run can't drive the generation.)
3. For each landmark, merge it into the entry whose **feature** matches, updating
   that entry **in place**:
   - Refresh the one-line description and symbol-link bullets to reflect the new
     work. Keep the fixed entry template (heading + one-line + symbol bullets +
     provenance) — no prose paragraphs.
   - Update the provenance breadcrumb: append `· extended <phase-id>` (unless this
     phase is already credited) plus the representative commit short-sha.
   - Create a **new** entry only when no existing feature matches. **Never** add a
     per-phase heading or a "Phase NN" section — the doc is organized by feature,
     never by phase. A duplicate per-phase heading appearing means the merge
     regressed.
4. Show the `git diff` of `ARCHITECTURE.md`. On the user's OK, run the full
   suite on the edited tree — a bare `dross test`, no selector and no `--files`
   — before committing. The commit gate admits a commit only when a full green
   was recorded for exactly the tree being committed, and the doc edit changed
   that tree since verify ran:
   ```
   dross test
   ```
   On green, commit it onto `phase/<id>` (it lives at repo root, so the provider
   squash-merge carries it into the PR):
   ```
   git add ARCHITECTURE.md
   git commit -m "docs(<phase-slug>): merge phase landmarks into ARCHITECTURE.md"
   ```
   Match the repo's existing trailer convention. The tree is clean again
   afterwards, so §4's clean-tree re-check passes. A gate refusal is a stop:
   read its rule and remedy, and never route around one.

## 4. Ship

Run `dross ship <phase-id>`, optionally with `--draft` and/or `--body-file`.

The CLI:
1. Re-checks that HEAD is on `phase/<id>`, then the verify gate — a pass whose measured tree no longer matches the work tree is refused as stale, naming the changed files
2. Sends `.dross` chores left on the base (a pause snapshot, a gate auto-commit) to origin: pushed straight to it when origin takes direct pushes, or — when the base is protected (`dross protect --check <base>` reads `protected`) — through a **chore PR** from `dross-chores/<base>`, armed to auto-merge as a merge commit. It prints the chore PR's URL; if auto-merge is unavailable on the repo it says so, and that PR is merged by hand. An `unknown` answer pushes nothing and names the fix.
3. Gates `phase/<id>` on origin — fetches and compares; a branch that is ahead or new is pushed (`-u`), a level one is left alone, a behind-only one refuses naming `git pull --rebase origin phase/<id>`, a diverged one refuses naming the pull or `dross ship --force`
4. Opens the PR via the provider API — skipped when the phase's record (or the provider, when the record carries no number) already has an open PR for `phase/<id>`
5. Requests reviewers
6. Commits the PR record (`chore(dross): record PR #N for <id>`) and pushes it through the same origin gate
7. **Only then** marks the phase shipped — `state.json` and the record's status flip together, and the marker commit is pushed. Until that push lands the phase still reads verified, and `dross status` names the retry.

## 5. CI gate

After the PR opens, watch CI to completion. On `azure-devops` and `bitbucket` never skip this section — `dross ship ci` reads their CI whatever the repo holds: an `azure-devops` repo with no `azure-pipelines.yml` still runs classic pipelines and branch-policy builds, so it is still watched. On every other provider, skip this section ONLY if the repo has no `.github/workflows/`, `.forgejo/workflows/`, `.gitea/workflows/`, `.gitlab-ci.yml`, AND the provider reports no checks for the head SHA.

**Watch checks:**
- Azure DevOps / Bitbucket: poll `dross ship ci <phase-id>` every ~30s. Each run reads CI once for the PR `changes.json` records, at the watched head — the commit `origin/phase/<id>` names — prints one `ci:` line, and exits with the verdict:
  - exit `0` = pass → go to §6.
  - exit `2` = fail → **On failure**; the `ci: fail` line carries the failing check's link.
  - exit `3` = pending → keep polling while it reads `pending (running)`. A `pending (queued)`, or a pending that carries a reason (`ci: pending - <reason>` — the PR does not carry the watched head yet), still there at the end of a ~10-minute window → ask the user to proceed or stop, with that reason shown.
  - exit `4` = none — nothing has registered yet: keep polling through the ~10-minute window; still none at its end → ask the user to proceed without CI or stop.
  - exit `5` = ask → ask the user to proceed or stop at once, with the line's reason shown.
  - exit `1` = error — the read itself failed, so there is no verdict: surface the error and ask whether to retry or stop.

  The window starts at the first poll after each push. None and ask are never a pass — only exit `0` is. On every way out to §6, note the watched head (`git rev-parse origin/phase/<id>` at the last poll). When the user chooses proceed, remember it together with that head and the reading it was given on: §6 honours that proceed for the same head only.
- GitHub: never gate on `gh pr checks --watch`. It exits 0 as soon as the checks registered **so far** pass, and a fast third-party check (GitGuardian, say) can be the only one registered while the Actions run is still queued — it reads green with CI unrun. Watch the runs themselves, in this order. SHA = head of `phase/<id>` (`git rev-parse origin/phase/<id>`).
  1. Wait for the runs to register: poll `gh run list --commit <sha> --json databaseId,workflowName,status` every ~15s until it lists at least one run, for up to ~10 minutes. A repo with workflows whose run never registers in that window joins the no-checks path below — surface it, never read it as a pass.
  2. Watch each run to completion: `gh run watch <run-id> --exit-status` — non-zero on failure.
  3. Once every run has finished, read the full check set: `gh pr checks <pr-url>` (no `--watch`). Every check must pass, third-party ones included; one still pending → re-read after ~30s; any failure → **On failure**.
- Forgejo / Gitea: poll every ~30s — `GET <api_base>/repos/<owner>/<repo>/commits/<sha>/status` (auth header `token $<auth_env>`). Stop when `state` ∈ `success | failure | error`. SHA = head of `phase/<id>`.
- GitLab: poll every ~30s — `GET <api_base>/projects/<id>/pipelines?sha=<sha>` (auth header `PRIVATE-TOKEN: $<auth_env>`, or `Authorization: Bearer` when `remote.auth_scheme = bearer`); `<id>` is the URL-encoded `owner/repo` (or numeric `remote.project_id`). Read the latest pipeline's `status` and apply the locked mapping: `success` → pass (go to §6); `failed` or `canceled` → fail (drop to **On failure**); `running` / `pending` / `created` / `preparing` → keep polling; `manual` / `skipped` → **do not guess** — surface to the user and ask whether to proceed without a green pipeline. SHA = head of `phase/<id>`.

If the provider reports no checks were registered — GitHub/Forgejo report none, **or** GitLab returns an empty pipelines array (no pipeline for the SHA) — surface that to the user and ask whether to proceed without CI or stop.

**On failure:**
1. Pull failing logs:
   - GitHub: `gh run view --log-failed` for the run linked from the PR.
   - Forgejo: log URL is in the commit status payload (`target_url`); `WebFetch` it.
   - GitLab: the failed job's `web_url` is in the pipeline's jobs (`GET <api_base>/projects/<id>/pipelines/<pipeline-id>/jobs`); `WebFetch` the trace or surface the job URL.
   - Azure DevOps / Bitbucket: the `ci: fail <link>` line names the failing build or status; surface that link (`WebFetch` it when it is reachable without the forge's sign-in).
2. Diagnose. Edit + commit the fix on `phase/<id>`, one commit per logical fix following `repo.commit_convention`.
3. `git push origin phase/<id>` — appends to the open PR, so CI re-runs on the fix.
4. Then re-run **`dross ship <phase-id>`** — required after every fix, not optional: it recognises the open PR, pushes anything still pending on `phase/<id>`, reports the existing PR by number (the URL is the one from the first run or the provider page; `changes.json` stores no url), and re-checks the verdict's freshness. A fix to any file outside `.dross/` and `ARCHITECTURE.md` makes the pass stale and ship refuses, naming the changed files: run `/dross-verify <phase-id>`, commit its record, re-run `dross ship <phase-id>`, then continue. If ship refuses a diverged branch, `dross ship --force` is the path (it force-with-leases); if it refuses a behind-only one, `git pull --rebase origin phase/<id>` first. If you rebase or amend by hand, use `git push --force-with-lease`.
5. Loop back to "Watch checks". Cap at 3 fix iterations — if checks still fail after 3 cycles, stop and hand back to the user.

**On pass:** continue to §6.

## 6. Merge gate

**Re-check freshness first:** re-run `dross ship <phase-id>` before asking to merge. A fix pushed after the PR opened is in the merge, and a pass that predates it does not cover it. If ship refuses the pass as stale, do not merge: run `/dross-verify <phase-id>`, commit its record, re-run `dross ship <phase-id>`, and go back to §5's Watch checks.

`AskUserQuestion`: **"All checks passed on PR #N. Merge now?"** — options: `merge` / `hold`.

If `hold`: skip to §7 with status `awaiting-merge`.

If `merge`:

1. **Squash-merge via provider — merge only, no branch teardown:**
   - GitHub: `gh pr merge <pr-url> --squash`
   - Forgejo / Gitea: `POST <api_base>/repos/<owner>/<repo>/pulls/<n>/merge` body `{"Do":"squash"}`
   - GitLab: `PUT <api_base>/projects/<id>/merge_requests/<iid>/merge` body `{"squash":true}` (auth header as in §5) — the squash collapses per-task commits.
   - Azure DevOps / Bitbucket: a fresh CI read, then `dross ship merge`.
     1. Run `dross ship ci <phase-id>` immediately before the merge, and compare `git rev-parse origin/phase/<id>` with the head §5 noted.
        - A head that moved since §5 → no merge, whatever the verdict: back to §5's Watch checks for the new head.
        - exit `0` = pass → merge.
        - exit `4` none or exit `5` ask → merge only if the user chose proceed in §5 for this same head; otherwise ask again — proceed merges, hold skips to §7 with status `awaiting-merge`.
        - exit `3` pending → merge only if §5's proceed for this same head was given on a stuck pending (queued, or carrying a reason); a `pending (running)` never merges — back to §5's Watch checks.
        - exit `2` fail or exit `1` → no merge: back to §5 (**On failure** for a fail, Watch checks otherwise).
     2. Run `dross ship merge <phase-id>` with an explicit Bash `timeout: 300000` (5 minutes — above the 2-minute default, since it waits until the forge reports the PR merged). It squash-merges at the watched head, keeps `phase/<id>` on origin, and never bypasses a branch policy.
        - exit `0` = merged — it prints the PR URL; continue with step 2 below.
        - exit `2` = blocked by a branch policy (required reviewers, comment resolution, linked work items) → not merged: skip to §7 with status `awaiting-merge`, naming the policy.
        - any other non-zero → surface the error and stop. A merge that did not land is never read as merged: after a non-zero `dross ship merge`, never run `dross phase complete`.

   **`dross phase complete` performs the local AND remote `phase/<id>` deletion, on every provider.** Never ask the provider to delete the branch as part of the merge — the merge call above is the whole step; no delete flag, no remove-source-branch field, no branch-removal API call. GitHub's delete-on-merge flag does its own raw checkout of the base branch, a branch switch outside dross's guard, and that is what destroyed a live `state.json` on a previous ship. Forgejo and GitLab merge over REST and never switch branches, so dropping provider-side teardown everywhere gives dross one teardown owner instead of a per-provider split.
2. **Mark the board issue shipped** (it exits 0 and does nothing when `[board].enabled` is false; a non-zero exit is a board failure — surface it, never read it as the disabled no-op): `dross issue phase sync <phase-id> --status shipped`. The PR is merged but the phase isn't finalized yet, so `shipped` is the honest state here — and it's the moment the board should stop showing the phase as in-progress.
3. **Finalize locally and on the board**: `dross phase complete <phase-id>` — switches to the base this phase was forked from (read from the phase's own record, never inferred), fast-forwards it from origin, writes the completion record into the machine-local `.dross/state.json`, then deletes `phase/<id>` locally and on origin. It commits nothing for the completion: `state.json` is gitignored, so the record rides no commit anywhere. Last, it finalizes the board (same `[board].enabled` rule — no board call when it is false): every task card moves to `task-complete` and the phase card to `complete`, all closed, and a card an earlier run never created is created closed. The `board.json` links that writes are committed and published like any other `.dross` chore. A board that cannot be reached never undoes the completion — complete exits non-zero naming `dross issue phase finalize <phase-id>`, which is a board failure to surface (see Recovery), not the disabled no-op.
4. **Reconcile the milestone backlog** (same board rule: silent only when `[board].enabled` is false, a non-zero exit is a failure to surface): `dross issue backlog sync --phase <phase-id>`. It resolves the version from the phase's own `spec.toml`, pushes the live backlog, then closes the mirrors whose artefact has resolved — a `slug:` whose phase is now scaffolded, a dismissed idea, a routed item with a disposition record (its survivor accepted or measured away by the destination's verify run, or a complete destination's criterion that absorbed it). A routed item is NOT closed because its target phase shipped: a finished destination does not show the item was done (it may have been routed after the ship, or rescoped out). A mirror it cannot show resolved is named on stderr and left open, never closed on a guess. Exits 0 doing nothing for a phase outside any milestone.

> **Two merge levels (v0.7 branch topology).** Phase PRs **squash-merge** into
> their base — `milestone/<version>` when a milestone is active, else `main` —
> collapsing per-task commits into one per phase. The milestone itself lands
> separately via `dross milestone complete`, which opens **one integration PR
> against the branch that milestone was cut from** — the recorded parent
> `milestone/<version>` while that parent is still unmerged, `main` once it has
> merged or is gone (v1.2 `milestone-stacking`; read the command's output rather
> than assuming `main`). That integration PR must be merged as a **merge commit,
> not a squash**, so the target keeps the per-phase history — do this even if
> `repo.squash_merge` is set. After it merges, run `dross milestone complete
> <version> --finalize`, which fast-forwards `main` only when the merge actually
> landed there, and deletes the milestone branch — refusing while an unmerged
> stacked milestone or an open PR still depends on it.

## 7. Wrap

Print:

```
Shipped 01-meal-tagging
PR:  https://forge.example/me/proj/pulls/42
Reviewers: alice, bob
CI:  passed
Status: merged | awaiting-merge

Next: /dross-status — see where things stand.
      ↳ /dross-spec --new "<title>" — start the next phase.

state is on disk — safe to /clear · fresh session: /dross-status
```

If the PR opened but reviewer-request failed, surface that — it's non-fatal but the user should know.

## Recovery

Most ships finish clean: §6's squash-merge plus `dross phase complete` fast-forwards the phase's recorded base from origin, tears down the branch, and finalizes the board. When the merge step goes sideways, recover with a dross command — **never hand-edit `.dross/` or re-commit it by hand.** That manual surgery is exactly what drifted in the past; a dross command owns the restore and the commit. The mid-flight failure states and their one-command fixes:

1. **Fast-forward abort.** `dross phase complete` stops with a "fast-forward … failed" error — local main has diverged from origin/main (a stray commit on main, or a legacy completion chore). Fix: **`dross phase complete --recover`** — it resets main to origin and restores the cumulative `.dross/` tree in one shot, then finishes the completion. Pass `--recover` only after reading the abort: it is a destructive reset of local main.

2. **Diverged main (legacy / strip-filter era).** Local main carries phase commits that origin/main lost to an old `.dross/`-stripping squash, so main holds a parallel history. Fix: **`dross ship recover`** — the standalone healer. Same reset-and-restore as `--recover`, usable outside the merge loop.

3. **Dirty tree after push.** `dross ship` returned but `git status` is not clean — an older ship left its post-push `.dross/` write uncommitted, and `dross phase complete` refuses on a dirty tree. Fix: re-run **`dross ship`** — it is idempotent and commits its own post-push `.dross/` records, leaving a clean tree. You stage nothing yourself.

4. **Record push failed.** `dross ship` opened the PR but the push carrying its record was refused — the error names the retry. `dross status` reads the phase as verified, not shipped, and prints `pending: … re-run dross ship <id>`. Fix: run **`dross ship <phase-id>`** again — it recognises the open PR, pushes the record, then flips the phase to shipped. A refusal naming `git pull --rebase` or `--force` means origin's `phase/<id>` moved under you; do that first. (The mirror case — the record landed but the shipped-marker push failed — reports the phase *as* shipped and names the same re-run.)

5. **Chore PR pending** (protected base). `dross phase complete` refuses, naming a chore PR and saying to re-run once it merges: the base's `.dross` chores are still waiting in that PR while origin has moved on, so the base can't fast-forward yet. This is a wait, not a divergence — never `--recover` here. Once the chore PR merges (auto-merge lands it when the required checks pass; merge it by hand if ship said auto-merge is unavailable), re-run **`dross phase complete <phase-id>`**. On a protected base complete's own completion record goes out through a chore PR as well; it prints that URL, and nothing more is needed once it merges.

6. **Board not finalized.** `dross phase complete` exits non-zero saying the phase is complete but its board was not finalized — the board was unreachable, a token was missing, or a card refused its close. The completion stands; only the cards lag. Fix: once the board is reachable, run **`dross issue phase finalize <phase-id>`** from the base the phase merged into. It is the same finalizer complete runs, resumes from the links the failed run recorded, and only reads cards already at their terminal state, so re-running it is safe.

If you find yourself reaching for git plumbing against `.dross/`, stop — one of the commands above already covers it.

## Subagent review panel — DEFERRED

The original design called for a 4-lens subagent review panel (security, code quality, test efficacy, spec fidelity) posting findings as PR comments. v1 ships without this; track it as a separate task once `/dross-ship` has been validated against real Forgejo + GitHub PRs.
