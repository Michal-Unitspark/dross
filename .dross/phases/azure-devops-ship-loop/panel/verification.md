# Planner draft: verification lens

Lens: I wrote each criterion's ideal test contract first, then worked out the smallest task that makes the contract satisfiable. Each task below gives its contract lines as "if X breaks, test Y fails". The file lists are the minimum each contract needs.

Facts from reading the tree that shaped the contracts:
- `prtriage` validates item ids with `^[cir][0-9]+(#[1-9][0-9]*)?$` in `internal/prtriage/record.go`, and `internal/cmd/pr_resolve.go` has a duplicate copy. A thread+comment ADO id (c-1) fails both, so the grammar has to widen.
- `openPRDigest` (`internal/cmd/watch.go`) calls `ListOpenPRsFunc` with only `{Provider, URL}`. An ADO lister needs `api_base`, `auth_env` and `Hosts`, so without a change c-8 would stay silently empty forever.
- `TestShipPromptNoUnguardedSwitch` bans `deletesourcebranch` (the normalised form) anywhere in ship.md. The ADO merge recipe therefore cannot name that field, and has to live in `dross ship merge`.
- `connectionData` returns 400 (VssInvalidPreviewVersionException) for `api-version=7.1`. It needs `7.1-preview`, and `adoRequest` would otherwise stamp the configured version on it.
- `bbRequest` is the registered Bitbucket seam (`secretscan/sinks.go`, `redact_test.go` scrubbingHelpers) and returns no headers. The async merge's task-status link comes back in the 202's `Location` header.
- `TestAzureDeferredSurfacesFailClosed` asserts that all four deferred surfaces send 0 requests. Whichever task wires each surface must edit it, so this draft assigns that test to one task at a time.
- Each new provider dispatch switch (`CIStatus`, `MergePR`) is only guarded once it is registered in `enumScanSites` (`internal/cmd/enum_divergence_test.go`).

