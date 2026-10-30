# mvp draft: azure-devops-ship-loop

Lens: the smallest task set that satisfies every criterion. Every task traces to a criterion. Merges follow
shared code: one transport surface, one dispatch file, or one guard test per task.

```
Phase azure-devops-ship-loop — 7 tasks across 4 waves

Wave 1
  t-1  Wire the four deferred ADO PR reads
       files:    internal/ship/azuredevops_read.go (new), internal/ship/azuredevops_read_test.go (new),
                 internal/ship/prcomments.go, internal/ship/basepr.go, internal/ship/azuredevops_dispatch_test.go
       desc:     Add azure-devops arms to ListPRComments, PRHeadOf, AuthenticatedUser and OpenPRsTargeting, all
                 over adoRequest.
                 Comments: GET .../pullRequests/<n>/threads, every thread status. Drop system and isDeleted
                 comments and threads. An anchored threadContext (leading "/" stripped, right line else left
                 line) is inline; everything else is conversation. ID = <threadId><commentId as %05d>, all
                 digits, so prtriage's c|i<digits> id shape holds. Author = author.id + uniqueName, Bot unknown.
                 URL = <canonical>/pullrequest/<n>?discussionId=<thread>.
                 Head: GET pullRequests/<n>.
                 Identity: GET <api_base>/_apis/connectionData, with the version suffixed -preview.
                 Dependents: the active list filtered by targetRefName and re-checked client-side; a full page
                 is refused.
                 Retire TestAzureDeferredSurfacesFailClosed. Response structs stay function-local (no not_paths
                 rows).
       covers:   c-1, c-2, c-3, c-4
       depends:  —
       contract:
         - if a system or deleted comment leaks, TestADOPRComments fails. The fixture holds an active text
           thread, a fixed thread, a closed thread, a commentType=system vote thread, an isDeleted comment and an
           isDeleted thread. It must yield exactly the three text comments; resolved threads stay in, per the
           thread_listing lock
         - if inline anchoring breaks, TestADOPRComments fails:
             - threadContext {filePath "/src/a.go", rightFileStart.line 12} gives Kind inline, Path src/a.go,
               Line 12
             - a left-only anchor gives its leftFileStart line
             - a thread without threadContext gives Kind conversation with an empty Path
         - if ids collide or break prtriage's shape, TestADOPRCommentIDs fails: thread 1 comment 12 and
           thread 11 comment 2 give distinct ids, and every id matches ^[0-9]+$
         - if author or link drift, each comment fails to carry Author.ID = author.id,
           Login = author.uniqueName, Bot = unknown and
           URL https://dev.azure.com/o/p/_git/r/pullrequest/7?discussionId=<thread>
         - if a partial list escapes, a threads 500, a 203 sign-in page and an unparseable body each fail to
           return (nil, err)
         - if PRHeadOf misreads state, TestADOPRHead fails:
             - sourceRefName refs/heads/phase/x gives Ref phase/x
             - active gives Open true; completed and abandoned each give Open false
             - a set forkSource gives CrossRepo true
             - URL is the browser .../_git/r/pullrequest/<n>
         - if AuthenticatedUser asks the wrong place, TestADOAuthenticatedUser fails:
             - its one request is GET <api_base>/_apis/connectionData, with no project segment
             - the returned ID is authenticatedUser.id
             - that ID equals the author.id ListPRComments reports for a thread the same identity posted in the
               shared fixture (c-3's "own replies" equality)
         - if dependents are trusted blindly, TestADOOpenPRsTargeting fails:
             - the query carries searchCriteria.status=active and
               searchCriteria.targetRefName=refs/heads/milestone/v1
             - a mock that also returns a main-targeting PR and an abandoned PR yields only the matching active
               PR as {number, title, browser URL, head without refs/heads/}
             - a 500 and a full page each give (nil, err), never []
         - if a surface stays unwired, TestAzureDevOpsDispatchRoutes (extended) fails:
             - provider " Azure-DevOps " routes all four reads to the mock
             - an off-allowlist api_base gives ErrRefused with 0 hits

  t-2  Add ADO CI verdict and squash merge
       files:    internal/ship/shiploop.go (new), internal/ship/azuredevops_shiploop.go (new),
                 internal/ship/azuredevops_shiploop_test.go (new)
       desc:     shiploop.go holds the shared surface:
                   - the CIVerdict values pass / fail / pending / none / ask
                   - CIResult{Verdict, Link, Queued}
                   - CIStatus(opts, n, sha) and MergePR(opts, n, sha), dispatching azure-devops
                   - ErrShipLoopUnsupported for every other provider, plus the *Func seams
                 ADO verdict (ci_source lock):
                   - read the PR for its project GUID, source ref and lastMergeSourceCommit
                   - read builds on refs/heads/<head> at sha
                   - read the PR's policy evaluations, counting only build-validation and status-check types
                   - the verdict core takes those PR fields, so t-5 reuses it without a second PR read
                 ADO merge:
                   - PATCH status=completed, lastMergeSourceCommit=sha, mergeStrategy squash,
                     deleteSourceBranch false
                   - never send bypassPolicy or autoCompleteSetBy
                   - poll the PR until it is completed, within a bounded wait that tests can lower
       covers:   c-5, c-6
       depends:  —
       contract:
         - if the mapping drifts from ci_source, a TestADOCIVerdict row fails:
             - every build succeeded and every evaluation approved gives pass
             - a failed build gives fail with its _links.web.href
             - a canceled build gives fail
             - a rejected build-validation evaluation gives fail with its build's results link
             - an inProgress build or a running evaluation gives pending
             - only notStarted builds or queued evaluations give pending with Queued = true
             - nothing registered gives none
             - a lone notApplicable evaluation gives none
             - an isExpired build evaluation gives ask
         - if non-CI policy leaks into CI, TestADOCIVerdictScope fails:
             - rejected minimum-reviewer, comment-resolution and work-item evaluations leave a pass a pass
             - a build at another sourceVersion is ignored
             - no request path contains /_apis/policy/configurations
         - if a stale head reads green, a PR whose lastMergeSourceCommit differs from sha stops giving pending
           (it must never give pass)
         - if a failed read reads as an answer, a 500 on builds, a 500 on evaluations and a 203 sign-in page
           each fail to return an error (never none or pass)
         - if the merge payload drifts, TestADOMergePR fails:
             - the PATCH must carry status completed, lastMergeSourceCommit.commitId == sha,
               completionOptions.mergeStrategy squash and deleteSourceBranch false
             - bypassPolicy must be absent or false, and autoCompleteSetBy must be absent
             - no DELETE and no refs-update request may go out
         - if a non-merge reads as merged, TestADOMergePROutcome fails:
             - a GET that goes active, then completed gives nil
             - mergeStatus conflicts, rejectedByPolicy and failure each give an error naming the status
             - a PATCH 400 policy refusal gives an error
             - a PR still active at the lowered deadline gives an error naming awaiting-merge, never nil
         - if dispatch over-reaches, github, gitlab, forgejo and bitbucket CIStatus and MergePR each fail to
           give errors.Is(ErrShipLoopUnsupported) with 0 requests (bitbucket flips in t-3)

  t-6  Teach ship and review prompts the loop
       files:    assets/prompts/ship.md, assets/prompts/review.md, internal/cmd/ship_prompt_test.go
       desc:     ship.md §5, for azure-devops and bitbucket:
                   - poll `dross ship ci <phase-id>` every ~30s; the prompt owns the ~10-min window
                   - exit 0 goes to §6; 2 goes to On failure with the printed link; 3 keeps polling
                   - 4 (none), or a queued-only 3 at window end, asks proceed/stop; 5 asks
                   - never skip §5 because azure-pipelines.yml or bitbucket-pipelines.yml is absent
                 ship.md §6: run `dross ship merge <phase-id>` for both providers. Any non-zero exit is
                 surfaced, and the phase holds at awaiting-merge. Never bypass policy or arm auto-complete.
                 review.md §1, azure-devops: `git fetch origin`, then
                 `git diff origin/<base>...origin/phase/<phase-id>`, with base = the PR target recorded in
                 `dross changes show <id> --json`. Write it to review-diff.patch; the non-empty check stays.
                 Run make install afterwards (r-01).
       covers:   c-5, c-6, c-9, c-10, c-11
       depends:  —   (the command names and the exit-code table are fixed by this plan; t-4 cross-checks them)
       contract:
         - if the loop recipe drifts, TestShipPromptShipLoopProviders fails:
             - ship.md must name `dross ship ci <phase-id>` and `dross ship merge <phase-id>` under both
               azure-devops and bitbucket
             - each of 0/2/3/4/5 must appear with its action
             - none must be marked "never read as a pass"
         - if classic pipelines get skipped, the same test fails when §5's skip sentence names
           azure-pipelines.yml or bitbucket-pipelines.yml as a skip trigger
         - if a blocked completion is forced through, the same test fails when ship.md lacks "awaiting-merge"
           for a non-zero `dross ship merge`, or contains bypassPolicy, autoCompleteSetBy,
           deleteSourceBranch true or close_source_branch true
         - if the teardown owner regresses, TestShipPromptNamesCompleteAsTeardownOwner or
           TestShipPromptNoUnguardedSwitch fails
         - if the review recipe drifts, TestReviewPromptAzureDevOpsDiff fails:
             - review.md §1 must carry `git diff origin/<base>...origin/phase/<phase-id>` (three dots) into
               review-diff.patch under azure-devops
             - the "non-empty" check must still follow the recipes
             - §5 must still post through `dross ship comment`

Wave 2 (t-3 depends t-2; t-5 depends t-1, t-2)
  t-3  Add Bitbucket CI verdict and async merge
       files:    internal/ship/bitbucket_shiploop.go (new), internal/ship/bitbucket_shiploop_test.go (new),
                 internal/ship/shiploop.go, internal/ship/bitbucket.go
       desc:     Bitbucket arms for CIStatus and MergePR.
                 CI: GET .../commit/<sha>/statuses, following next pages. All SUCCESSFUL gives pass; any FAILED
                 or STOPPED gives fail with its url; any INPROGRESS gives pending; no statuses gives none.
                 Merge: POST .../pullrequests/<n>/merge {merge_strategy: squash, close_source_branch: false}.
                 A 200 means merged. A 202 means polling the task-status link until SUCCESS. bbRequest surfaces
                 the Location header, and stays the one screened entry; the link is host-checked against
                 api_base before the token rides along.
       covers:   c-10, c-11
       depends:  t-2
       contract:
         - if the status mapping drifts, a TestBitbucketCIVerdict row fails:
             - [SUCCESSFUL, SUCCESSFUL] gives pass
             - [SUCCESSFUL, FAILED] gives fail with the FAILED status's url
             - STOPPED gives fail
             - [SUCCESSFUL, INPROGRESS] gives pending
             - [FAILED, INPROGRESS] gives fail
             - [] gives none
             - a FAILED status on page 2 (next) gives fail
             - a 500 gives an error, never none
         - if the merge body drifts, TestBitbucketMergePR fails unless it sends merge_strategy squash and
           close_source_branch false
         - if an async merge is misread, TestBitbucketMergeAsync fails:
             - a 202 with Location, then task PENDING, PENDING, SUCCESS gives nil
             - a FAILED task gives an error
             - 400 / 409 / 555 refusals give errors
             - a still-PENDING task at the lowered deadline gives an error, never nil
         - if the poll can be steered off-host, a Location on another host fails to give a refusal with 0
           requests to that host and no token sent
         - if surfacing Location weakens the transport, TestScreenPrecedesRequestInEveryScreenedTransport or
           TestShipHelpersScrubAtSource (internal/cmd) fails naming bbRequest
         - if the arm is missing, bitbucket CIStatus and MergePR still give ErrShipLoopUnsupported

  t-5  List ADO PRs for watch with CI rollup
       files:    internal/ship/openprs.go, internal/ship/azuredevops_read.go, internal/ship/azuredevops_read_test.go,
                 internal/cmd/watch.go, internal/cmd/watch_test.go
       desc:     ListOpenPRs gains azure-devops, reusing t-1's active-PR list helper (a full page is refused).
                 Each record gets IsBot false, IsCrossRepository = forkSource set and HeadRefName stripped.
                 Checks come from t-2's verdict core: pass -> passing, fail -> failing, pending and ask ->
                 pending, none -> none. Any read failure gives (nil, err).
                 openPRDigest builds the full opts through remotePolicy + buildOpenOpts instead of
                 {Provider, URL}, and stays quiet on any failure.
       covers:   c-8
       depends:  t-1, t-2
       contract:
         - if the ADO list is misclassified, TestADOListOpenPRs fails. The active PRs are phase/a (builds
           succeeded), phase/b (failed build), feature/x and a forkSource phase/c:
             - heads come back stripped of refs/heads/
             - IsBot is false on every record
             - IsCrossRepository is true only on phase/c
             - phase/a rolls up passing and phase/b failing
             - an isExpired evaluation rolls up pending
         - if a partial list escapes, a full page fails to give (nil, err), and so does a verdict 500 on any
           one PR (never a list missing that PR)
         - if /dross-watch misses ADO end to end, TestWatchAzureDevOpsShipPRs fails. The setup is an httptest
           ADO remote set via project set, with a phase record for phase/a. `dross watch --json` must show:
             - ship_prs = [#a passing with its untriaged count, #b failing], with the fork's phase/c excluded
             - bot_prs present as []
         - if a failure prints an empty list, TestWatchAzureDevOpsQuiet fails: an ADO list 500, a 203 sign-in
           page and an off-allowlist api_base each must leave bot_prs and ship_prs absent, exit 0 and print
           nothing on stderr
         - if the opts change regresses other providers, TestWatchPRDigestJSON or
           TestWatchPRsUnsupportedProvider fails

Wave 3 (depends t-2, t-3, t-6)
  t-4  Add ship ci and ship merge commands
       files:    internal/cmd/ship_loop.go (new), internal/cmd/ship_loop_test.go (new), internal/cmd/ship.go
       desc:     `dross ship ci <phase-id>` and `dross ship merge <phase-id>`. Each:
                   - reads the PR number from changes.json
                   - takes sha = rev-parse origin/phase/<id> through gitRefArgs
                   - builds opts via remotePolicy + buildOpenOpts
                   - calls ship.CIStatusFunc or ship.MergePRFunc
                 ci prints the verdict (pending says queued or running) and the failing link. Exit codes:
                 pass 0, fail 2, pending 3, none 4, ask 5, lookup error 1. merge exits 0 only once merged and
                 otherwise prints the reason. An unsupported provider is told "unsupported — use the ship.md
                 recipe".
       covers:   c-5, c-6, c-10, c-11
       depends:  t-2, t-3, t-6
       contract:
         - if exit codes drift, TestShipCIExitCodes fails:
             - stubbed pass / fail / pending / none / ask give ExitCode 0 / 2 / 3 / 4 / 5
             - fail prints the link
             - pending prints queued vs running
             - a stub error exits 1 and never prints pass
         - if the prompt and the binary disagree, TestShipPromptCIExitCodesMatch fails: the codes ship.md §5
           lists must equal ship ci's constants
         - if ci watches the wrong commit, TestShipCIAzureDevOpsFlow fails. With an httptest ADO remote via
           project set, record pr 42 and a fixture origin, the builds query must carry
           branchName=refs/heads/phase/x and a sha equal to rev-parse origin/phase/x
         - if merge is not pinned or tears down, TestShipMergeAzureDevOpsPinsHead fails:
             - the PATCH's lastMergeSourceCommit.commitId must equal rev-parse origin/phase/x, with
               deleteSourceBranch false
             - after exit 0, `git ls-remote origin phase/x` must still list the branch
         - if a failed merge reads as merged, TestShipMergeBitbucketAsync fails: a 202 that ends in SUCCESS
           must print merged and exit 0; a FAILED task must exit non-zero and never print merged
         - if an unsupported provider guesses, TestShipLoopUnsupportedProvider fails unless github exits 1
           naming "unsupported" and ship.md with 0 HTTP requests; with no PR recorded it must refuse,
           naming `dross ship <id>`
         - if the rev-parse positional is unseparated, TestNoUnseparatedGitPositional fails

Wave 4 (depends t-1, t-4, t-6)
  t-7  Run and record the live ADO smoke
       files:    .dross/phases/azure-devops-ship-loop/live-smoke.toml (new), internal/ship/azuredevops_comment.go,
                 internal/ship/azuredevops_comment_test.go   (the last two only on the blocks outcome)
       desc:     After make install, run in pair-mode in a scratch repo on the real org with the user's PAT:
                   1. dross ship opens the PR; a re-run must dedupe
                   2. dross ship comment posts a thread
                   3. dross pr comments lists it, plus an inline comment left in the web UI
                   4. head and self lookups run
                   5. /dross-respond replies
                   6. dross ship ci reaches a verdict
                   7. dross ship merge squash-merges
                   8. git ls-remote checks phase/<id>
                   9. merged-status reads merged
                 Record each leg's command, observed value and pass/fail, and whether a statusless thread
                 blocks completion under a comment-resolution policy. If it does, set adoThreadStatus = "closed"
                 (the locked fallback) and re-run the leg.
       covers:   c-7
       depends:  t-1, t-4, t-6
       contract:
         - if dedupe breaks live, the re-run leg records a second PR number and fails
         - if the real threadContext shape differs from the mock, the listing leg's inline comment loses its
           file:line and fails
         - if connectionData's id is not the thread author id, `dross pr comments` lists dross's own thread as
           a reviewer item and the identity leg fails
         - if completion deletes the source branch, `git ls-remote origin refs/heads/phase/<id>` is empty after
           the merge and the merge leg fails
         - if ship ci misreads a real pipeline, the ci leg's verdict disagrees with the build page and fails
         - if a statusless thread blocks completion, the record says blocks = true. TestPostAzureDevOpsThread
           must then assert status closed in the same commit, and the merge leg must pass after the re-run
         - if any leg is skipped or mock-substituted, it carries no pass in live-smoke.toml, so c-7 is not
           live-verified. live-smoke.toml is never cited for c-1..c-6, c-8..c-11
```

