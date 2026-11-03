# /dross-review

Spawn four parallel subagents to review an open PR through different lenses, aggregate their findings, and post one consolidated comment to the PR.

The four lenses (intentionally narrow scopes — each subagent stays in its lane):

1. **security** — credential leaks, injection sinks, auth bypasses, OWASP top 10
2. **code quality** — readability, dead code, premature abstraction, error handling, naming
3. **test efficacy** — do tests actually exercise the change? (mutation-style reasoning, not coverage). Cross-reference `verify.toml` if present.
4. **spec fidelity** — does the diff deliver the criteria in `spec.toml`? Anything outside the spec? Anything missing?

**Surface the post decision as a conversation, not a broadcast.** Follow the shared interaction playbook (`_interaction.md`, printed by the `dross interaction show` pre-flight step below): the §5 post-or-skip gate is a single propose-and-react turn that leads with a default. The composed comment is outward-facing content about to be published, so it's shown in full before posting — that's the deliberate exception, not a contract violation.

## 0. Pre-flight

1. `dross rule show` and `dross interaction show` — treat the rules as MUST-FOLLOW (the builtin commit-hygiene rule applies if you write any `.dross/` files during this run) and follow the printed interaction playbook for the post gate.
2. Resolve the PR number from `$ARGUMENTS`. Required.
3. Resolve the phase id from `--phase <id>` or, if absent, from `state.json`'s `current_phase`. The phase context is essential — without it the spec-fidelity lens has no spec to compare against.
4. Read `.dross/phases/<phase-id>/spec.toml`, `plan.toml`, and `verify.toml` if present. Surface the criteria + any verify findings — these become inputs the lenses cite.
5. `dross doctor` — must exit 0 (the comment poster needs `[remote]` configured).

## 1. Fetch the PR diff

The subagents review a *diff*, not the live working tree. Get one cohesive diff to hand them:

- GitHub: `gh pr diff <pr-number>` — emits unified diff to stdout. Pipe to `.dross/phases/<phase-id>/review-diff.patch`.
- Forgejo / Gitea: `curl -s -H "Authorization: token $<auth_env>" "<api_base>/repos/<owner>/<repo>/pulls/<pr-number>.diff" > .dross/phases/<phase-id>/review-diff.patch`.
- Azure DevOps: build the diff locally from git — no API call and no credential. The base is the PR's target branch: `base` in `dross changes show <phase-id>`. Fetch both branches first, then take the three-dot diff (what the phase added since it left the base, the same diff the PR shows):
  ```
  git fetch origin <base> phase/<phase-id>
  git diff origin/<base>...origin/phase/<phase-id> > .dross/phases/<phase-id>/review-diff.patch
  ```

Confirm the file exists and is non-empty before continuing.

## 2. Spawn the panel — parallel

Use the `Task` tool to launch all four subagents **in a single tool block** so they run concurrently. Each gets:

- The diff path
- The phase artefacts (spec.toml, verify.toml)
- A tightly scoped prompt for its lens

### Subagent prompts (templates — fill the placeholders)

**security** — `subagent_type: general-purpose`
> Review the unified diff at `.dross/phases/<phase-id>/review-diff.patch` for security issues only. Look for: credentials in code or configs, SQL/command/HTML injection sinks, auth bypasses, missing authorization checks, secrets in logs, unsafe deserialization, SSRF, OWASP top 10 patterns. Stay in your lane: ignore code quality, test coverage, and spec fidelity — other reviewers handle those.
>
> Return a JSON-formatted finding list. Each finding: `{severity: "BLOCKING|FLAG|NOTE", file: "<path>", line: <int|null>, summary: "<one line>", evidence: "<verbatim snippet>"}`. If no findings, return `{"findings": []}`.

**code quality** — `subagent_type: general-purpose`
> Review the unified diff at `.dross/phases/<phase-id>/review-diff.patch` for code-quality issues only. Look for: dead code, premature abstraction, deeply nested logic, inconsistent error handling, misleading names, comments that disagree with the code, surprising defaults. Stay in your lane: don't comment on security, tests, or spec fidelity.
>
> Return findings in the same JSON shape as above.