```
Phase azure-devops-ship-loop — 12 tasks across 4 waves

Wave 1
  t-1  Widen triage ids to thread.comment form
       files:    internal/prtriage/record.go, internal/prtriage/record_test.go,
                 internal/cmd/pr_resolve.go, internal/cmd/pr_resolve_test.go
       desc:     prtriage exports one item-id check accepting <c|i|r><n>[.<m>][#<k>]. Record
                 validation and `dross pr resolve` both call it, and pr_resolve.go's duplicate
                 triageItemID regexp is deleted.
       covers:   c-1
       contract: if the grammar stays numeric-only, TestRecordAcceptsThreadCommentIDs fails:
                 resolutions i6.2, c5.1 and c5.1#3 validate, and c7, i7#1 and r9 still validate
       contract: if the grammar is loose, the same table fails: i6., i.2, i6.2.3, i6..2, i6.2#0
                 and x6.2 are each refused with an error naming the id
       contract: if the two checks drift, TestTriageIDCheckIsShared fails: for every row of that
                 table, `dross pr resolve 7 <id> --seen … --reject --reason r --at a.go:1` refuses
                 exactly the ids prtriage refuses, and pr_resolve.go declares no regexp of its own
       contract: if the letter/kind cross-check is lost, a row fails: i6.2 recorded with kind
                 conversation still errors "the id says inline"
       depends:  —

  t-2  Read ADO PR threads, head and caller
       files:    internal/ship/azuredevops_threads.go, internal/ship/azuredevops_threads_test.go,
                 internal/ship/prcomments.go, internal/ship/azuredevops_dispatch_test.go
       desc:     azureDevOpsPRComments (GET …/pullRequests/<n>/threads, no status filter),
                 azureDevOpsPRHead (GET …/pullRequests/<n>), azureDevOpsAuthenticatedUser
                 (GET <api_base>/_apis/connectionData at <api-version>-preview); three dispatch arms.
                 IDs are "<thread>.<comment>". GUIDs are lowercased on both sides. Decoded structs
                 stay function-local (no not_paths.txt row). TestAzureDeferredSurfacesFailClosed is
                 narrowed to OpenPRsTargeting only.
       covers:   c-1, c-2, c-3
       contract: if mapping breaks, TestAzureDevOpsPRComments fails. Thread 5 (no threadContext),
                 comment 1, must give {ID "5.1", conversation}. Thread 6 with threadContext.filePath
                 "/internal/a.go" and rightFileStart.line 12 must give two inline comments, 6.1 and
                 6.2, each with Path "internal/a.go" (leading slash dropped) and Line 12. A
                 leftFileStart-only thread takes the left line. URL must be
                 <canonical>/_git/R/pullrequest/7?discussionId=6.
       contract: if a filter is wrong, the same test fails. commentType system comments (a vote,
                 a RefUpdate push, a status change), codeChange comments, isDeleted comments and
                 isDeleted threads must be absent. A text comment in a thread whose status is fixed,
                 closed, wontFix, byDesign or pending must be present (thread_listing lock). The
                 request must carry no status filter.
       contract: if the author is guessed, the test fails: Author.ID = author.id lowercased,
                 Login = uniqueName, Bot = unknown for every comment, including a
                 "Project Collection Build Service" author
       contract: if a failure turns partial, TestAzureDevOpsPRCommentsFailClosed fails: a 500, a
                 203 sign-in page, non-JSON, a body without `value`, and a thread or comment with
                 id 0 each return (nil, err); n<=0 sends 0 requests
       contract: if head/state mapping drifts, TestAzureDevOpsPRHead fails:
                 refs/heads/phase/x → Ref phase/x; active → Open; completed and abandoned → !Open;
                 an empty or unknown status → error (never Open=false with nil); forkSource present
                 → CrossRepo; URL = <canonical>/pullrequest/7; a missing sourceRefName → error
       contract: if the caller lookup is mis-addressed, TestAzureDevOpsAuthenticatedUser fails:
                 the GET hits <api_base>/_apis/connectionData (no project segment). Its
                 api-version is 7.1-preview for configured 7.1 and 6.0-preview for 6.0, while
                 7.1-preview.1 is kept unchanged. It returns {authenticatedUser.id lowercased,
                 properties.Account.$value}; a missing id → error, never Account{}.
       contract: if the identity join breaks, TestAzureDevOpsSelfMatchesThreadAuthor fails:
                 connectionData id "AB12…" (upper case) and thread comment author.id "ab12…" give
                 Account.ID == PRComment.Author.ID
       contract: if any of the three reads the token before the host check,
                 TestAzureDevOpsThreadReadsRefuseOffAllowlist fails: an api_base off the derived
                 policy gives ErrRefused with 0 hits for comments, head and caller
       depends:  —

  t-3  Judge ADO CI from builds + evaluations
       files:    internal/ship/ci.go, internal/ship/azuredevops_ci.go,
                 internal/ship/azuredevops_ci_test.go
       desc:     ci.go: CIVerdict (pass/fail/pending/none/ask); CIReport {Verdict, Queued, Link,
                 Reason, Head}; CIStatus(opts, head, branch) dispatching via configenum.Normalize
                 with an azure-devops arm; ErrCIUnsupported default; CIStatusFunc seam.
                 azuredevops_ci.go reads the PR (repository.id, repository.project.id,
                 lastMergeSourceCommit), the builds on refs/heads/<branch> for that repo, and the
                 policy evaluations by artifactId. These map into untagged adoCheck values and a
                 pure adoVerdict, which t-8 reuses.
       covers:   c-5
       contract: if the ci_source mapping drifts, TestADOVerdictTable (pure adoVerdict) fails.
                 Each row is an input → expected result:
                 - succeeded build + approved Build evaluation → pass
                 - failed build → fail, Link = the build's _links.web.href; canceled → fail
                 - rejected Status-policy evaluation → fail
                 - rejected Build evaluation with context.buildId 88 → fail, Link ending
                   _build/results?buildId=88
                 - failed + inProgress → fail (fail beats pending)
                 - inProgress → pending with Queued=false
                 - only notStarted builds or queued evaluations → pending with Queued=true
                 - queued + inProgress → pending with Queued=false
                 - nothing at all → none; a lone notApplicable evaluation → none
                 - a Build evaluation with context.isExpired → ask
                 - partiallySucceeded, broken, or an unrecognised status string → ask, never pass
       contract: if non-CI policy leaks into the verdict, a row fails: Required reviewers,
                 Minimum reviewers, Comment requirements and Work item linking evaluations
                 (rejected or queued) leave a passing build at pass. A rejected optional
                 (isBlocking=false) Build evaluation still fails.
       contract: if the reads are mis-addressed, TestAzureDevOpsCIRequests fails. The builds GET
                 must be /coll/P/_apis/build/builds with branchName=refs/heads/phase/x,
                 repositoryId=<PR repository.id> and repositoryType=TfsGit. The evaluations GET
                 must be /coll/P/_apis/policy/evaluations with
                 artifactId=vstfs:///CodeReview/CodeReviewId/<repository.project.id>/7. No
                 request path may contain policy/configurations (branch-policy config is never
                 read).
       contract: if the wrong builds are counted, TestAzureDevOpsCICountsOnlyHeadBuilds fails:
                 a failed build at another sourceVersion, and one from another repository id that
                 the mock returns despite the filter, both leave the verdict none
       contract: if a stale PR passes, a row fails: a PR whose lastMergeSourceCommit differs from
                 head gives pending, with a Reason naming both SHAs
       contract: if a failed read becomes a verdict, TestAzureDevOpsCIFailClosed fails: a 500,
                 a 203 or non-JSON on the PR, builds or evaluations read gives (CIReport{}, err),
                 never none; a builds page that is full at $top gives an error
       contract: if dispatch or the guard slips, TestCIStatusDispatch fails: github, gitlab,
                 forgejo and gitea give errors.Is(ErrCIUnsupported) with 0 requests; " Azure-DevOps "
                 routes to the ADO arm; an off-allowlist api_base gives ErrRefused with 0 hits
       depends:  —

  t-4  Complete ADO PR as pinned squash
       files:    internal/ship/mergepr.go, internal/ship/azuredevops_merge.go,
                 internal/ship/azuredevops_merge_test.go
       desc:     mergepr.go: MergeOutcome (merged/blocked/conflicts/failed/pending); MergeResult;
                 MergePR(opts, head) dispatching with an azure-devops arm; ErrMergeUnsupported;
                 MergePRFunc seam; poll interval and bound as test-lowerable vars (bound ≤ ~90s, so
                 one Bash call never outlives its timeout). ADO: GET the PR, refuse when
                 lastMergeSourceCommit ≠ head, PATCH the completion, then poll until completed or
                 a terminal mergeStatus.
       covers:   c-6
       contract: if the completion body drifts, TestAzureDevOpsMergeBody fails. The PATCH
                 …/pullRequests/7 must carry status completed, lastMergeSourceCommit.commitId ==
                 head, completionOptions.mergeStrategy "squash", completionOptions.squashMerge true,
                 and completionOptions.deleteSourceBranch present and false. A recursive JSON walk
                 must find no bypassPolicy, bypassReason or autoCompleteSetBy key at any depth.
       contract: if an unwatched commit can merge, TestAzureDevOpsMergePinsWatchedHead fails:
                 PR lastMergeSourceCommit bbb with head aaa is refused naming both, with 0 PATCHes
       contract: if the wait is skipped or unbounded, TestAzureDevOpsMergeWaitsForCompleted
                 fails: GETs answering active/queued, active/queued, completed give merged after
                 3 polls; still active at the bound gives pending ("re-run dross ship merge"),
                 never merged
       contract: if a failed merge reads as merged, TestAzureDevOpsMergeTerminalStates fails:
                 mergeStatus conflicts → conflicts, rejectedByPolicy → blocked, failure → failed;
                 a PATCH 400/409 → failed carrying ADO's scrubbed message with 0 polls; none of
                 these sets Merged
       contract: if a re-run double-completes, a row fails: a PR that is already completed gives
                 merged with 0 PATCHes; an abandoned PR gives an error
       contract: if teardown sneaks in, every row's mock sees 0 requests under /refs and 0 DELETEs
       contract: if dispatch or the guard slips, TestMergePRDispatch fails: github, gitlab and
                 forgejo give ErrMergeUnsupported with 0 requests; off-allowlist gives ErrRefused
                 with 0 hits
       depends:  —

Wave 2
  t-5  Read Bitbucket CI from commit statuses
       files:    internal/ship/bitbucket_ci.go, internal/ship/bitbucket_ci_test.go,
                 internal/ship/ci.go
       desc:     bitbucketCI reads the PR's source.commit.hash and requires it to prefix head
                 (else pending). It then pages /commit/<head>/statuses by page number, never by
                 following the `next` URL, and maps the states. ci.go gains the bitbucket arm.
       covers:   c-10
       contract: if the state mapping drifts, TestBitbucketCIVerdict fails:
                 - all SUCCESSFUL → pass
                 - one FAILED → fail with that status's url as Link
                 - STOPPED → fail
                 - INPROGRESS among SUCCESSFUL → pending
                 - FAILED + INPROGRESS → fail
                 - empty values → none
                 - an unknown state → ask
       contract: if the wrong commit is read, TestBitbucketCIReadsHeadStatuses fails: statuses
                 are read at /repositories/ws/slug/commit/<full head>/statuses; a PR hash that
                 does not prefix head gives pending with 0 status requests
       contract: if paging trusts the server, a row fails: page 2 is fetched as page=2 on the
                 same endpoint; a `next` link to another host sees 0 requests; more than
                 prThreadMaxPages full pages gives an error
       contract: if a failed read becomes none, a row fails: a 500 or non-JSON on the PR or the
                 statuses gives (CIReport{}, err); a missing auth_user or off-allowlist api_base
                 errors with 0 hits
       depends:  t-3

  t-6  Squash-merge Bitbucket PR, wait out async
       files:    internal/ship/bitbucket_merge.go, internal/ship/bitbucket_merge_test.go,
                 internal/ship/mergepr.go, internal/ship/bitbucket.go
       desc:     POST …/pullrequests/<n>/merge?async=true with {merge_strategy: squash,
                 close_source_branch: false}. 200 MERGED → merged. 202 → poll the Location
                 task-status link until SUCCESS, following it only when it lies under this PR's
                 merge/task-status/ path on api_base. bbRequest gets an optional trailing header
                 out-param so it stays the one Bitbucket request entry (no ledger change, no
                 call-site churn). mergepr.go gains the arm.
       covers:   c-11
       contract: if the body drifts, TestBitbucketMergeBody fails: merge_strategy "squash", and
                 close_source_branch present and false
       contract: if the async path is misread, TestBitbucketMergeAsync fails:
                 - 200 state MERGED → merged with 0 task-status polls
                 - 202 + Location …/pullrequests/7/merge/task-status/T answering PENDING, PENDING,
                   SUCCESS → merged after 3 polls
                 - still PENDING at the bound → pending, never merged
       contract: if Basic auth can be steered, TestBitbucketMergeRefusesForeignTaskLink fails:
                 a Location on another host, or on another PR's path, errors with 0 requests to it
       contract: if a refusal reads as merged, a row fails: a 409, 400 or 555 on the POST, or a
                 task-status with an error or unknown state, gives failed with Merged false and a
                 scrubbed message
       contract: if teardown sneaks in, every row sees 0 DELETEs and 0 requests under /refs
       contract: if a second Bitbucket request builder appears, TestEveryOutboundSeamIsRegistered
                 (internal/cmd) and TestShipHelpersScrubAtSource fail naming it; the existing
                 bitbucket_test.go and bitbucket_comment_test.go stay green unchanged
       depends:  t-4

  t-7  List active ADO PRs targeting a base
       files:    internal/ship/azuredevops_pr.go, internal/ship/azuredevops_pr_test.go,
                 internal/ship/basepr.go, internal/ship/azuredevops_dispatch_test.go
       desc:     adoOpenPRsTargeting GETs …/pullrequests with
                 searchCriteria.targetRefName=refs/heads/<base>, searchCriteria.status=active and
                 $top=100, then re-checks target and status client-side. OpenPRsTargeting gains
                 the arm. TestAzureDeferredSurfacesFailClosed is retired; its last row is replaced
                 by the real contract below.
       covers:   c-4
       contract: if mapping or the query drifts, TestOpenPRsTargetingAzureDevOps fails: the
                 query carries both searchCriteria params (a base with & or + round-trips); hit
                 {9, "Add x", refs/heads/phase/x} gives BasePR{9, "Add x",
                 <canonical>/pullrequest/9, "phase/x"}; a fork PR targeting the base is still
                 listed
       contract: if the server filter is trusted, a mock ignoring it (a refs/heads/other target,
                 an abandoned PR) still gives only the matching active PR
       contract: if a failed lookup can authorize a delete, TestOpenPRsTargetingAzureDevOpsFailClosed
                 fails: a 500, a 203, non-JSON, or a missing `value` gives (nil, err) that is not
                 errors.Is(ErrBasePRLookupUnsupported); a page of exactly 100 errors; an empty base
                 errors with 0 requests
       contract: if the guard slips, an off-allowlist api_base gives ErrRefused with 0 hits
       depends:  t-2  (owns azuredevops_dispatch_test.go in wave 1)

  t-8  List ADO open PRs with CI rollup
       files:    internal/ship/azuredevops_openprs.go, internal/ship/azuredevops_openprs_test.go,
                 internal/ship/openprs.go
       desc:     ListOpenPRs gains an azure-devops arm: active PRs at $top=100 → OpenPRRecord.
                 Non-fork PRs with a phase/ or milestone/ head get Checks from t-3's adoVerdict at
                 their lastMergeSourceCommit; every other PR gets ChecksNone with no CI read.
                 Failures stay quiet.
       covers:   c-8
       contract: if record mapping drifts, TestListOpenPRsAzureDevOps fails. {pullRequestId 42,
                 title, creationDate, sourceRefName refs/heads/phase/x, createdBy.uniqueName} must
                 give Number 42, HeadRefName phase/x, URL <canonical>/pullrequest/42, CreatedAt
                 parsed, and Author.IsBot false (also for a Build Service creator); forkSource →
                 IsCrossRepository true.
       contract: if the rollup diverges from ship ci, TestADOWatchChecksMatchShipCI fails: for
                 every TestADOVerdictTable fixture, pass/fail/pending/none give
                 passing/failing/pending/none, and ask gives pending (never passing)
       contract: if CI is read for every PR, TestListOpenPRsAzureDevOpsFanOut fails: a fork
                 phase/y PR and a feature/z PR get ChecksNone with 0 builds or evaluations
                 requests for them
       contract: if a failure leaks, TestListOpenPRsAzureDevOpsQuiet fails: a list 500, a 203, a
                 missing creationDate, a page of 100, or one PR's builds read failing gives
                 (nil, err), with nothing written to stderr
       contract: if scope creeps, gitlab, forgejo and bitbucket still give
                 errors.Is(ErrOpenPRListUnsupported) with 0 requests; an off-allowlist api_base
                 gives ErrRefused with 0 hits
       depends:  t-3

Wave 3
  t-9  Add dross ship ci and ship merge
       files:    internal/cmd/ship_ci.go, internal/cmd/ship_ci_test.go, internal/cmd/ship.go,
                 internal/cmd/enum_divergence_test.go
       desc:     Two subcommands taking <phase-id>. Each reads the PR number from changes.json and
                 the head from `git rev-parse origin/phase/<id>`, then calls CIStatusFunc or
                 MergePRFunc. Each prints one result line and exits with one code per
                 verdict/outcome. Providers without an arm print "unsupported, use the ship.md
                 recipe". The ship.CIStatus and ship.MergePR switches are registered as enum
                 scan sites.
       covers:   c-5, c-6, c-10, c-11
       contract: if a verdict is mis-rendered, TestShipCIVerdictExitCodes fails. Stubbed seams
                 must print:
                 - `ci: pass` with exit 0
                 - `ci: fail — <link>`
                 - `ci: pending (queued)` vs `ci: pending (running)`
                 - `ci: none`
                 - `ci: ask — <reason>`
                 The four non-pass ci codes and the four non-merged merge codes must be pairwise
                 distinct, and none may be 0 or 1.
       contract: if a failed read becomes a verdict, TestShipCIReadErrorIsNotAVerdict fails: a
                 seam error exits 1 with no `ci:` line
       contract: if the wrong PR or SHA is judged, TestShipCIResolvesHeadAndPR fails:
                 changes.json pr 42 and origin/phase/x at S give a seam call with PRNumber 42,
                 head S and branch phase/x. With no recorded PR, it errors naming `dross ship x`
                 with 0 seam calls; with no origin/phase/x ref, it errors with 0 seam calls.
       contract: if a non-merge reads as merged, TestShipMergeOutcomes fails: merged exits 0
                 printing the PR URL; blocked, conflicts, failed and pending each exit with their
                 own code and a line naming the outcome
       contract: if wiring breaks end to end, TestShipCIAzureDevOpsFlow (newADOShipMock, a
                 fixture with no azure-pipelines.yml) fails: a succeeded build at S plus an
                 approved evaluation gives exit 0, and `dross ship merge x` PATCHes
                 lastMergeSourceCommit S
       contract: if provider coverage drifts, TestCIMergeDispatchSet fails: ship.CIStatus and
                 ship.MergePR dispatch exactly {azure-devops, bitbucket}, a subset of
                 ShipProviders, and the new enumScanSites rows feed
                 TestDispatchUsesConfigenumNormalize and TestGuardsSeeNonEmptySets
       contract: if github is half-wired, a github project prints "ship.md" with 0 HTTP requests
       depends:  t-3, t-4, t-5, t-6

  t-10 Prove ADO respond and watch through cmd
       files:    internal/cmd/watch.go, internal/cmd/watch_test.go, internal/cmd/pr_azure_test.go
       desc:     openPRDigest passes buildOpenOpts(p, hosts) instead of {Provider, URL}.
                 Cmd-level flows over newADOShipMock prove /dross-watch and /dross-respond
                 (comments, resolve, reply) on azure-devops.
       covers:   c-1, c-2, c-3, c-8
       contract: if openPRDigest keeps passing only {Provider, URL}, TestWatchAzureDevOpsShipPRs
                 fails. The fixture is a PR list {42 phase/x, 43 phase/y fork, 44 feature/z}, with
                 #42 builds and evaluations passing, and #42 threads holding one reviewer comment
                 plus one ReplyMarker reply by the connectionData id; phase x has a record.
                 `dross watch --json` must give ship_prs == [{42, phase/x, passing, untriaged 1}]
                 and bot_prs == [] present.
       contract: if a failure prints an empty list, TestWatchAzureDevOpsQuiet fails: a list 500
                 or a page of 100 omits both PR fields (absent, not []), exits 0, and leaves
                 stderr empty
       contract: if listing/identity breaks across layers, TestPRCommentsAzureDevOps fails. On
                 phase/x, `dross pr comments 42` must print `i6.2 · inline · @rev@corp.example ·
                 unknown` with internal/a.go:12, and `c5.1`. dross's own ReplyMarker thread must be
                 absent. A reviewer comment copying ReplyMarker must be present. dross's own
                 /dross-review thread must split into c<t>.1#1..n.
       contract: if the respond loop cannot close, TestPRResolveAndReplyAzureDevOps fails:
                 `dross pr resolve 42 i6.2 --seen <d> --reject --reason r --at internal/a.go:12`
                 records the verdict, then `dross pr reply 42 --post` with a recorded approval
                 POSTs one …/pullRequests/42/threads whose first line is ReplyMarker
       contract: if head gating breaks, a PR whose source carries forkSource, and a completed
                 PR, each make `dross pr comments 42` refuse
       depends:  t-1, t-2, t-8

Wave 4
  t-11 Teach ship and review prompts the loop
       files:    assets/prompts/ship.md, internal/cmd/ship_prompt_test.go,
                 assets/prompts/review.md, internal/cmd/review_prompt_test.go
       desc:     ship.md §5 gets an azure-devops + bitbucket bullet that polls
                 `dross ship ci <phase-id>` every ~30s and owns the ~10-minute window. §5 On
                 failure uses the printed link. §6 runs `dross ship merge <phase-id>`; a non-zero
                 exit is surfaced and the phase holds at awaiting-merge. review.md §1 gets the
                 azure-devops local three-dot diff. The GitHub/Forgejo/GitLab recipes are not
                 touched. Run `make install` afterwards (r-01).
       covers:   c-5, c-6, c-9, c-10, c-11
       contract: if the poll loop is wrong, TestShipPromptPollsShipCI fails. §5 must name
                 `dross ship ci <phase-id>` for azure-devops and bitbucket and map:
                 - pass → §6
                 - fail → On failure with the printed link
                 - pending → keep polling
                 - none → keep polling until the ~10-minute window ends, then ask
                 - `pending (queued)` at the window's end → ask
                 - ask → ask to proceed or stop
                 It must also state that none and ask are never read as a pass.
       contract: if classic pipelines can be skipped, TestShipPromptWatchesClassicPipelines
                 fails: §5 must say an azure-devops repo with no azure-pipelines.yml is still
                 watched, and the skip sentence must exclude azure-devops and bitbucket
       contract: if a merge failure can be read as merged, TestShipPromptMergesViaShipMerge
                 fails: §6 must run `dross ship merge <phase-id>` for both providers, send any
                 non-zero exit (blocked by policy, conflicts, failed, not yet completed) to §7 as
                 awaiting-merge, and offer no curl for either
       contract: if the prompt can instruct teardown or a bypass, TestShipPromptNoUnguardedSwitch
                 fails: its forbidden list gains closesourcebranch and bypasspolicy (it already
                 has deletesourcebranch)
       contract: if a deferred provider is moved by accident, TestShipPromptGitHubCIWatchWaitsForRuns
                 and TestShipPromptGitLabSections go red
       contract: if the review diff recipe drifts, TestReviewPromptAzureDiffRecipe fails. The
                 review.md §1 azure-devops bullet must run `git fetch origin` and then
                 `git diff origin/<base>...origin/phase/<phase-id>` (three dots) into
                 review-diff.patch, with <base> named as the PR's target branch (changes.json
                 base). The "non-empty" check must still follow the bullets, §5 must still post
                 with `dross ship comment --pr`, and §1 must carry no curl or Authorization for
                 azure-devops.
       depends:  t-9

  t-12 Live smoke the ADO ship loop
       files:    internal/cmd/azure_live_test.go,
                 .dross/phases/azure-devops-ship-loop/live-smoke.toml,
                 internal/ship/azuredevops_comment.go, internal/ship/azuredevops_comment_test.go
       desc:     A `//go:build adolive` test drives the cobra commands against the real org (env
                 DROSS_ADO_LIVE_URL, DROSS_ADO_LIVE_PAT) and writes one outcome per step. The
                 result is committed as live-smoke.toml. The two comment files change only if the
                 run shows that a statusless thread blocks completion; in that case the locked
                 comment_thread_status fallback flips the thread status to closed. Run
                 `make install` first (r-01).
       covers:   c-7
       contract: if a mocked run could pass as live, the build tag keeps the file out of
                 `go test ./...`; with the tag but a missing env var, it t.Skips naming the
                 variable (never PASS)
       contract: if any loop step regresses on the real API, its own assertion fails:
                 - `dross ship` opens #N
                 - a re-run prints "found open PR #N", and the org lists one active PR from
                   phase/<id>
                 - `dross ship comment` posts a thread
                 - the listing has an inline comment with a non-empty path and line > 0
                 - PRHeadOf gives phase/<id>, Open, !CrossRepo
                 - AuthenticatedUser().ID equals the author id on dross's thread
                 - `dross pr reply --post` lands a ReplyMarker thread that prtriage.Items drops
                   as own
                 - `dross ship ci` reaches a verdict (recorded verbatim; none and ask are
                   recorded as such)
                 - `dross ship merge` exits 0
                 - GetPRStatus reports Merged
                 - GET refs?filter=heads/phase/<id> still returns the branch
       contract: if the comment-resolution question goes unanswered, the record lacks
                 blocks_completion. That value is read from the PR's Comment requirements
                 evaluation after the thread posts. If it is true, the fallback lands here and
                 TestPostAzureDevOpsThread's status row flips to closed in the same commit.
       contract: if live is overstated, live-smoke.toml fails its own writer's check: it must
                 carry kind = "live", the org host, the PR URL, the head SHA, the run date and one
                 outcome per step, and any failed step makes the record's verdict fail
       depends:  t-9, t-10
