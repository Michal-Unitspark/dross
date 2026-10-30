# Planner draft — risk lens

Bias: failure modes drive the graph. Every task exists to own a set of named ways the ship loop can lie
(a failure read as pass/merged/empty, a token sent off-host, a truncated list read as complete,
mock evidence filed as live). Each failure mode has exactly one owning task and one test that goes red.
Two structural rules follow from that:
- Implementation tasks write unexported provider functions in their own files. Dispatch arms, the
  retirement of TestAzureDeferredSurfacesFailClosed and the shared hostguard table are owned by a
  single wiring task each, so no two tasks in a wave touch the same file.
- JSON decode structs stay function-local (no not_paths.txt rows), as in azure-devops-provider.
  Provider functions take `sha` / `windowClosed` as parameters, so OpenOpts (open.go) is not edited.

```
Phase azure-devops-ship-loop — 18 tasks across 6 waves

Wave 1
  t-1  Read ADO threads and caller identity
       files:    internal/ship/azuredevops_threads.go, internal/ship/azuredevops_threads_test.go,
                 internal/ship/azuredevops_identity.go, internal/ship/azuredevops_identity_test.go
       desc:     azureDevOpsPRComments GETs .../pullRequests/<n>/threads (every thread status). It keeps
                 non-deleted text/codeChange comments and skips system ones (commentType accepted as a string
                 or a number). threadContext.filePath (leading / stripped) plus rightFileStart.line (else
                 leftFileStart.line) makes a comment inline. ID = thread id + the comment id zero-padded to 5
                 digits (digits only). URL = <repo url>/pullrequest/<n>?discussionId=<thread>. Author is
                 {ID lowercased, uniqueName, unknown}. azureDevOpsAuthenticatedUser GETs
                 <api_base>/_apis/connectionData through a per-call preview api-version helper
                 (adoConn.withPreview, defined here). Its id goes through the same adoIdentityID normaliser.
                 Not dispatched yet.
       covers:   c-1, c-3
       depends:  —
       contract: if ids collide across threads, TestADOThreadCommentIDs fails: thread 7/comment 1 and
                 thread 8/comment 1 give 700001 and 800001, every id matches ^[0-9]+$, and a comment id 0 or
                 > 99999 returns (nil, err) rather than a colliding id
       contract: if noise leaks, TestADOThreadsSkipNoise fails: a VoteUpdate thread whose comment has
                 commentType "system" (and numeric 3), a thread with isDeleted true and a comment with
                 isDeleted true contribute 0 comments. "text", 1 and "codeChange" are kept. Threads of status
                 fixed, closed, wontFix, byDesign and pending are all listed
       contract: if anchoring breaks, TestADOThreadInlineAnchor fails: /src/a.go + rightFileStart.line 12
                 gives inline, Path src/a.go, Line 12. leftFileStart-only gives its line. A file-level
                 context gives Path with Line 0. No threadContext gives conversation with empty Path
       contract: if a partial list escapes, a thread with id 0, a non-JSON body, a body without a value
                 array, and a 500 each return (nil, err), never the threads decoded before it
       contract: if the bot class is guessed, a 'Project Collection Build Service' author or one with
                 isContainer true still gives Bot unknown. URL is
                 https://dev.azure.com/o/p/_git/r/pullrequest/7?discussionId=42 (browser form, single-encoded)
       contract: if identity hits the wrong base, TestADOAuthenticatedUserEndpoint fails: api_base
                 https://dev.azure.com/o requests /o/_apis/connectionData (no project segment). Server
                 /tfs/Coll requests /tfs/Coll/_apis/connectionData. Configured 7.1 sends 7.1-preview.1, 6.0
                 sends 6.0-preview.1, and 7.1-preview.1 is not double-suffixed
       contract: if an anonymous answer reads as dross, an authenticatedUser with the anonymous id
                 aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa or with no id returns an error, never Account{}
       contract: if id case diverges, TestADOIdentityMatchesThreadAuthor fails: connectionData id
                 'AB12…' and a thread author id 'ab12…' give equal Account.ID / CommentAuthor.ID. Login is
                 properties.Account.$value (providerDisplayName when absent)

  t-2  Read ADO PR head and dependent PRs
       files:    internal/ship/azuredevops_prread.go, internal/ship/azuredevops_prread_test.go
       desc:     azureDevOpsPRHead GETs pullRequests/<n>. Ref = sourceRefName minus refs/heads/, Open is
                 status == active, CrossRepo is forkSource non-null, URL is the browser form.
                 azureDevOpsOpenPRsTargeting GETs pullrequests with
                 searchCriteria.targetRefName=refs/heads/<base>, status=active and $top=100, then
                 re-checks target and status client-side. Not dispatched yet.
       covers:   c-2, c-4
       depends:  —
       contract: if status maps loosely, TestADOPRHeadOpen fails: active/Active give Open true. completed
                 and abandoned give false. An empty, missing or unknown status ('notSet') returns an error,
                 never PRHead{Open:false}
       contract: if refs/heads/ handling breaks, refs/heads/phase/x gives Ref phase/x and refs/heads/phase/a/b
                 gives phase/a/b. An empty sourceRefName or one outside refs/heads/ (refs/pull/3/merge)
                 returns an error
       contract: if a fork reads as same-repo, forkSource {"name":"refs/heads/phase/x"} gives CrossRepo
                 true. "forkSource": null and an absent key give false. PRNumber 0 sends 0 requests
       contract: if the dependents filter is trusted blindly, TestADOOpenPRsTargeting fails: the mock
                 asserts targetRefName == refs/heads/milestone/v1.8 (a base with & or + round-trips) and
                 status active. A server that ignores the filter and also returns a PR into refs/heads/main
                 and an abandoned one yields only the real dependent, with number, title, browser URL and
                 HeadRefName without refs/heads/
       contract: if a failed lookup reads as "no dependents", a 500, a 203 sign-in page, a body without a
                 value array and a pullRequestId 0 each return (nil, err). A genuine empty value list
                 returns (empty, nil)
       contract: if truncation hides a dependent, a full 100-PR page with no client-side match returns an
                 error. A full page that contains a match returns that match, so the delete still refuses

  t-3  Define CI verdicts and the ADO rollup
       files:    internal/ship/ci.go, internal/ship/azuredevops_ci.go, internal/ship/azuredevops_ci_test.go
       desc:     ci.go holds CIVerdict (pass|fail|pending|none|ask), CIResult{Verdict, Link, Reason, QueuedOnly},
                 ErrCIUnsupported, and closeWindow(r, windowClosed): none → ask, and queued-only pending → ask.
                 azuredevops_ci.go holds the pure adoCIRollup(builds, evals) over untagged observation types.
                 It counts builds plus Build (0609b952-…) and Status (cbdc66da-…) policy evaluations by type
                 id and skips notApplicable. Precedence: fail > running > ask (expired build policy, broken,
                 unknown) > queued > none > pass. No I/O.
       covers:   c-5
       depends:  —
       contract: if a failure is masked, TestADORollupFail fails: a failed, canceled or partiallySucceeded
                 build, or a rejected Build/Status evaluation, gives fail even alongside succeeded, running
                 and queued items, and Link is that item's link
       contract: if a non-CI policy counts, a rejected reviewer (fa4e907d-…), comment (c6a1889d-…) or
                 work-item (40e92b44-…) evaluation alone gives none, and next to a succeeded build gives pass.
                 A displayName 'Build' on a reviewer type id is not counted, and a Build type id with
                 displayName 'Compilación' is counted (classification is by type id, never by name)
       contract: if notApplicable counts, a lone notApplicable evaluation gives none, and notApplicable plus
                 a succeeded build gives pass
       contract: if an unfinished state reads green: running build + approved evaluation gives pending
                 (QueuedOnly false). notStarted/postponed builds or queued evaluations alone give pending
                 (QueuedOnly true). An expired build evaluation (context.isExpired) gives ask even while
                 queued. A broken evaluation or an unknown build result gives ask. Zero counted items gives
                 none. None of these is ever pass
       contract: if the window rule drifts, TestCloseWindow fails: with the window closed, none → ask and
                 pending(QueuedOnly) → ask, while pending(running), pass and fail are unchanged. With the
                 window open every verdict is unchanged
       contract: if precedence is order-dependent, every TestADORollup* row gives the same verdict under
                 every permutation of its items

  t-4  Define merge outcomes and the ADO completion
       files:    internal/ship/merge.go, internal/ship/azuredevops_merge.go, internal/ship/azuredevops_merge_test.go
       desc:     merge.go holds MergeResult{URL}, ErrMergeBlocked and ErrMergeUnsupported. adoCompletePR(opts, sha)
                 first GETs the PR: completed → already merged, 0 writes. abandoned, isDraft, or
                 lastMergeSourceCommit ≠ sha → refuse, 0 writes. Otherwise it sends one PATCH {status
                 completed, lastMergeSourceCommit.commitId sha, completionOptions {mergeStrategy squash,
                 deleteSourceBranch false, transitionWorkItems false}}, then polls on a bounded sleep seam
                 until status == completed.
       covers:   c-6
       depends:  —
       contract: if the body drifts, TestADOCompletePayload fails: exactly one PATCH to .../pullrequests/7,
                 with status completed, lastMergeSourceCommit.commitId == sha, mergeStrategy squash,
                 deleteSourceBranch present and false, transitionWorkItems false, and no bypassPolicy,
                 bypassReason or autoCompleteSetBy key
       contract: if provider-side teardown sneaks in, any DELETE or any request under .../refs fails the
                 test
       contract: if the pin is skipped, a PR whose lastMergeSourceCommit ≠ sha, an abandoned PR and an
                 isDraft PR each error with 0 PATCHes. An already-completed PR returns merged with 0 PATCHes,
                 so a re-run is idempotent
       contract: if a merge preview reads as merged, a poll answering active + mergeStatus succeeded and
                 then completed reports merged only at completed. mergeStatus conflicts errors naming
                 conflicts. failure errors. rejectedByPolicy gives errors.Is(err, ErrMergeBlocked). A PATCH
                 400/409 errors. Polls exhausted while still active error 'completion not confirmed'. None of
                 these returns a MergeResult

  t-5  Expose Bitbucket response headers through one seam
       files:    internal/ship/bitbucket.go, internal/ship/bitbucket_test.go, internal/secretscan/sinks.go,
                 internal/ship/redact_test.go
       desc:     bbDo(method, endpoint, authEnv, user, token, body) returns body, status and headers.
                 bbRequest becomes a thin wrapper over it, so a merge's 202 Location is readable without
                 following it. bbDo is registered in sinks.go and scrubbingHelpers. Screening and
                 scrub-at-source are unchanged.
       covers:   c-11
       depends:  —
       contract: if bbDo is unregistered, TestEveryOutboundSeamIsRegistered,
                 TestScreenPrecedesRequestInEveryScreenedTransport and TestShipHelpersScrubAtSource fail
                 naming bbDo
       contract: if the Location is followed or lost, TestBBDoReturnsLocation fails: a 202 with
                 Location <srv>/2.0/.../merge/task-status/abc hands that header to the caller after exactly 1
                 request
       contract: if screening moves after encoding, a bbDo body holding a ghp_ token returns ErrHit with 0
                 requests. The existing bitbucket open/comment/status/comments tests stay green

  t-6  Teach review.md the ADO diff recipe
       files:    assets/prompts/review.md, internal/cmd/review_prompt_test.go
       desc:     §1 gets an Azure DevOps bullet. base = `base` from `dross changes show <phase-id>` (the
                 branch the PR was opened against). Run `git fetch origin <base> phase/<phase-id>`, then
                 `git diff origin/<base>...origin/phase/<phase-id> > .dross/phases/<phase-id>/review-diff.patch`.
                 The non-empty check and §5's `dross ship comment` stay. make install (r-01).
       covers:   c-9
       depends:  —
       contract: if the recipe drifts, TestReviewPromptAzureDevOpsDiff fails unless the azure-devops bullet
                 has the three-dot origin/<base>...origin/phase/<phase-id> diff (two dots fails), fetches both
                 refs before it, writes review-diff.patch and takes base from `dross changes show`
       contract: if the bullet hand-builds auth, the test fails when it contains curl, `az `, Authorization
                 or $<auth_env>
       contract: if the guard or post path drops, the test fails without 'exists and is non-empty' in §1
                 and `dross ship comment --pr <n> --body-file` in §5

  t-7  Add the live-smoke record and its honesty check
       files:    internal/ship/testdata/ado_live_smoke.toml, internal/ship/ado_live_record_test.go
       desc:     A record template whose TOML comments are the runbook. It holds the 11 c-7 steps:
                 open, dedupe re-run, ship-comment thread, statusless-thread-blocks probe, listing incl.
                 inline, PR head, authenticated user, /dross-respond reply, CI verdict, squash-merge leaving
                 phase/<id>, merged-status. Each starts as result not-run, and posture is "mock-only".
                 The validator is a pure func plus a check of the real file. Its record struct stays
                 test-local.
       covers:   c-7
       depends:  —
       contract: if mock can be filed as live, TestADOLiveRecordHonest fails when posture = "live" while
                 any step is not-run or fail, or lacks evidence (an ADO URL or id), run_at or api_version
       contract: if httptest evidence is passed off as live, a step whose evidence names 127.0.0.1,
                 localhost or an httptest port is rejected
       contract: if a step is dropped, the test fails unless all 11 step keys appear exactly once
       contract: if the thread fallback is ignored, statusless_thread_blocks = true while adoThreadStatus is
                 still "" fails the test (the locked fallback is "closed")
       contract: if the validator is vacuous, its in-test fixtures (live-with-gap, mock-only,
                 complete-live) must give reject, accept-as-mock and accept-as-live respectively

Wave 2 (depends wave 1)
  t-8  Wire ADO read surfaces into dispatch
       files:    internal/ship/prcomments.go, internal/ship/basepr.go, internal/ship/azuredevops_dispatch_test.go,
                 internal/ship/hostguard_test.go
       desc:     ListPRComments, PRHeadOf, AuthenticatedUser and OpenPRsTargeting get azure-devops arms
                 (t-1, t-2). TestAzureDeferredSurfacesFailClosed is retired into a routing test, and the
                 hostguard table gets four ADO rows.
       covers:   c-1, c-2, c-3, c-4
       depends:  t-1, t-2
       contract: if an arm is missing or misrouted, TestAzureDevOpsReadSurfacesRoute fails: with Provider
                 ' Azure-DevOps ', ListPRComments GETs .../pullRequests/7/threads, PRHeadOf GETs
                 .../pullRequests/7, AuthenticatedUser GETs <base>/_apis/connectionData, and
                 OpenPRsTargeting GETs .../pullrequests with searchCriteria.targetRefName
       contract: if any of the four reads the token before the host check, its new
                 TestShipRefusesOffAllowlistAPIBase row (azure-devops comments/head/user/dependents) sees
                 fake-host hits, or errors.Is(err, hostallow.ErrRefused) is false
       contract: if the other providers are disturbed, bitbucket OpenPRsTargeting still returns
                 ErrBasePRLookupUnsupported with 0 requests, and n <= 0 on azure-devops still errors with
                 0 requests

  t-9  Fetch ADO builds and policy evaluations
       files:    internal/ship/azuredevops_ci_fetch.go, internal/ship/azuredevops_ci_fetch_test.go
       desc:     adoCIStatus(opts, sha) reads the PR (repository.id, repository.project.id,
                 lastMergeSourceCommit) and refuses when its head ≠ sha. adoCIForPR(conn, facts) then GETs
                 <project>/_apis/build/builds (repositoryId, repositoryType TfsGit,
                 branchName refs/heads/<source>, queryOrder queueTimeDescending, $top) and
                 <project>/_apis/policy/evaluations?artifactId=vstfs:///CodeReview/CodeReviewId/<projectGUID>/<n>
                 at the preview api-version. It maps both into t-3's observations, with build web links.
                 adoCIForPR is the entry watch reuses.
       covers:   c-5
       depends:  t-1, t-3
       contract: if a stale build counts, TestADOCIBuildsAtHeadOnly fails: a succeeded build at an older
                 sourceVersion and a failed one with another repository.id are ignored (→ none), while a
                 failed build at sha (hex compared case-insensitively) gives fail with its _links.web.href
       contract: if the query is loose, the builds mock asserts branchName == refs/heads/phase/x,
                 repositoryId == the PR's repository.id and repositoryType TfsGit, and that no definition or
                 YAML/process filter is sent, so classic pipelines count. A full $top page errors, never a
                 verdict over a truncated list
       contract: if the artifact id uses the project name, TestADOEvaluationArtifactID fails: artifactId is
                 vstfs:///CodeReview/CodeReviewId/<repository.project.id>/7, escaped once, at api-version
                 7.1-preview.1. A PR body without repository.project.id or repository.id errors with 0
                 build/evaluation requests
       contract: if the watched head drifts, a PR whose lastMergeSourceCommit.commitId ≠ sha errors naming
                 `dross ship <id>`, with 0 build/evaluation requests
       contract: if a failed read becomes a verdict, a 500 or a non-JSON body from builds or evaluations
                 returns (CIResult{}, err). A rejected build-validation evaluation links
                 <api_base>/<project>/_build/results?buildId=<context.buildId>

  t-10 Read Bitbucket commit statuses for ship ci
       files:    internal/ship/bitbucket_ci.go, internal/ship/bitbucket_ci_test.go
       desc:     bitbucketCIStatus(opts, sha) reads the PR, whose source.commit.hash must prefix sha. It then
                 GETs /repositories/<ws>/<slug>/commit/<sha>/statuses?pagelen=100&page=N by page number
                 (never the next URL), up to prThreadMaxPages. Verdicts: SUCCESSFUL everywhere → pass,
                 FAILED/STOPPED → fail with that status's url, INPROGRESS → pending, no statuses → none,
                 an unknown state → ask.
       covers:   c-10
       depends:  t-3
       contract: if mapping slips, TestBitbucketCIVerdicts fails: [SUCCESSFUL,SUCCESSFUL] → pass,
                 [SUCCESSFUL,FAILED] → fail with the FAILED url, STOPPED → fail, [SUCCESSFUL,INPROGRESS] →
                 pending, [] → none, 'QUEUED' → ask, never pass
       contract: if pagination truncates, a FAILED status on page 2 gives fail. Still more after
                 prThreadMaxPages errors. The mock records 0 requests to the body's next URL
       contract: if the head moved, a source.commit.hash that does not prefix sha errors with 0 status
                 requests. A 500 or a non-JSON statuses page errors, never none

  t-11 Squash-merge Bitbucket PRs and await the task
       files:    internal/ship/bitbucket_merge.go, internal/ship/bitbucket_merge_test.go
       desc:     bitbucketMergePR(opts, sha) pre-reads the PR. MERGED → done, 0 POSTs. DECLINED, SUPERSEDED
                 or head ≠ sha → refuse. Otherwise it POSTs .../merge {type pullrequest, merge_strategy
                 squash, close_source_branch false}. A 200 must say MERGED. A 202 is polled through bbDo at
                 its Location, only when that is on api_base's scheme+host and under
                 .../pullrequests/<n>/merge/task-status/. The wait is bounded by a sleep seam.
       covers:   c-11
       depends:  t-4, t-5
       contract: if the body drifts, exactly one POST to .../pullrequests/7/merge carries merge_strategy
                 squash and close_source_branch present and false. Any DELETE or request under
                 .../refs/branches fails the test
       contract: if async is misread, a 202 whose Location is on the api_base host polls PENDING, PENDING,
                 then SUCCESS with merge_result.state MERGED → merged. SUCCESS without MERGED errors. Still
                 PENDING past the bound errors 'merge not confirmed'
       contract: if the poll link can carry the token off-host, a Location on a second httptest server,
                 on another scheme, or on a path outside .../merge/task-status/ errors, and that server
                 records 0 hits
       contract: if a refusal reads as merged, 400 (merge checks), 409 (conflict), 555 and a 202 without
                 Location each error and never return a MergeResult. Already MERGED → merged with 0 POSTs.
                 DECLINED, or a head hash that does not prefix sha → error with 0 POSTs

Wave 3 (depends wave 2)
  t-12 List active ADO PRs with check rollups
       files:    internal/ship/azuredevops_openprs.go, internal/ship/azuredevops_openprs_test.go, internal/ship/openprs.go
       desc:     ListOpenPRs gets an azure-devops arm: GET pullrequests with searchCriteria.status=active and
                 $top=100, mapped to OpenPRRecord (number, title, browser URL, createdBy.uniqueName with
                 IsBot false, creationDate, head minus refs/heads/, IsCrossRepository = forkSource set).
                 Each non-fork PR's Checks come from adoCIForPR at its lastMergeSourceCommit:
                 pass→passing, fail→failing, pending/ask→pending, none→none. Forks get none with no check
                 reads.
       covers:   c-8
       depends:  t-9
       contract: if the head keeps its ref prefix, TestADOOpenPRsHeads fails: refs/heads/phase/x gives
                 HeadRefName phase/x, so watch.SplitPRs classifies it as a ship PR
       contract: if one PR's checks fail to read, the whole call returns (nil, err): a 500 on PR 2's
                 evaluations while PR 1 is fine gives nil, never a list where PR 2 reads none
       contract: if a full page is trusted, exactly 100 PRs errors. A missing value array, pullRequestId 0 or
                 an empty creationDate each error
       contract: if bots or fork costs are guessed, every record has IsBot false (even a 'Build Service'
                 creator), and a forkSource PR gets ChecksNone with 0 builds/evaluations requests
       contract: if the dispatcher widens, forgejo/gitlab/bitbucket still return ErrOpenPRListUnsupported
                 with 0 requests, and an off-allowlist ADO api_base returns ErrRefused with 0 hits

  t-13 Dispatch ship ci and merge per provider
       files:    internal/ship/ci.go, internal/ship/merge.go, internal/ship/cimerge_dispatch_test.go
       desc:     CIStatus(opts, sha, windowClosed) and MergePR(opts, sha) dispatch azure-devops (t-9, t-4)
                 and bitbucket (t-10, t-11). closeWindow is applied once for every provider. CIStatusFunc and
                 MergePRFunc seams are exported. github, gitlab, forgejo and gitea get ErrCIUnsupported /
                 ErrMergeUnsupported ('use the ship.md recipe').
       covers:   c-5, c-6, c-10, c-11
       depends:  t-4, t-9, t-10, t-11
       contract: if an arm is missing, TestCIMergeDispatchRoutes fails: ' Azure-DevOps ' reaches
                 .../_apis/build/builds and PATCHes .../pullrequests/7. ' Bitbucket ' reaches
                 .../commit/<sha>/statuses and .../merge
       contract: if the window rule is provider-specific, bitbucket none and ADO none both become ask with
                 windowClosed true, and stay none with false
       contract: if an unwired provider guesses, github/gitlab/forgejo/gitea return errors.Is
                 ErrCIUnsupported/ErrMergeUnsupported, with text naming the ship.md recipe, 0 requests and no
                 gh spawn. An unknown provider gets a plain unsupported-provider error that is neither sentinel
       contract: if a new entry point skips the host check, off-allowlist rows for ADO ci, ADO merge,
                 Bitbucket ci and Bitbucket merge each return ErrRefused with 0 hits and the env unread

  t-14 Prove ADO read surfaces through cmd
       files:    internal/cmd/pr_azuredevops_test.go, internal/cmd/milestone_dependents_test.go
       desc:     Cmd-level flows run against an httptest ADO remote configured via `dross project set`. They
                 cover dross pr comments / resolve / reply on a phase PR, and the ADO dependents lookup in
                 the milestone branch-delete guard.
       covers:   c-1, c-2, c-3, c-4
       depends:  t-8
       contract: if listing breaks end to end, TestPRCommentsAzureDevOps fails. The threads hold a reviewer
                 conversation comment, a reviewer inline on /src/a.go line 12, a vote system thread, a deleted
                 comment, and dross's own reply (author = the connectionData id in the other case, body
                 starting with prtriage.ReplyMarker). Exactly two items must be listed: the inline one at
                 src/a.go:12, and no own reply
       contract: if ids break triage, `dross pr resolve <n> <listed ADO id> --reject …` is accepted and the
                 saved pr-triage.toml passes prtriage.Validate
       contract: if a fork passes as a phase PR, forkSource set makes dross pr comments refuse, naming the
                 head, with 0 threads requests
       contract: if own-reply detection fails across endpoints, `dross pr reply <n> --post` (with a seeded
                 approval) posts one thread, and the next `dross pr comments` lists nothing new for it
       contract: if the milestone guard degrades wrongly, TestGuardOpenPRsTargetingAzureDevOps fails:
                 an active PR into milestone/v1 refuses, naming #N and its browser URL. A 500 prints the
                 'open-PR check skipped' line (announced, never silent). An off-allowlist api_base returns
                 hostallow.ErrRefused, not the skip

Wave 4 (depends wave 3)
  t-15 Add dross ship ci and dross ship merge
       files:    internal/cmd/ship_cimerge.go, internal/cmd/ship_cimerge_test.go, internal/cmd/ship.go
       desc:     `dross ship ci <phase-id> [--window-closed]` and `dross ship merge <phase-id>`. Both validate
                 the phase id and take the PR number from changes.json. The watched SHA is
                 refs/remotes/origin/phase/<id>, checked as 40 hex and taint-cleared only after that check.
                 They call CIStatusFunc / MergePRFunc with buildOpenOpts. Each verdict gets one exit code; ci
                 prints the failing link and the reason; merge prints the PR URL. Both are registered under
                 Ship().
       covers:   c-5, c-6, c-10, c-11
       depends:  t-13
       contract: if an error reads as a verdict, TestShipCIExitCodes fails: pass exits 0. fail, pending,
                 none and ask exit four distinct codes, none of them 0 or 1. A CIStatusFunc error (500,
                 sign-in page) exits 1 and prints no verdict word
       contract: if the watched SHA is wrong, the stub sees sha == `git rev-parse
                 refs/remotes/origin/phase/<id>` for both ci and merge. A missing origin ref, changes.json pr 0
                 and an invalid phase id ('-x', '../x') each error with 0 seam calls
       contract: if a flag is dropped, --window-closed reaches CIStatusFunc as true and its absence as false.
                 fail prints CIResult.Link and ask prints CIResult.Reason
       contract: if a refused merge reads as merged, MergePRFunc nil exits 0 and prints the URL.
                 ErrMergeBlocked gets its own code (≠0, ≠1) naming awaiting-merge. Any other error exits 1 and
                 never prints merged. On github, both commands exit non-zero and name the ship.md recipe
       contract: if the rev-parse output escapes unvalidated, TestNoSpawnOutputEscapes fails, and a non-hex
                 ref value is refused
       contract: if the token leaks, ADO and Bitbucket mirrorAuthServer 403 runs of ship ci and ship merge
                 leave neither the PAT nor its Basic form in stdout or in the error

  t-16 Show ADO ship PRs in dross watch
       files:    internal/cmd/watch.go, internal/cmd/watch_test.go, assets/prompts/watch.md, internal/cmd/watch_prompt_test.go
       desc:     openPRDigest builds the full OpenOpts (remotePolicy + buildOpenOpts) before calling
                 ListOpenPRsFunc, so ADO reads reach api_base with auth, api_version and hosts. Any failure
                 still gives (nil, nil). watch.md documents azure-devops: ship_prs are listed, and bot_prs is
                 always [] because ADO has no bot flag. make install (r-01).
       covers:   c-8
       depends:  t-8, t-12
       contract: if the digest misses ADO ship PRs, TestWatchAzureDevOpsShipPRs fails. The httptest serves a
                 phase/x PR (approved evaluation, succeeded build at its head), a fork phase/y PR and a
                 feature/z PR, plus one reviewer comment and dross's own reply on phase/x. The result must be
                 ship_prs == [{phase/x, checks passing, untriaged 1}] and bot_prs == []
       contract: if the opts are incomplete, the ListOpenPRsFunc stub sees APIBase, AuthEnv, APIVersion and
                 Hosts for an ADO project
       contract: if a failure prints a list, a 500 on the PR list, a full page and a remotePolicy refusal
                 each leave ship_prs and bot_prs absent from --json and stderr empty. The existing GitHub
                 watch tests stay green
       contract: if the doc drifts, TestWatchPromptAzureDevOps fails unless watch.md says azure-devops lists
                 ship PRs with an always-empty bot_prs, and still says 'Absent when the forge could not be
                 queried'

Wave 5 (depends t-15)
  t-17 Route ship.md §5/§6 through ship ci and merge
       files:    assets/prompts/ship.md, internal/cmd/ship_prompt_test.go, README.md, docs/dross.1
       desc:     §5, azure-devops and bitbucket: poll `dross ship ci <phase-id>` every ~30s and map each exit
                 code. After ~10 min add --window-closed. The skip clause never skips azure-devops for a
                 missing azure-pipelines.yml (classic pipelines). §6: after the re-ship, `dross ship ci` must
                 read pass, then run `dross ship merge <phase-id>`. Blocked → §7 awaiting-merge. Any other
                 non-zero → surface it and never run `dross phase complete`. README and dross.1 list both
                 commands. make install (r-01).
       covers:   c-5, c-6, c-10, c-11
       depends:  t-15
       contract: if the CI gate drifts, TestShipPromptAzureDevOpsCI fails unless §5 names `dross ship ci
                 <phase-id>` for azure-devops and bitbucket, gives all five verdicts with their actions, the
                 ~30s poll, the ~10-minute window and --window-closed, and says none/ask are never a pass
       contract: if classic pipelines get skipped, the test fails unless the skip clause says an
                 azure-devops repo is watched without azure-pipelines.yml
       contract: if a failed merge reaches teardown, TestShipPromptMergeGate fails unless §6 runs `dross ship
                 ci` (pass) immediately before `dross ship merge`, maps the blocked exit to awaiting-merge, and
                 says a non-zero merge never runs `dross phase complete`
       contract: if provider-side deletion creeps back, TestShipPromptNamesCompleteAsTeardownOwner stays
                 green and the new text holds no deleteSourceBranch true, close_source_branch true or
                 bypassPolicy. TestReadmeDocumentsShipCIMerge fails unless README.md and docs/dross.1 name
                 both commands

Wave 6 (depends t-7, t-14, t-16, t-17)
  t-18 Run the live ADO smoke and record it
       files:    internal/ship/testdata/ado_live_smoke.toml, internal/ship/azuredevops_comment.go,
                 internal/ship/azuredevops_comment_test.go
       desc:     After make install, drive the 11 steps against the real org with the installed binary and
                 fill each step's result and evidence. The comment-resolution policy for the probe is set by
                 hand in the org; dross never sets it. If a statusless thread blocks completion, set
                 adoThreadStatus = "closed" and flip TestPostAzureDevOpsThread's status row. A live-step
                 failure becomes a /dross-quick fix plus a re-run, never a mock-only pass.
       covers:   c-7
       depends:  t-7, t-14, t-16, t-17
       contract: if mock is reported as live, TestADOLiveRecordHonest (t-7) passes with posture "live" only
                 when all 11 steps carry pass plus real-host evidence. Any failed step leaves posture
                 "partial"
       contract: if the probe's answer is ignored, statusless_thread_blocks = true with adoThreadStatus ""
                 fails TestADOLiveRecordHonest. Once flipped, TestPostAzureDevOpsThread asserts status
                 closed
       contract: if the merge step's evidence is hollow, the squash-merge step must record a non-empty
                 `git ls-remote origin phase/<id>` after completion and merged-status = merged from
                 GetPRStatus, or the validator rejects the step
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 ListPRComments | t-1, t-8, t-14 |
| c-2 PRHeadOf | t-2, t-8, t-14 |
| c-3 AuthenticatedUser | t-1, t-8, t-14 |
| c-4 OpenPRsTargeting | t-2, t-8, t-14 |
| c-5 ADO CI watch | t-3, t-9, t-13, t-15, t-17 |
| c-6 ADO squash completion | t-4, t-13, t-15, t-17 |
| c-7 live smoke | t-7, t-18 |
| c-8 /dross-watch on ADO | t-12, t-16 (verdict reuse through t-9) |
| c-9 /dross-review diff | t-6 |
| c-10 Bitbucket ship ci | t-10, t-13, t-15, t-17 |
| c-11 Bitbucket ship merge | t-5, t-11, t-13, t-15, t-17 |

11/11 criteria covered. Every task has at least one specific contract.

## Judgment calls

- **ADO comment id.** Chosen: digits only, `<threadId><commentId %05d>` (comment ids are int16, so the encoding is injective, and an out-of-range id errors). Rejected: widening the `[cir][0-9]+` grammar in prtriage/record.go and cmd/pr_resolve.go. Why: no blast radius into other providers' triage records, and t-14 proves the round-trip through `dross pr resolve` and Validate.
- **ADO CI split.** Chosen: a pure rollup (t-3) separate from the wire fetch (t-9). Rejected: one ADO-CI task. Why: precedence and policy classification are permutation-tested without HTTP, and query, pin and artifact-id risks get their own red test.
- **Dispatch ownership.** Chosen: dispatch arms in single wiring tasks (t-8 read surfaces, t-13 ci/merge). Rejected: each implementation task adding its own arm. Why: prcomments.go, the hostguard table and TestAzureDeferredSurfacesFailClosed would otherwise be edited by 4 tasks in one wave, and that test must be retired exactly once.
- **Window boundary.** Chosen: in Go, as `ship ci --window-closed` (none and queued-only → ask) through the shared closeWindow. Rejected: having ship.md parse a "queued" token from output. Why: "stuck queued is never a pass" becomes a unit test instead of prose. The prompt still owns the ~10-minute clock, per the lock.
- **Watched SHA.** Chosen: `refs/remotes/origin/phase/<id>`, cross-checked against the PR's lastMergeSourceCommit in both ci and merge, and pinned by the merge. Rejected: merge re-reading the PR's current head, which would land unwatched pushes. Also rejected: persisting the SHA in state. Why: neither command fetches, so a moved PR makes ADO refuse the pinned completion (fail-closed). §6 requires a pass from ship ci right before merge.
- **Rollup precedence.** Chosen: fail > running > ask > queued > none > pass. Why: never ask the user while a running item could still fail, and an expired policy needs a human regardless of a queued sibling.
- **States the lock leaves open.** partiallySucceeded → fail. broken or unknown → ask. Rejected: pass, or silent pending. Why: neither is "succeeded", and ask is the never-guess bucket.
- **transitionWorkItems:false.** Sent explicitly although the lock is silent. Why: auto-transitioning linked work items at completion would race board finalize, which the azure-boards-backend phase makes dross's job.
- **Preview api-version.** connectionData and policy evaluations get a per-call `<v>-preview.1`. Rejected: the transport's single api-version for every call. Why: evaluations are preview-only, and a mock can't catch that while the live run would. The live run (t-18) is the final proof.
- **Bitbucket 202.** Chosen: a new registered bbDo seam that exposes headers, plus host and path validation of Location. Rejected: following the link through bbRequest. Why: bbRequest sends Basic user:token to any URL it is given.
- **Watch partial failure.** One PR's check read failing makes the whole list (nil, err), so the digest stays quiet. Rejected: marking that PR "none". Why: "none" would claim no CI on a PR whose CI we could not read.
- **Watch mappings.** ask → pending in the watch rollup, because ChecksRollup has no ask, and pending is "not green, not red". Fork PRs get none with no requests (SplitPRs drops them anyway). Rejected: computing their checks.
- **Dependents full page.** Matches are returned (a non-empty list still refuses the delete). A full page with zero matches is an error. Rejected: always erroring. Why: an error falls into the announced-skip path and would let the delete proceed.
- **c-4 "refuses".** guardOpenPRsTargeting's announced skip on lookup error is kept (dependent_detection, locked in an earlier phase). The ADO arm guarantees "never an empty list on failure", and t-14 proves ErrRefused still refuses. Rejected: changing the skip to a refusal for every provider, which is out of this phase's scope. Flagged for the judge.
- **Live record.** Kept in internal/ship/testdata, next to the mocked suite, with a validator that ties `statusless_thread_blocks` to the adoThreadStatus const. Rejected: a free-form note in verify.toml. Why: "mock-verified never reported as live-verified" becomes a red test. The live run itself is necessarily human-driven, because /dross-respond is a slash command.
- **Anonymous connectionData.** The anonymous identity is an error. Rejected: returning it as the account. Why: dross's own replies would show up as reviewer comments and its /dross-review findings would stop splitting.
- **review.md base.** Taken from `dross changes show` (ship rewrites it to the PR's actual target). Rejected: an API read of targetRefName. Why: no CLI exposes it, and a curl recipe would hand-build auth in the transcript against the transport lock. A later retarget is already refused at completion.
- **Out of scope.** No verify-freshness gate inside `ship merge` (ship.md's re-ship owns it), and no configenum CI-provider set. Why: neither is in the spec, and t-13's unsupported rows guard drift.

## Risk owners (index)

| Failure mode | Owner |
|---|---|
| comment ids collide / break triage grammar | t-1 (format), t-14 (round-trip) |
| system/deleted comments leak, inline path keeps leading `/` | t-1 |
| own reply not recognised (id case, anonymous identity, wrong base) | t-1 (normalise), t-14 (end to end) |
| abandoned/completed read as open, fork read as same-repo | t-2 |
| failed or truncated dependents lookup reads as none | t-2; cmd refusal path t-14 |
| new ADO read entry skips the host check | t-8 |
| non-CI policy counted, notApplicable counted, order-dependent verdict | t-3 |
| queued/none read as pass after the window | t-3 (rule), t-13 (applied once) |
| stale-SHA or other-repo build counted; project name in artifactId | t-9 |
| preview-only endpoints rejected live | t-1 / t-9 (mock), t-18 (live) |
| merge lands an unwatched head / deletes source / bypasses policy | t-4 (ADO), t-11 (Bitbucket), t-15 (same SHA) |
| merge preview or a refused merge read as merged | t-4, t-11 |
| task-status link sends Basic auth off-host | t-11 (seam: t-5) |
| transport error exits like a verdict | t-15 |
| watch silently empty (opts, refs/heads/ prefix, partial checks) | t-16, t-12 |
| classic pipelines skipped, failed merge proceeds to teardown | t-17 |
| mock evidence filed as live; thread-status fallback ignored | t-7, t-18 |