## Coverage

| criterion | tasks |
|---|---|
| c-1  ADO ListPRComments | t-1 (live: t-7) |
| c-2  ADO PRHeadOf | t-1 (live: t-7) |
| c-3  ADO AuthenticatedUser | t-1 (live: t-7) |
| c-4  ADO OpenPRsTargeting | t-1 |
| c-5  ADO §5 CI watch | t-2 (verdict), t-4 (`ship ci`), t-6 (§5 polling + window) |
| c-6  ADO §6 squash merge | t-2 (merge), t-4 (`ship merge`), t-6 (§6) |
| c-7  live smoke run | t-7 |
| c-8  /dross-watch on ADO | t-5 |
| c-9  /dross-review §1 diff | t-6 |
| c-10 Bitbucket ship ci | t-3 (verdict), t-4 (command), t-6 (§5) |
| c-11 Bitbucket ship merge | t-3 (async merge), t-4 (command), t-6 (§6) |

All 11 criteria are covered. No task exists outside a criterion.

## Judgment calls

- **One task for all four ADO reads (c-1..c-4).** Rejected: one task per surface, or two pairs. The four reads
  share a transport and a dispatch shape, and TestAzureDeferredSurfacesFailClosed guards exactly these four, so
  one commit retires it whole. Any split leaves a half-wired guard edited across tasks. The task is 5 files but
  one layer.