```

## Coverage

For each criterion: the decisive contract (written first), then the tasks that make it satisfiable.

| Criterion | Decisive contract | Tasks |
|---|---|---|
| c-1 | Thread/inline/system/deleted mapping and composite ids (TestAzureDevOpsPRComments); ids survive prtriage (TestRecordAcceptsThreadCommentIDs); end to end through `dross pr comments` (TestPRCommentsAzureDevOps); live inline comment | t-1, t-2, t-10, t-12 |
| c-2 | TestAzureDevOpsPRHead (active-only Open, forkSource → CrossRepo, refs/heads stripped); cmd refusal on fork/closed PR; live head lookup | t-2, t-10, t-12 |
| c-3 | connectionData at `<ver>-preview`, and its id equals the thread author id (TestAzureDevOpsSelfMatchesThreadAuthor); own ReplyMarker reply dropped through cmd; live id equality | t-2, t-10, t-12 |
| c-4 | Active PRs by targetRefName, client re-check, fork kept, full page and failure → error, not Unsupported | t-7 |
| c-5 | TestADOVerdictTable (builds + Build/Status evaluations; ask on expired/unknown); `ci:` exit codes; ship.md maps none/queued-at-window-end to ask; classic pipelines watched; live verdict | t-3, t-9, t-11, t-12 |
| c-6 | PATCH body pinned to head, squash, deleteSourceBranch false, no bypass or auto-complete keys; waits for completed; conflicts/policy/failure never merged; 0 ref requests; live branch still on origin | t-4, t-9, t-11, t-12 |
| c-7 | Tag-gated live harness asserting every step, plus a live-smoke.toml record kept apart from the mocked suite | t-12 |
| c-8 | ADO ListOpenPRs (IsBot false; rollup = adoVerdict; quiet failure) and watch --json through full remote opts | t-8, t-10 |
| c-9 | TestReviewPromptAzureDiffRecipe (three-dot local diff, non-empty check, post via ship comment) | t-11 |
| c-10 | TestBitbucketCIVerdict (SUCCESSFUL/FAILED/STOPPED/INPROGRESS/none); `ship ci` reaches the bitbucket arm; ship.md polls it like ADO | t-5, t-9, t-11 |
| c-11 | Squash with close_source_branch false, 202 → task-status polled to SUCCESS, a foreign link refused, failures never merged | t-6, t-9, t-11 |

All 11 criteria are covered. Locked decisions are honoured as follows:
- ci_source is pinned by t-3's verdict table and request contract.
- ci_merge_home: t-3, t-4, t-5, t-6 and t-9 build `ship ci` and `ship merge`, unsupported elsewhere, and t-11 makes ship.md own the window.
- blocked_completion is pinned by t-4's no-bypass/no-auto-complete walk, the blocked outcome, and t-11's awaiting-merge.
- thread_listing is pinned by t-2's every-status row.
- transport: every reader goes through adoRequest/bbRequest, and each task has its own off-allowlist row; TestNoGetenvOutsideHostguard auto-scans new files.
- verify_posture: t-12 is tag-gated and recorded separately.

## Judgment calls

- **Comment id.** Chose `<thread>.<comment>` and widened the triage grammar to one shared validator. Rejected packing both into one integer (lossy) and using the comment id alone (ADO restarts it at 1 in each thread). The numeric-only regex exists in two places and would refuse any composite id.
- **Head SHA.** The cmd resolves `git rev-parse origin/phase/<id>` and passes it to both `ship ci` and `ship merge`. A PR whose source commit differs is pending in ci and refused by the ADO merge. Rejected reading the PR's own head, which cannot pin the *watched* SHA. Rejected a `--head` flag, which extends the locked CLI surface.
- **Queued vs running.** Carried as a qualifier on the pending line (`pending (queued)`), and ship.md maps it at the window's end. Rejected a `--window-closed` flag, which would move window semantics into Go when ci_merge_home says ship.md owns the window.
- **Unknown states.** partiallySucceeded, a broken evaluation and unrecognised strings map to ask. Rejected pass (it fails the every-check bar) and fail (the decision lists fail states explicitly).
- **Squash fields.** The squash completion sends both `mergeStrategy: squash` and the older `squashMerge: true`. A Server on api-version 5.0 that ignores mergeStrategy would otherwise merge without squashing.
- **connectionData version.** Requested at `<api_version>-preview`, because the plain version answers 400. Rejected omitting api-version, which adoRequest stamps unconditionally.
- **Comment types.** Skip `system` and `codeChange`, and keep `text` and unrecognised types. Dropping a human comment silently is worse than showing an odd one.
- **Watch scope.** CI is rolled up only for non-fork phase/ and milestone/ PRs, and the rest get ChecksNone. ADO's bot line is always empty, so their checks would never show, and this avoids a 2-read-per-PR fan-out on every tick. ask maps to pending in the rollup. One failing per-PR read fails the whole list, quietly.
- **Dependents and forks.** OpenPRsTargeting keeps fork PRs that target the base, because deleting the base orphans them too.
- **t-7 placement.** t-7 sits in wave 2 behind t-2 only because both must edit TestAzureDeferredSurfacesFailClosed. Rejected same-wave edits of one test function, a pattern the previous plan also avoided.
- **Host-guard rows.** Each task carries its own off-allowlist test in its own new test file. Rejected one shared wave-3 hostguard_test.go task, which would be a tiny file-conflict magnet.
- **Bitbucket headers.** bbRequest gets an optional trailing header out-param so the 202 `Location` is reachable. Rejected a new request builder, which would need ledger and scrub-registry rows and adds a second Basic-auth entry point.
- **Bitbucket head pin.** The Bitbucket merge does not pin a head SHA. Bitbucket has no lastMergeSourceCommit equivalent, and c-11 does not ask for one; ci still checks the head prefix.
- **ship.md field names.** ship.md never names the provider's branch-keep field. TestShipPromptNoUnguardedSwitch already bans `deletesourcebranch`, and the ban is extended to `closesourcebranch` and `bypasspolicy`. The merge recipe is `dross ship merge` alone.
- **Review base.** review.md takes `<base>` from changes.json `base`, which is what `dross ship` sent as targetRefName. checkBaseRetarget refuses completion if the forge's target drifts from it. Rejected adding a CLI just to print the PR target.
- **Live smoke form.** c-7 is a tag-gated Go test that drives the cobra commands and writes a record. Rejected a hand runbook: it gives no per-step assertions and cannot structurally separate live from mocked.
- **Prompts merged.** The review.md and ship.md edits are one task. Alone, the c-9 change is a single bullet plus one test, below the granularity floor. Both are prompt-layer edits whose tests read text only.