**test efficacy** — `subagent_type: general-purpose`
> Review the unified diff at `.dross/phases/<phase-id>/review-diff.patch` for test efficacy. The phase's mutation-testing report is at `.dross/phases/<phase-id>/verify.toml` (if present); read it. For each non-test file in the diff, check whether the same diff includes test changes that would catch a regression to that code. If verify.toml shows surviving mutants on touched code, surface the file/line — that's a test the diff weakened. Stay in your lane: don't comment on security, code quality (other than test code itself), or spec fidelity.
>
> Return findings in the same JSON shape.

**spec fidelity** — `subagent_type: general-purpose`
> Read `.dross/phases/<phase-id>/spec.toml` and the diff at `.dross/phases/<phase-id>/review-diff.patch`. For each `[[criteria]]` entry in spec.toml: does the diff implement it? Is there code in the diff that doesn't map to any criterion? Is anything in `[[deferred]]` accidentally implemented? Locked decisions in `[[decisions]]`: is the diff consistent with them?
>
> Return findings in the same JSON shape, with severity reflecting: **BLOCKING** for missing criteria or violated locked decisions, **FLAG** for scope creep, **NOTE** for minor drift.

## 3. Aggregate

When all four subagents return, collect their findings into one structured set:

```
SECURITY (N findings)
  BLOCKING: <count>
    - <file>:<line> — <summary>
  FLAG: <count>
    - ...
CODE QUALITY (N findings)
  ...
TEST EFFICACY (N findings)
  ...
SPEC FIDELITY (N findings)
  ...
```

Skip lenses with zero findings rather than printing an empty block.

## 4. Compose the comment

Format the aggregated findings as markdown. Lead with a one-line summary, then one section per lens that returned findings. Keep evidence snippets short — link out to the diff line via the PR's `/files` view if the provider supports it (`<pr-url>/files#diff-<sha>L<line>`).

Comment template:

```markdown
## /dross-review — phase <phase-id>

<one-line summary: "N blocking, M flags, K notes across 4 lenses">

### Security (X findings)
- **BLOCKING** — `path/to/file.go:42` — Token logged in plaintext
  ```snippet```
- **FLAG** — ...

### Code quality (Y findings)
...

### Test efficacy (Z findings)
...

### Spec fidelity (W findings)
...

---
*Posted by `/dross-review`. Lenses are independent; agreement across lenses is intentional. Disagreement between lenses is a useful signal.*
```

Save the composed comment to `.dross/phases/<phase-id>/review-comment.md` for posterity.

## 5. Post

Drive a single propose-and-react turn (`AskUserQuestion`), leading with the default: **"Post the panel's findings as a comment on PR #<n>?"** — `post` (the recommended default once the comment is shown) / `skip`. One decision; don't bundle anything else into this turn.

If `post`:
```
dross ship comment --pr <n> --body-file .dross/phases/<phase-id>/review-comment.md
```

If `skip`: just print the comment to stdout so the user can copy it manually.

## 6. Wrap

Per the builtin commit-hygiene rule, commit the artefacts you wrote:
```
git add .dross/phases/<phase-id>/review-diff.patch .dross/phases/<phase-id>/review-comment.md
git commit -m "chore(dross): record /dross-review for <phase-id> on PR #<n>"
```

Print:

```
Reviewed PR #<n> — <phase-id>
  Subagents: security, code quality, test efficacy, spec fidelity
  Findings:  <total> (B blocking, F flags, N notes)
  Comment:   posted | saved to .dross/phases/<phase-id>/review-comment.md

Next: /dross-status — or address blocking findings, then /dross-ship once the PR is clean.
```

## Hard rules

- **Follow the interaction playbook (`_interaction.md`).** The §5 post-or-skip gate is a single propose-and-react turn leading with a default; the composed comment is shown in full before posting as the deliberate outward-facing exception.
- **Spawn all four subagents in one tool block.** Sequential spawns triple the wall-clock cost without changing the analysis.
- **Each lens stays in its lane.** Cross-lens findings are noise; the review value comes from independent perspectives. If a lens proposes a finding outside its scope, drop it.
- **Don't auto-fix.** Findings are advisory. The phase author decides what to act on.
- **Don't post if `dross doctor` failed pre-flight.** Without `[remote]` configured, the comment can't be posted; surface the doctor failure instead.
- **One comment per review run.** Multiple comments fragment the review; if the user wants a re-review after fixes, run `/dross-review` again, which produces a new aggregated comment.