- **Comment id = `<threadId><commentId %05d>`, digits only.** Rejected: a "12.3" id plus widening prtriage's
  `^[cir][0-9]+` regex. That costs two more files in another package. ADO comment ids are int16, so a fixed
  5-digit suffix is injective, and it stays "thread + comment" as c-1 requires.
- **c-4 rides with c-1..c-3, not with watch (c-8).** Rejected: pairing the two active-PR list readers. Watch
  needs t-2's verdict, which would push c-4 to wave 2 and leave the fail-closed guard split across tasks.
- **CI and merge backends split by provider (t-2 ADO, t-3 Bitbucket), not by verb.** Rejected: a by-verb split
  (ci.go and merge.go, both wave 1). That gives 5 files per task with both providers in each. By provider keeps
  each task at 3–4 files around one transport. t-2 owns the shared shiploop.go, which costs one wave of
  parallelism for Bitbucket.
- **One cmd task for both `ship ci` and `ship merge` (t-4).** Rejected: one task per verb. They share the same
  phase -> PR -> sha -> opts resolution and the exit-code plumbing.
- **sha = `rev-parse origin/phase/<id>` in both verbs.** Rejected: re-reading the PR head inside merge, or a
  `--sha` flag. Re-reading at merge time pins whatever is there, not what was watched. Reading the same local
  ref in both verbs pins the watched SHA with no new flag, and ADO refuses the completion if the PR head moved.
- **`ship merge` has one failure exit code.** Rejected: a separate "blocked" code. ship.md handles conflicts,
  policy rejection and a blocked completion the same way (surface, hold at awaiting-merge), so a second code
  would have no reader.
- **The 10-minute window lives only in ship.md.** Rejected: a `--window` flag on `ship ci`. ci_merge_home locks
  the window to ship.md. `ship ci` only marks pending as queued or running so the prompt can tell
  "queued at window end".
- **watch rollup maps ask -> pending.** Rejected: ask -> none. An expired build policy is unsettled, not absent,
  and watch has no ask state.
- **All prompt edits in one wave-1 task (t-6), review.md included.** Rejected: a separate review.md task (one
  file, under 10 minutes) and putting t-6 after t-4. The plan fixes the command names and exit codes, and t-4
  adds a drift test that pins ship.md's codes to the binary.
- **connectionData is called with `<version>-preview`.** Rejected: the plain version. A preview version is
  accepted for released resources too, so the suffix is safe whether or not the service demands it. c-7's
  identity leg confirms it.
- **Live record at `.dross/phases/azure-devops-ship-loop/live-smoke.toml`, next to verify.toml.** Rejected: an
  env-gated Go live test. c-7 drives the CLI and /dross-respond, which a Go test cannot exercise. A gated test
  would also be new structure no criterion asks for.
- **The adoThreadStatus flip is conditional inside t-7.** Rejected: a standalone task. It is the previous
  phase's locked one-line fallback, triggered only by the live finding that c-7 records.
- **Not touched: watch.md, README, docs/dross.1, ARCHITECTURE.md.** watch.md's "provider other than GitHub" and
  "no dross verb merges a PR" lines go stale, but no criterion depends on them and the digest behaviour is
  correct without them.
