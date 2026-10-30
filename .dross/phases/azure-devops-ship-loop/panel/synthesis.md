# Panel synthesis — azure-devops-ship-loop

Inputs: risk.md (18 tasks / 6 waves), mvp.md (7 / 4), verification.md (12 / 4). I checked the drafts' claims
against the tree before scoring them. Locked decisions (ci_source, ci_merge_home, blocked_completion,
thread_listing, transport, verify_posture) and the hard rule r-01 constrain every choice below.

## Scores

| Draft | Criteria coverage | Test-contract specificity | Granularity | Wave correctness |
|---|---|---|---|---|
| risk | 5/5. All 11 criteria are covered. Every ADO read is proven at unit, dispatch and cmd level, including the c-4 milestone-guard path. c-7 gets a validator that stops mock evidence being filed as live, and watch.md, README and dross.1 stay true | 5/5. The most exact payloads, plus permutation-invariant rollup rows and type-id policy classification. Two weak spots: the id-collision row (7/1 vs 8/1) misses the concatenation case, and t-15 uses mirrorAuthServer, which internal/cmd cannot reach | 4/5. Every task has ≤4 files and ≤2 layers. But 18 tasks is heavy, and t-6 (one prompt bullet plus a test) sits at the floor | 5/5. Every edge is real, including t-9→t-1 for the preview helper. No two tasks in a wave share a file, and shared files and guard tests each have exactly one owner |
| mvp | 3/5. All 11 are mapped, but c-4 is tested only at unit level. c-7's "no pass if mock-substituted" is not a test. It misses the preview api-version for evaluations and the enumScanSites registration | 3/5. Most contracts name a test, but some are loose: a stale head "stops giving pending". The skip-clause contract passes vacuously against today's ship.md, which names neither yml file. It places TestShipHelpersScrubAtSource in internal/cmd, but it is in internal/ship | 2/5. t-1 (5 files) and t-5 (5 files across ship and cmd) break the 5+-file limit. t-2 packs ADO CI, merge and the shared dispatch into one task | 4/5. The edges are real. Putting the prompt task in wave 1 is legitimate because the plan freezes the exit codes and t-4 adds a drift test. Bitbucket waits a wave only for shiploop.go |
| verification | 4/5. All 11 are covered, but c-4 stops at unit level. The c-7 Go harness cannot drive /dross-respond, so it uses `dross pr reply`. It misses the preview api-version for evaluations, and watch.md and README are left stale | 5/5. Very specific. It has the strongest factual grounding: the duplicate regex, the openPRDigest opts gap, the deletesourcebranch ban and enumScanSites all check out. One false claim: that packing the ids is "lossy" | 5/5. Every task has ≤4 files and ≤2 layers | 3/5. review.md is held in wave 4 behind t-9 although it needs nothing. t-7 sits in wave 2 only because it shares a test function (acceptable). The live run (t-12) never waits for the ship.md task, so it cannot exercise the shipped §5/§6 |

**Skeleton: risk.** It is top or tied on coverage and contracts. It is the only draft whose wave graph gives
every shared file and guard test a single owner per wave, and it is the only one that catches the preview-only
evaluations endpoint. Its claims hold up against the tree, with one slip (mirrorAuthServer). Its cost, 18 tasks
across 6 waves, comes from real dependency edges, not padding. Grafts come mostly from verification (sharper
contracts, enumScanSites, the dispatch-set guard, the forbidden-field list, the skip-sentence assertion) and
from mvp (the collision row, the concrete exit-code table and the drift test, the ls-remote check after merge).

Claims checked against the tree:
- `^[cir][0-9]+(#[1-9][0-9]*)?$` appears twice, in internal/prtriage/record.go:77 and internal/cmd/pr_resolve.go:32
  (true, verification). Digits-only packed ids pass it unchanged (risk, mvp).
- `openPRDigest` passes only `{Provider, URL}` to ListOpenPRsFunc (internal/cmd/watch.go:155). True, and all three
  drafts fix it.
- `promptContent` lowercases and strips `_`, backticks and `*`. TestShipPromptNoUnguardedSwitch bans
  `deletesourcebranch`, so ship.md cannot spell `deleteSourceBranch` in any form (true, verification).
- bbRequest returns no headers. It is registered in secretscan/sinks.go:140 and in ship/redact_test.go
  `scrubbingHelpers` (true, risk and verification).
- enumScanSites has no CI or merge rows (true). Only verification registers the new switches.
- guardOpenPRsTargeting refuses on ErrRefused and announces a skip on any other error
  (internal/cmd/milestone_dependents.go:114-125). Its `dependent_detection` lock comes from milestone-stacking
  (true, risk). Verification's contract "not errors.Is(ErrBasePRLookupUnsupported)" separates nothing the guard
  actually reads.
- bitbucketPRComments pages by page number and never follows `next`. risk and verification follow this
  precedent; mvp's "following next pages" breaks it.
- mirrorAuthServer is unexported test code in package ship (redact_test.go:37), so risk t-15 cannot call it from
  internal/cmd as written.
- internal/ship/azuredevops_pr.go already exists, holding the PR status and open-by-source lookups. Verification
  t-7 appends to it; risk adds a separate azuredevops_prread.go.
- `adoThreadStatus = ""` and TestPostAzureDevOpsThread exist, so every draft's fallback flip has something real
  to change.
- `dross changes show` emits `base`, which `dross ship` rewrites to the branch the PR was opened against
  (cmd/ship.go:473). True, and the base of all three review.md recipes.
- ship.md §5's skip sentence today lists only GitHub, Forgejo, Gitea and GitLab CI files, so it would skip an ADO
  or Bitbucket repo. That is why mvp's negative contract is vacuous.
- Two facts are external and cannot be checked in the tree: that ADO policy evaluations are preview-only (only risk
  handles this), and that ADO comment ids are int16, which makes a fixed `%05d` suffix injective.

## Merged plan

Legend: the task tag names the drafts that hold the task. A contract line with no tag comes from the
skeleton (risk). `[graft:x]` marks a line taken from draft x. `[D-n]` marks a line set by a provisional
default from Disagreements.

```
Phase azure-devops-ship-loop — 18 tasks across 6 waves

Wave 1
  t-1  Read ADO threads and caller identity                                     [risk+mvp+verification]
       files:    internal/ship/azuredevops_threads.go (new), internal/ship/azuredevops_threads_test.go (new),
                 internal/ship/azuredevops_identity.go (new), internal/ship/azuredevops_identity_test.go (new)
       desc:     azureDevOpsPRComments GETs .../pullRequests/<n>/threads with no status filter (thread_listing).
                 It keeps non-deleted text and codeChange comments [D-8] and skips system comments (commentType
                 read as a string or a number) and deleted comments or threads. threadContext.filePath (leading /
                 stripped) plus rightFileStart.line (else leftFileStart.line) makes a comment inline; no
                 threadContext makes it conversation. ID = <threadId><commentId %05d>, digits only, so prtriage's
                 grammar is untouched [D-2]. URL = <repo browser url>/pullrequest/<n>?discussionId=<thread>.
                 Author = {id lowercased, uniqueName, Bot unknown}. azureDevOpsAuthenticatedUser GETs
                 <api_base>/_apis/connectionData through adoConn.withPreview, which is defined here and reused by
                 t-9. Its id goes through the same lowercasing normaliser. Decode structs stay function-local (no
                 not_paths.txt rows). Not dispatched yet (t-8).
       covers:   c-1, c-3
       depends:  —
       contract: if ids collide, TestADOThreadCommentIDs fails: thread 1/comment 12 and thread 11/comment 2 must
                 give distinct ids (100012 vs 1100002) [graft:mvp — the concatenation case]. Thread 7/comment 1
                 and thread 8/comment 1 give 700001 and 800001. Every id matches ^[0-9]+$, and a comment id of 0 or
                 > 99999 returns (nil, err)
       contract: if noise leaks, TestADOThreadsSkipNoise fails. These contribute 0 comments: a commentType
                 "system" (and numeric 3) vote, a RefUpdate push and a status change [graft:verification], an
                 isDeleted thread and an isDeleted comment. "text", 1 and "codeChange" are kept. Threads of status
                 fixed, closed, wontFix, byDesign and pending are all listed, and the threads request carries no
                 status filter [graft:verification]
       contract: if anchoring breaks, TestADOThreadInlineAnchor fails: /src/a.go + rightFileStart.line 12 gives
                 inline, Path src/a.go, Line 12. leftFileStart-only gives its line. A file-level context gives
                 Path with Line 0. No threadContext gives conversation with an empty Path
       contract: if a partial list escapes, each of these returns (nil, err), never the threads decoded before
                 it: a thread with id 0, a non-JSON body, a body without a value array, a 500, and a 203 sign-in
                 page [graft:mvp+verification]
       contract: if the bot class is guessed, a 'Project Collection Build Service' author or an author with
                 isContainer true still gives Bot unknown. URL is
                 https://dev.azure.com/o/p/_git/r/pullrequest/7?discussionId=42 (browser form, single-encoded)
       contract: if identity hits the wrong base, TestADOAuthenticatedUserEndpoint fails: api_base
                 https://dev.azure.com/o requests /o/_apis/connectionData (no project segment), and Server
                 /tfs/Coll requests /tfs/Coll/_apis/connectionData. Configured 7.1 sends 7.1-preview.1, 6.0 sends
                 6.0-preview.1, and 7.1-preview.1 is not double-suffixed
       contract: if an anonymous answer reads as dross, an authenticatedUser with the anonymous id
                 aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa or with no id returns an error, never Account{}
       contract: if id case diverges, TestADOIdentityMatchesThreadAuthor fails: connectionData id 'AB12…' and
                 thread author id 'ab12…' must give equal Account.ID and CommentAuthor.ID (prtriage.Items
                 compares them with ==). Login is properties.Account.$value, or providerDisplayName when absent

  t-2  Read ADO PR head and dependent PRs                                       [risk+verification]
       files:    internal/ship/azuredevops_prread.go (new), internal/ship/azuredevops_prread_test.go (new)
       desc:     azureDevOpsPRHead GETs pullRequests/<n>. Ref = sourceRefName minus refs/heads/, Open = status
                 active, CrossRepo = forkSource non-null, URL = the browser form. azureDevOpsOpenPRsTargeting GETs
                 pullrequests with searchCriteria.targetRefName=refs/heads/<base>, searchCriteria.status=active and
                 $top=100, then re-checks target and status client-side. Fork PRs that target the base stay in,
                 because deleting the base orphans them too [graft:verification]. A full page with a match returns
                 the match; a full page with none is an error [D-7]. The task adds a new file and leaves the
                 existing azuredevops_pr.go alone. Not dispatched yet (t-8).
       covers:   c-2, c-4
       depends:  —
       contract: if status maps loosely, TestADOPRHeadOpen fails: active and Active give Open true; completed and
                 abandoned give false. An empty, missing or unknown status ('notSet') returns an error, never
                 PRHead{Open:false}
       contract: if refs/heads/ handling breaks, refs/heads/phase/x gives Ref phase/x and refs/heads/phase/a/b
                 gives phase/a/b. An empty sourceRefName, or one outside refs/heads/ (refs/pull/3/merge), returns
                 an error
       contract: if a fork reads as same-repo, forkSource {"name":"refs/heads/phase/x"} gives CrossRepo true,
                 while "forkSource": null and an absent key give false. PRNumber 0 sends 0 requests
       contract: if the dependents filter is trusted blindly, TestADOOpenPRsTargeting fails. The mock asserts
                 targetRefName == refs/heads/milestone/v1.8 (a base with & or + round-trips) and status active. A
                 server that ignores the filter and also returns a PR into refs/heads/main and an abandoned PR
                 must yield only the real dependent, with number, title, browser URL and HeadRefName without
                 refs/heads/. A fork PR targeting the base is still listed [graft:verification]. An empty base
                 errors with 0 requests [graft:verification]
       contract: if a failed lookup reads as "no dependents", each of these returns (nil, err): a 500, a 203
                 sign-in page, a non-JSON body [graft:verification], a body without a value array, and a
                 pullRequestId 0. A genuine empty value list returns (empty, nil)
       contract: if truncation hides a dependent [D-7], a full 100-PR page with no client-side match returns an
                 error. A full page that contains a match returns that match, so the delete still refuses

  t-3  Define CI verdicts and the ADO rollup                                    [risk+verification]
       files:    internal/ship/ci.go (new), internal/ship/azuredevops_ci.go (new), internal/ship/azuredevops_ci_test.go (new)
       desc:     ci.go holds CIVerdict (pass|fail|pending|none|ask), CIResult{Verdict, Link, Reason, Queued} and
                 ErrCIUnsupported. Go holds no window logic [D-1]; risk's closeWindow and TestCloseWindow are
                 dropped. Queued marks a pending made only of queued items, so ship.md can spot "queued at window
                 end". azuredevops_ci.go holds the pure adoCIRollup(builds, evals) over untagged observation types.
                 It counts builds plus Build (0609b952-…) and Status (cbdc66da-…) policy evaluations, blocking and
                 optional, classified by type id; notApplicable is skipped. Precedence: fail > running > ask
                 (expired build policy, broken, unknown) > queued > none > pass. partiallySucceeded gives fail
                 [D-4]. No I/O.
       covers:   c-5
       depends:  —
       contract: if a failure is masked, TestADORollupFail fails: a failed, canceled or partiallySucceeded build,
                 or a rejected Build/Status evaluation, gives fail even alongside succeeded, running and queued
                 items, with Link set to that item's link. A rejected optional (isBlocking=false) Build evaluation
                 still fails [graft:verification]
       contract: if a non-CI policy counts, a rejected Required reviewers (fd2167ab-…) [graft:verification],
                 Minimum reviewers (fa4e907d-…), Comment requirements (c6a1889d-…) or Work item linking
                 (40e92b44-…) evaluation alone gives none, and next to a succeeded build gives pass. A displayName
                 'Build' on a reviewer type id is not counted, and a Build type id with displayName 'Compilación'
                 is counted
       contract: if notApplicable counts, a lone notApplicable evaluation gives none, and notApplicable plus a
                 succeeded build gives pass
       contract: if an unfinished state reads green: a running build + approved evaluation gives pending (Queued
                 false), and queued + inProgress gives pending (Queued false) [graft:verification]. notStarted or
                 postponed builds, or queued evaluations, alone give pending (Queued true). An expired build
                 evaluation (context.isExpired) gives ask even while queued. A broken evaluation or an unknown
                 build result gives ask. Zero counted items gives none. None of these is ever pass
       contract: if precedence depends on order, every TestADORollup* row gives the same verdict under every
                 permutation of its items

  t-4  Define merge outcomes and the ADO completion                             [risk+verification+mvp]
       files:    internal/ship/merge.go (new), internal/ship/azuredevops_merge.go (new),
                 internal/ship/azuredevops_merge_test.go (new)
       desc:     merge.go holds MergeResult{URL}, ErrMergeBlocked and ErrMergeUnsupported [D-5].
                 adoCompletePR(opts, sha) first GETs the PR. Completed means already merged, with 0 writes.
                 Abandoned, isDraft, or lastMergeSourceCommit ≠ sha means refuse, with 0 writes. Otherwise it
                 sends one PATCH {status completed, lastMergeSourceCommit.commitId sha, completionOptions
                 {mergeStrategy squash, squashMerge true [graft:verification], deleteSourceBranch false,
                 transitionWorkItems false}}. It then polls until status == completed, on a test-lowerable
                 interval with a bound of ≤ ~90s so one Bash call never outlives its timeout [graft:verification].
       covers:   c-6
       depends:  —
       contract: if the body drifts, TestADOCompletePayload fails: exactly one PATCH to .../pullrequests/7, with
                 status completed, lastMergeSourceCommit.commitId == sha, mergeStrategy squash, squashMerge true,
                 deleteSourceBranch present and false, and transitionWorkItems false. A recursive JSON walk finds no
                 bypassPolicy, bypassReason or autoCompleteSetBy key at any depth [graft:verification]
       contract: if provider-side teardown sneaks in, any DELETE or any request under .../refs fails the test
       contract: if the pin is skipped, each of these errors with 0 PATCHes: lastMergeSourceCommit bbb with sha
                 aaa (the error names both) [graft:verification], an abandoned PR, and an isDraft PR. An
                 already-completed PR returns merged with 0 PATCHes, so a re-run is idempotent
       contract: if a merge preview reads as merged, GETs answering active+queued, active+succeeded, then
                 completed report merged only at completed, after 3 polls [graft:verification]. mergeStatus
                 conflicts errors naming conflicts, and failure errors. rejectedByPolicy gives errors.Is(err,
                 ErrMergeBlocked). A PATCH 400/409 errors with ADO's scrubbed message and 0 polls
                 [graft:verification]. Polls exhausted while still active error 'completion not confirmed — re-run
                 dross ship merge'. None of these returns a MergeResult

  t-5  Expose Bitbucket response headers through one seam                       [risk]
       files:    internal/ship/bitbucket.go, internal/ship/bitbucket_test.go, internal/secretscan/sinks.go,
                 internal/ship/redact_test.go
       desc:     bbDo(method, endpoint, authEnv, user, token, body) returns body, status and headers. bbRequest
                 becomes a thin wrapper over it, so a merge's 202 Location is readable without being followed
                 [D-9]. bbDo is registered in sinks.go and scrubbingHelpers. bbRequest's existing sinks row
                 (sinks.go:140) must still hold. Screening before encoding, and scrubbing at the source, are
                 unchanged.
       covers:   c-11
       depends:  —
       contract: if bbDo is unregistered, TestEveryOutboundSeamIsRegistered and
                 TestScreenPrecedesRequestInEveryScreenedTransport (internal/cmd) and TestShipHelpersScrubAtSource
                 (internal/ship) fail naming bbDo
       contract: if the Location is followed or lost, TestBBDoReturnsLocation fails: a 202 with Location
                 <srv>/2.0/.../merge/task-status/abc hands that header to the caller after exactly 1 request
       contract: if screening moves after encoding, a bbDo body holding a ghp_ token returns ErrHit with 0
                 requests. The existing bitbucket open/comment/status/comments tests stay green

  t-6  Teach review.md the ADO diff recipe                                      [risk+mvp+verification]
       files:    assets/prompts/review.md, internal/cmd/review_prompt_test.go (new)
       desc:     §1 gets an Azure DevOps bullet. base = `base` from `dross changes show <phase-id>`, which ship
                 rewrites to the branch the PR was opened against. The bullet runs `git fetch origin <base>
                 phase/<phase-id>`, then `git diff origin/<base>...origin/phase/<phase-id> >
                 .dross/phases/<phase-id>/review-diff.patch`. The non-empty check and §5's `dross ship comment`
                 stay. Kept as its own wave-1 task [D-13]. Run make install (r-01).
       covers:   c-9
       depends:  —
       contract: if the recipe drifts, TestReviewPromptAzureDevOpsDiff fails unless the azure-devops bullet has
                 the three-dot origin/<base>...origin/phase/<phase-id> diff (two dots fails), fetches both refs
                 before it, writes review-diff.patch and takes base from `dross changes show`
       contract: if the bullet hand-builds auth, the test fails when the bullet contains curl, `az `,
                 Authorization or $<auth_env>
       contract: if the guard or the post path drops, the test fails without 'exists and is non-empty' in §1
                 and `dross ship comment --pr <n> --body-file` in §5

  t-7  Add the live-smoke record and its honesty check                          [risk+mvp]
       files:    internal/ship/testdata/ado_live_smoke.toml (new), internal/ship/ado_live_record_test.go (new)
       desc:     A record template whose TOML comments are the runbook [D-11]. It holds the 11 c-7 steps: open,
                 dedupe re-run, ship-comment thread, statusless-thread-blocks probe, listing incl. inline, PR
                 head, authenticated user, /dross-respond reply, CI verdict, squash-merge leaving phase/<id>, and
                 merged-status. Each step starts as result not-run, and posture starts as "mock-only". The record
                 backs c-7 only and is never cited for c-1..c-6 or c-8..c-11 (verify_posture) [graft:mvp]. The
                 validator is a pure function plus a check of the real file, and its record struct stays
                 test-local.
       covers:   c-7
       depends:  —
       contract: if mock can be filed as live, TestADOLiveRecordHonest fails when posture = "live" while any step
                 is not-run or fail, or lacks evidence (an ADO URL or id), run_at or api_version
       contract: if httptest evidence is passed off as live, a step whose evidence names 127.0.0.1, localhost or
                 an httptest port is rejected
       contract: if a step is dropped, the test fails unless all 11 step keys appear exactly once
       contract: if the thread fallback is ignored, statusless_thread_blocks = true while adoThreadStatus is still
                 "" fails the test (the locked fallback is "closed")
       contract: if the validator is vacuous, its in-test fixtures live-with-gap, mock-only and complete-live
                 must give reject, accept-as-mock and accept-as-live respectively

Wave 2 (depends wave 1)
  t-8  Wire ADO read surfaces into dispatch                                     [risk]
       files:    internal/ship/prcomments.go, internal/ship/basepr.go, internal/ship/azuredevops_dispatch_test.go,
                 internal/ship/hostguard_test.go
       desc:     ListPRComments, PRHeadOf, AuthenticatedUser and OpenPRsTargeting get azure-devops arms (t-1,
                 t-2). TestAzureDeferredSurfacesFailClosed is retired, in one commit, into a routing test, and the
                 hostguard table gets four ADO rows [D-14].
       covers:   c-1, c-2, c-3, c-4
       depends:  t-1, t-2
       contract: if an arm is missing or misrouted, TestAzureDevOpsReadSurfacesRoute fails. With Provider
                 ' Azure-DevOps ': ListPRComments GETs .../pullRequests/7/threads, PRHeadOf GETs
                 .../pullRequests/7, AuthenticatedUser GETs <base>/_apis/connectionData, and OpenPRsTargeting GETs
                 .../pullrequests with searchCriteria.targetRefName
       contract: if any of the four reads the token before the host check, its new
                 TestShipRefusesOffAllowlistAPIBase row (azure-devops comments/head/user/dependents) sees fake-host
                 hits, or errors.Is(err, hostallow.ErrRefused) is false
       contract: if the other providers are disturbed, bitbucket OpenPRsTargeting still returns
                 ErrBasePRLookupUnsupported with 0 requests, and n <= 0 on azure-devops still errors with 0
                 requests

  t-9  Fetch ADO builds and policy evaluations                                  [risk+verification+mvp]
       files:    internal/ship/azuredevops_ci_fetch.go (new), internal/ship/azuredevops_ci_fetch_test.go (new)
       desc:     adoCIStatus(opts, sha) reads the PR (repository.id, repository.project.id, sourceRefName,
                 lastMergeSourceCommit). If lastMergeSourceCommit is not sha, or not yet set, it returns pending
                 with a Reason naming both SHAs and `dross ship <id>`, and reads no CI [D-3]. Otherwise
                 adoCIForPR(conn, facts) GETs <project>/_apis/build/builds (repositoryId, repositoryType TfsGit,
                 branchName refs/heads/<source>, queryOrder queueTimeDescending, $top). It also GETs
                 <project>/_apis/policy/evaluations?artifactId=vstfs:///CodeReview/CodeReviewId/<projectGUID>/<n>
                 at the preview api-version through t-1's withPreview, because evaluations are preview-only. Both
                 map into t-3's observations, with build web links. adoCIForPR is the entry point t-12 reuses.
       covers:   c-5
       depends:  t-1, t-3
       contract: if a stale build counts, TestADOCIBuildsAtHeadOnly fails: a succeeded build at an older
                 sourceVersion and a failed one with another repository.id are ignored (→ none). A failed build at
                 sha (hex compared case-insensitively) gives fail with its _links.web.href
       contract: if the query is loose, the builds mock asserts branchName == refs/heads/phase/x, repositoryId ==
                 the PR's repository.id and repositoryType TfsGit. It also asserts that no definition or
                 YAML/process filter is sent, so classic pipelines count. A full $top page errors, never a verdict
                 over a truncated list. No request path contains /_apis/policy/configurations
                 [graft:mvp+verification]
       contract: if the artifact id uses the project name, TestADOEvaluationArtifactID fails: artifactId is
                 vstfs:///CodeReview/CodeReviewId/<repository.project.id>/7, escaped once, at api-version
                 7.1-preview.1. A PR body without repository.project.id or repository.id errors with 0
                 build/evaluation requests
       contract: if a stale head reads green [D-3], a PR whose lastMergeSourceCommit.commitId ≠ sha, or is
                 absent, gives pending with a Reason naming both SHAs and 0 build/evaluation requests. Never pass,
                 never none [graft:verification]
       contract: if a failed read becomes a verdict, a 500, a 203 sign-in page [graft:mvp+verification] or a
                 non-JSON body from the PR, builds or evaluations read returns (CIResult{}, err). A rejected
                 build-validation evaluation links <api_base>/<project>/_build/results?buildId=<context.buildId>

  t-10 Read Bitbucket commit statuses for ship ci                               [risk+verification+mvp]
       files:    internal/ship/bitbucket_ci.go (new), internal/ship/bitbucket_ci_test.go (new)
       desc:     bitbucketCIStatus(opts, sha) reads the PR. If source.commit.hash does not prefix sha, it gives
                 pending with a Reason and sends 0 status requests [D-3]. Otherwise it GETs
                 /repositories/<ws>/<slug>/commit/<sha>/statuses?pagelen=100&page=N by page number, up to
                 prThreadMaxPages, and never follows the next URL (bitbucketPRComments' precedent). SUCCESSFUL
                 everywhere → pass. FAILED/STOPPED → fail with that status's url. INPROGRESS → pending. No statuses
                 → none. An unknown state → ask.
       covers:   c-10
       depends:  t-3
       contract: if mapping slips, TestBitbucketCIVerdicts fails: [SUCCESSFUL,SUCCESSFUL] → pass;
                 [SUCCESSFUL,FAILED] → fail with the FAILED url; STOPPED → fail; [SUCCESSFUL,INPROGRESS] →
                 pending; [FAILED,INPROGRESS] → fail [graft:mvp+verification]; [] → none; 'QUEUED' → ask, never pass
       contract: if pagination truncates, a FAILED status on page 2 gives fail, and still more after
                 prThreadMaxPages errors. The mock records 0 requests to the body's next URL
       contract: if the head moved [D-3], a source.commit.hash that does not prefix sha gives pending with 0
                 status requests. A 500 or a non-JSON PR or statuses page errors, never none. A missing auth_user
                 errors with 0 hits [graft:verification]

  t-11 Squash-merge Bitbucket PRs and await the task                            [risk+verification]
       files:    internal/ship/bitbucket_merge.go (new), internal/ship/bitbucket_merge_test.go (new)
       desc:     bitbucketMergePR(opts, sha) pre-reads the PR [D-10]. MERGED → done, with 0 POSTs. DECLINED,
                 SUPERSEDED, or a head hash that does not prefix sha → refuse. Otherwise it POSTs .../merge {type
                 pullrequest, merge_strategy squash, close_source_branch false}. A 200 must say MERGED. A 202 is
                 polled through bbDo at its Location, but only when that Location is on api_base's scheme+host and
                 under .../pullrequests/<n>/merge/task-status/. The wait is bounded by a test-lowerable sleep seam
                 (≤ ~90s) [graft:verification].
       covers:   c-11
       depends:  t-4, t-5
       contract: if the body drifts, exactly one POST to .../pullrequests/7/merge carries merge_strategy squash
                 and close_source_branch present and false. Any DELETE or request under .../refs/branches fails
                 the test
       contract: if async is misread: a 200 with state MERGED → merged with 0 task-status polls
                 [graft:verification]. A 202 whose Location is on the api_base host polls PENDING, PENDING, then
                 SUCCESS with merge_result.state MERGED → merged. SUCCESS without MERGED errors. Still PENDING
                 past the bound errors 'merge not confirmed'
       contract: if the poll link can carry the token off-host, a Location on a second httptest server, on
                 another scheme, on another PR's path [graft:verification], or outside .../merge/task-status/
                 errors, and that server records 0 hits
       contract: if a refusal reads as merged, each of these errors and never returns a MergeResult: 400 (merge
                 checks), 409 (conflict), 555, a 202 without Location, and a task-status with an error or an
                 unrecognised state [graft:verification]. Already MERGED → merged with 0 POSTs. DECLINED, or a
                 head hash that does not prefix sha → error with 0 POSTs

Wave 3 (depends wave 2)
  t-12 List active ADO PRs with check rollups                                   [risk+verification+mvp]
       files:    internal/ship/azuredevops_openprs.go (new), internal/ship/azuredevops_openprs_test.go (new),
                 internal/ship/openprs.go
       desc:     ListOpenPRs gets an azure-devops arm; today it is a bare `!= "github"` check. It GETs
                 pullrequests with searchCriteria.status=active and $top=100 and maps each PR to OpenPRRecord:
                 number, title, browser URL, createdBy.uniqueName with IsBot false, creationDate, head minus
                 refs/heads/, and IsCrossRepository = forkSource set. Checks come from adoCIForPR at
                 lastMergeSourceCommit, but only for non-fork PRs whose head starts phase/ or milestone/ (watch's
                 shipHeadPrefixes). Every other PR gets ChecksNone with no CI read [D-6]. pass→passing,
                 fail→failing, pending/ask→pending, none→none.
       covers:   c-8
       depends:  t-9
       contract: if the head keeps its ref prefix, TestADOOpenPRsHeads fails: refs/heads/phase/x gives
                 HeadRefName phase/x, so watch.SplitPRs classifies it as a ship PR
       contract: if the rollup diverges from ship ci, TestADOWatchChecksMatchShipCI fails [graft:verification]:
                 for every t-3 rollup fixture, pass/fail/pending/none give passing/failing/pending/none, and ask
                 gives pending, never passing
       contract: if one PR's checks fail to read, the whole call returns (nil, err): a 500 on PR 2's evaluations
                 while PR 1 is fine gives nil, never a list where PR 2 reads none
       contract: if a full page is trusted, exactly 100 PRs errors. A missing value array, pullRequestId 0, an
                 empty creationDate or a 203 sign-in page [graft:verification] each error
       contract: if bots or fan-out costs are guessed [D-6], every record has IsBot false, even a 'Build Service'
                 creator. A forkSource phase/y PR and a feature/z PR get ChecksNone with 0 builds/evaluations
                 requests [graft:verification]
       contract: if the dispatcher widens, forgejo/gitlab/bitbucket still return ErrOpenPRListUnsupported with 0
                 requests, and an off-allowlist ADO api_base returns ErrRefused with 0 hits

  t-13 Dispatch ship ci and merge per provider                                  [risk+verification]
       files:    internal/ship/ci.go, internal/ship/merge.go, internal/ship/cimerge_dispatch_test.go (new),
                 internal/cmd/enum_divergence_test.go
       desc:     CIStatus(opts, sha) and MergePR(opts, sha) dispatch through configenum.Normalize: azure-devops to
                 t-9 and t-4, bitbucket to t-10 and t-11. The CIStatusFunc and MergePRFunc seams are exported.
                 github, gitlab, forgejo and gitea get ErrCIUnsupported / ErrMergeUnsupported ('use the ship.md
                 recipe'), and an unknown provider gets a plain error. No window parameter [D-1]. Both switches
                 are registered in enumScanSites [graft:verification].
       covers:   c-5, c-6, c-10, c-11
       depends:  t-4, t-9, t-10, t-11
       contract: if an arm is missing, TestCIMergeDispatchRoutes fails: ' Azure-DevOps ' reaches
                 .../_apis/build/builds and PATCHes .../pullrequests/7, and ' Bitbucket ' reaches
                 .../commit/<sha>/statuses and .../merge
       contract: if an unwired provider guesses, github/gitlab/forgejo/gitea return errors.Is
                 ErrCIUnsupported/ErrMergeUnsupported, with text naming the ship.md recipe, 0 requests and no gh
                 spawn. An unknown provider gets an error that is neither sentinel
       contract: if a new entry point skips the host check, off-allowlist rows for ADO ci, ADO merge, Bitbucket ci
                 and Bitbucket merge each return ErrRefused with 0 hits and the env unread
       contract: if provider coverage drifts, TestCIMergeDispatchSet fails [graft:verification]: the implemented
                 arms of ship.CIStatus and ship.MergePR are exactly {azure-devops, bitbucket}, a subset of
                 ShipProviders. The new enumScanSites rows feed TestDispatchUsesConfigenumNormalize and
                 TestGuardsSeeNonEmptySets, so dropping Normalize or refactoring the switch away goes red

  t-14 Prove ADO read surfaces through cmd                                      [risk+verification]
       files:    internal/cmd/pr_azuredevops_test.go (new), internal/cmd/milestone_dependents_test.go
       desc:     Cmd-level flows run against an httptest ADO remote (newADOShipMock, cmd/ship_test.go) configured
                 via `dross project set`. They cover dross pr comments / resolve / reply on a phase PR, and the ADO
                 dependents lookup inside guardOpenPRsTargeting [D-15].
       covers:   c-1, c-2, c-3, c-4
       depends:  t-8
       contract: if listing breaks end to end, TestPRCommentsAzureDevOps fails. The threads hold a reviewer
                 conversation comment, a reviewer inline comment on /src/a.go line 12, a vote system thread, a
                 deleted comment, and dross's own reply (author = the connectionData id in the other case, body
                 starting with prtriage.ReplyMarker). Exactly two items must be listed, the inline one at
                 src/a.go:12, and no own reply. A reviewer comment that copies ReplyMarker is still listed, since
                 "own" is decided by author id, not by the marker. dross's own /dross-review comment splits into
                 one item per finding, c<id>#1..n [graft:verification]
       contract: if ids break triage, `dross pr resolve <n> <listed ADO id> --reject …` is accepted and the saved
                 pr-triage.toml passes prtriage.Validate
       contract: if a fork or a closed PR passes as a phase PR, forkSource set, and a completed PR
                 [graft:verification], each make dross pr comments refuse with 0 threads requests
       contract: if own-reply detection fails across endpoints, `dross pr reply <n> --post` (with a seeded
                 approval) posts one thread whose first line is ReplyMarker, and the next `dross pr comments`
                 lists nothing new for it
       contract: if the milestone guard degrades wrongly, TestGuardOpenPRsTargetingAzureDevOps fails. An active
                 PR into milestone/v1 refuses, naming #N and its browser URL. A full page holding that PR still
                 refuses [D-7]. A 500 prints the 'open-PR check skipped' line, announced and never silent. An
                 off-allowlist api_base returns hostallow.ErrRefused, not the skip

Wave 4 (depends wave 3)
  t-15 Add dross ship ci and dross ship merge                                   [risk+mvp+verification]
       files:    internal/cmd/ship_cimerge.go (new), internal/cmd/ship_cimerge_test.go (new), internal/cmd/ship.go
       desc:     `dross ship ci <phase-id>` and `dross ship merge <phase-id>`, with no flags [D-1]. Both validate
                 the phase id and take the PR number from changes.json. The watched SHA is
                 refs/remotes/origin/phase/<id>, read via gitRefArgs, checked as 40 hex and taint-cleared only after
                 that check. Both call CIStatusFunc / MergePRFunc with remotePolicy + buildOpenOpts. ci prints one
                 line: `ci: pass`, `ci: fail — <link>`, `ci: pending (queued)` or `ci: pending (running)`, `ci:
                 none`, or `ci: ask — <reason>`. Its exit codes are pass 0, fail 2, pending 3, none 4, ask 5, and
                 lookup error 1 [graft:mvp codes, verification lines]. merge exits 0 and prints the PR URL.
                 ErrMergeBlocked gets its own code naming awaiting-merge, and any other error exits 1 [D-5]. Both
                 commands are registered under Ship().
       covers:   c-5, c-6, c-10, c-11
       depends:  t-13
       contract: if an error reads as a verdict, TestShipCIExitCodes fails: stubbed pass/fail/pending/none/ask
                 must exit 0/2/3/4/5. fail prints CIResult.Link, ask prints CIResult.Reason, and pending prints
                 queued vs running from CIResult.Queued. A CIStatusFunc error (500, sign-in page) exits 1 and prints
                 no `ci:` line
       contract: if the watched SHA is wrong, the stub must see sha == `git rev-parse
                 refs/remotes/origin/phase/<id>` for both ci and merge. A missing origin ref, an invalid phase id
                 ('-x', '../x') and changes.json pr 0 each error with 0 seam calls; the last names `dross ship
                 <id>` [graft:mvp+verification]
       contract: if a refused merge reads as merged, MergePRFunc nil must exit 0 and print the URL.
                 ErrMergeBlocked exits with its own code (≠0, ≠1) naming awaiting-merge. Any other error exits 1
                 and never prints merged. On github both commands exit non-zero naming the ship.md recipe, with 0
                 HTTP requests
       contract: if wiring breaks end to end, TestShipCIAzureDevOpsFlow fails [graft:verification]. Using
                 newADOShipMock and a fixture repo with no azure-pipelines.yml, a succeeded build at S plus an
                 approved evaluation must exit 0, and the builds query must carry branchName=refs/heads/phase/x
       contract: if merge is not pinned or tears down, TestShipMergeAzureDevOpsPinsHead fails [graft:mvp]: the
                 PATCH's lastMergeSourceCommit.commitId must equal rev-parse origin/phase/x with
                 deleteSourceBranch false, and after exit 0 `git ls-remote origin phase/x` still lists the branch
       contract: if the rev-parse output escapes unvalidated, TestNoSpawnOutputEscapes fails and a non-hex ref
                 value is refused. If the positional is unseparated, TestNoUnseparatedGitPositional fails
                 [graft:mvp]
       contract: if the token leaks, ADO and Bitbucket 403 mirror runs of ship ci and ship merge leave neither
                 the PAT nor its Basic form in stdout or the error. This needs a cmd-local mirror server, because
                 ship's mirrorAuthServer is unexported test code in package ship (fact-check fix)

  t-16 Show ADO ship PRs in dross watch                                         [risk+mvp+verification]
       files:    internal/cmd/watch.go, internal/cmd/watch_test.go, assets/prompts/watch.md,
                 internal/cmd/watch_prompt_test.go
       desc:     openPRDigest builds the full OpenOpts (remotePolicy + buildOpenOpts) before calling
                 ListOpenPRsFunc. Today it passes only {Provider, URL}, so an ADO listing would have no api_base,
                 auth or hosts. Any failure still gives (nil, nil). watch.md documents azure-devops: ship_prs are
                 listed, and bot_prs is always [] because ADO has no bot flag. The "a provider other than GitHub"
                 absence clause and the "no dross verb merges a PR" line are reworded so they stay true once
                 `dross ship merge` exists [D-12; staleness found by mvp]. make install (r-01).
       covers:   c-8
       depends:  t-8, t-12
       contract: if the digest misses ADO ship PRs, TestWatchAzureDevOpsShipPRs fails. The httptest serves a
                 phase/x PR (approved evaluation, succeeded build at its head), a fork phase/y PR and a feature/z
                 PR, plus one reviewer comment and dross's own reply on phase/x. The result must be ship_prs ==
                 [{phase/x, checks passing, untriaged 1}] and bot_prs == []
       contract: if the opts are incomplete, the ListOpenPRsFunc stub sees APIBase, AuthEnv, APIVersion and Hosts
                 for an ADO project
       contract: if a failure prints a list, each of these leaves ship_prs and bot_prs absent from --json, exits 0
                 [graft:mvp] and leaves stderr empty: a 500 on the PR list, a 203 sign-in page [graft:mvp], a full
                 page, and a remotePolicy refusal. TestWatchPRDigestJSON and TestWatchPRsUnsupportedProvider stay
                 green [graft:mvp]
       contract: if the doc drifts, TestWatchPromptAzureDevOps fails unless watch.md says azure-devops lists ship
                 PRs with an always-empty bot_prs and still says 'absent when the forge could not be queried'.
                 TestWatchPromptPRsAreInformationOnly stays green

Wave 5 (depends t-15)
  t-17 Route ship.md §5/§6 through ship ci and merge                            [risk+verification+mvp]
       files:    assets/prompts/ship.md, internal/cmd/ship_prompt_test.go, README.md, docs/dross.1
       desc:     §5, for azure-devops and bitbucket: poll `dross ship ci <phase-id>` every ~30s and map each
                 verdict [D-1, mapping from verification]: pass → §6; fail → On failure with the printed link;
                 pending → keep polling; none → keep polling until the ~10-minute window ends, then ask proceed or
                 stop; `pending (queued)` at window end → ask; ask → ask proceed or stop. Today's skip sentence
                 skips any repo without GitHub, Forgejo or GitLab CI files. It is rewritten so neither provider is
                 skipped for a missing azure-pipelines.yml or bitbucket-pipelines.yml. §6: after the re-ship,
                 `dross ship ci` must read pass, then run `dross ship merge <phase-id>`. A blocked exit goes to §7
                 awaiting-merge; any other non-zero is surfaced and `dross phase complete` is never run. No curl
                 for either provider. ship.md never spells the provider's branch-keep, bypass or auto-complete
                 field names [D-16]. The GitHub, Forgejo and GitLab recipes are not touched. README.md and
                 docs/dross.1 list both commands [D-12]. make install (r-01).
       covers:   c-5, c-6, c-10, c-11
       depends:  t-15
       contract: if the CI gate drifts, TestShipPromptAzureDevOpsCI fails unless §5 names `dross ship ci
                 <phase-id>` for azure-devops and bitbucket, maps all five verdicts as above, gives the ~30s poll
                 and the ~10-minute window, and says none and ask are never a pass
       contract: if the prompt and the binary disagree, TestShipPromptCIExitCodesMatch fails [graft:mvp]: the
                 exit codes §5 lists must equal ship ci's constants
       contract: if classic pipelines get skipped, TestShipPromptWatchesClassicPipelines fails
                 [graft:verification] unless §5 says an azure-devops repo with no azure-pipelines.yml is still
                 watched and the skip sentence excludes azure-devops and bitbucket
       contract: if a failed merge reaches teardown, TestShipPromptMergeGate fails unless §6 runs `dross ship ci`
                 (pass) immediately before `dross ship merge`, maps the blocked exit to awaiting-merge, and says a
                 non-zero merge never runs `dross phase complete`
       contract: if teardown or a bypass can be prompted, TestShipPromptNoUnguardedSwitch's forbidden list gains
                 closesourcebranch, bypasspolicy [graft:verification] and autocompletesetby [graft:mvp];
                 deletesourcebranch is already banned. TestShipPromptNamesCompleteAsTeardownOwner stays green
       contract: if a deferred provider is moved by accident, TestShipPromptGitHubCIWatchWaitsForRuns or
                 TestShipPromptGitLabSections goes red [graft:verification]
       contract: if the docs drift, TestReadmeDocumentsShipCIMerge fails unless README.md and docs/dross.1 name
                 both commands

Wave 6 (depends t-7, t-14, t-16, t-17)
  t-18 Run the live ADO smoke and record it                                     [risk+verification+mvp]
       files:    internal/ship/testdata/ado_live_smoke.toml, internal/ship/azuredevops_comment.go,
                 internal/ship/azuredevops_comment_test.go   (the last two change only on the blocks outcome)
       desc:     After make install, drive the 11 steps against the real org with the installed binary, in
                 pair-mode, and fill in each step's result and evidence [D-11]. The comment-resolution policy for
                 the probe is set by hand in the org; dross never sets it. blocks_completion is read from the PR's
                 Comment requirements evaluation after the thread posts [graft:verification]. If a statusless
                 thread blocks completion, set adoThreadStatus = "closed" (the locked fallback), flip
                 TestPostAzureDevOpsThread's status row in the same commit, and re-run the merge step. A live-step
                 failure becomes a /dross-quick fix plus a re-run, never a mock-only pass.
       covers:   c-7
       depends:  t-7, t-14, t-16, t-17
       contract: if mock is reported as live, TestADOLiveRecordHonest (t-7) passes with posture "live" only when
                 all 11 steps carry pass plus real-host evidence. Any failed step leaves posture "partial"
       contract: if the probe's answer is ignored, statusless_thread_blocks = true with adoThreadStatus "" fails
                 TestADOLiveRecordHonest. Once flipped, TestPostAzureDevOpsThread asserts status closed
       contract: if a leg is hollow, it fails unless its recorded evidence shows [graft:verification+mvp]:
                 - the dedupe re-run prints "found open PR #N", and the org lists one active PR from phase/<id>
                 - the listing holds an inline comment with a non-empty path and line > 0 (a real threadContext
                   shape that differs from the mock fails here)
                 - PRHeadOf gives phase/<id>, Open, !CrossRepo
                 - AuthenticatedUser().ID equals the author id on dross's thread
                 - the /dross-respond reply lands a ReplyMarker thread that the next listing drops as own
                 - the ship ci verdict is recorded verbatim (none and ask recorded as such) and agrees with the
                   build page
       contract: if the merge step's evidence is hollow, the squash-merge step must record a non-empty `git
                 ls-remote origin phase/<id>` after completion and merged-status = merged from GetPRStatus, or the
                 validator rejects the step
```

Coverage: all 11 criteria are covered, as in the skeleton.

| Criterion | Tasks |
|---|---|
| c-1 ListPRComments | t-1, t-8, t-14 (live: t-18) |
| c-2 PRHeadOf | t-2, t-8, t-14 (live: t-18) |
| c-3 AuthenticatedUser | t-1, t-8, t-14 (live: t-18) |
| c-4 OpenPRsTargeting | t-2, t-8, t-14 |
| c-5 ADO CI watch | t-3, t-9, t-13, t-15, t-17 |
| c-6 ADO squash completion | t-4, t-13, t-15, t-17 |
| c-7 live smoke | t-7, t-18 |
| c-8 /dross-watch on ADO | t-12, t-16 |
| c-9 /dross-review diff | t-6 |
| c-10 Bitbucket ship ci | t-10, t-13, t-15, t-17 |
| c-11 Bitbucket ship merge | t-5, t-11, t-13, t-15, t-17 |

Not adopted: verification t-1 (widening the triage-id grammar, dropped under D-2) and verification t-12's
`//go:build adolive` harness (replaced under D-11). The plan adds no task that appears in none of the drafts.

## Disagreements

### D-1: Where the window boundary lives (none / queued at window end → ask)
- **risk:** `dross ship ci --window-closed`. A shared `closeWindow` in Go turns none and queued-only pending into
  ask, and TestCloseWindow proves it. The prompt only keeps the clock.
- **mvp, verification:** no flag. `ship ci` prints `pending (queued)` or `pending (running)`, and ship.md maps
  none and queued-at-window-end to ask. Verification argues that a flag extends the locked `dross ship ci
  <phase-id>` surface, and ci_merge_home says ship.md owns the window.
- **Provisional:** no flag. This follows the majority and the literal reading of ci_merge_home. It edits the
  skeleton's t-3, t-13, t-15 and t-17. mvp's TestShipPromptCIExitCodesMatch partly replaces risk's lost unit test.
- **Why it matters:** it decides whether "stuck queued is never a pass" is a Go unit test or a prompt-text test.
  Under either choice Go never emits pass for none or queued. What is left is the model polling forever or
  asking too early, and only the prompt tests guard that.

### D-2: ADO comment id encoding
- **risk, mvp:** `<threadId><commentId %05d>`, digits only. prtriage is untouched, comment ids 0 or > 99999 are
  an error, and the encoding is injective because ADO comment ids are int16.
- **verification:** `<thread>.<comment>`, plus a wave-1 task (its t-1) that widens the grammar in
  internal/prtriage/record.go and internal/cmd/pr_resolve.go into one shared validator. It rejects packing as
  "lossy". That claim does not hold for a fixed-width suffix with a range check.
- **Provisional:** packing. Verification t-1 is dropped, and mvp's 1/12 vs 11/2 row guards the concatenation case.
- **Why it matters:** packed ids are harder to read in `dross pr comments` (`i4200001` vs `i42.1`) and depend on
  ADO keeping comment ids within int16. Widening touches a second package and every provider's triage validation.
  If you want readable ids, re-add verification t-1 in wave 1 and flip t-1's id contract.

### D-3: Stale head in `ship ci` (the PR's head ≠ the watched SHA)
- **risk:** error, naming `dross ship <id>`, with 0 CI reads (ADO t-9 and Bitbucket t-10).
- **verification:** pending, with a Reason naming both SHAs and 0 CI reads.
- **mvp:** ambiguous ("stops giving pending, must never give pass").
- **Provisional:** pending with a Reason. This departs from the skeleton.
- **Why it matters:** this is my reasoning, not any draft's, and the t-18 CI step should confirm it. ADO's
  lastMergeSourceCommit is set when ADO computes the PR merge, which happens asynchronously after a push or PR
  creation. So the first poll right after `dross ship` can see it lag or be absent, and an error would break
  the §5 loop on a transient state. The cost: a permanently stale local origin ref reads as pending (named in
  the Reason) instead of failing fast. Merge still refuses a mismatched head in both providers, and all drafts
  agree on that.

### D-4: partiallySucceeded builds
- **risk:** fail. Neither "succeeded" nor "ask" fits, and c-5 says "anything failed → fail".
- **verification:** ask. The lock lists the fail states explicitly, so a state outside that list should not guess.
- **mvp:** silent.
- **Provisional:** fail (skeleton; it follows c-5's text).
- **Why it matters:** fail drops into §5's three-cycle fix loop with a link, while ask hands the call to the
  user. Pipelines with continueOnError steps partially succeed routinely, so this choice decides whether such
  a repo can ever ship without a human override.

### D-5: Merge exit-code granularity
- **risk:** merged 0, blocked has its own code, everything else 1.
- **verification:** one code per outcome: blocked, conflicts, failed, and pending-at-bound.
- **mvp:** one failure code, because ship.md treats every failure the same.
- **Provisional:** risk's middle ground. The pending-at-bound case becomes an error whose message says "re-run
  dross ship merge", and t-4's already-completed path makes that re-run idempotent.
- **Why it matters:** blocked maps to awaiting-merge, which the blocked_completion lock requires, while other
  failures are surfaced. A distinct pending code would only help if §6 auto-re-ran the merge, which no draft's
  ship.md does.

### D-6: Which PRs `/dross-watch` computes CI for
- **risk, mvp:** every non-fork PR.
- **verification:** only non-fork PRs whose head is phase/ or milestone/. Every other PR gets ChecksNone with no
  reads.
- **Provisional:** verification's scope (grafted into t-12).
- **Why it matters:** ADO's bot line is always empty, so checks on other PRs are never shown. Each one still costs
  builds and evaluations reads on every tick. Under the whole-list-fails rule all three drafts share, any one
  failing read blanks the whole digest.

### D-7: A full page from the dependents lookup
- **risk:** a full page that holds a match returns the match; a full page with no match is an error.
- **mvp, verification:** any full page is an error.
- **Provisional:** risk's.
- **Why it matters:** guardOpenPRsTargeting turns every non-ErrRefused error into an announced skip, and the
  delete then proceeds. Erroring on a page that already shows a dependent turns a refusal into a skip.

### D-8: codeChange comments
- **risk, mvp:** kept; mvp keeps them implicitly by dropping only system and deleted comments.
- **verification:** dropped, while unrecognised types are kept.
- **Provisional:** keep them. c-1 names only system comments as skipped.
- **Why it matters:** a human comment silently dropped from triage is invisible to /dross-respond. The t-18
  listing leg will show what real codeChange comments contain.

### D-9: How Bitbucket exposes the 202 Location header
- **risk:** a new `bbDo` seam that returns headers. bbRequest becomes a wrapper, and bbDo is registered in
  sinks.go and scrubbingHelpers.
- **verification:** an optional (variadic) trailing header out-param on bbRequest. That keeps one Basic-auth
  entry point and avoids a registry change.
- **mvp:** bbRequest "surfaces" the header, with no mechanism given.
- **Provisional:** bbDo (skeleton).
- **Why it matters:** risk's approach adds a second registered request function. The screened-transport guards
  must learn it, and bbRequest's existing sinks row (sinks.go:140) must still be satisfied by a wrapper.
  Verification's approach changes the signature that bbRequest's 7 callers share. Both validate Location's host
  and path before the token rides along.

### D-10: Bitbucket merge pre-read, head pin and `?async=true`
- **risk:** pre-reads the PR. MERGED → done with 0 POSTs. DECLINED, SUPERSEDED, or a head that does not prefix
  sha → refuse.
- **verification:** no pin ("Bitbucket has no lastMergeSourceCommit; c-11 doesn't ask"). It POSTs with
  `?async=true`.
- **mvp:** neither.
- **Provisional:** risk's pre-read, and no `?async=true`; both the 200 and 202 paths are handled.
- **Why it matters:** the pre-read makes a re-run idempotent and catches an unwatched head. But the pin is a
  check-then-act gap, not atomic like ADO's lastMergeSourceCommit. `async=true` would make the 202 path the only
  path, which tests more uniformly but always pays the poll.

### D-11: Form of the live smoke run (c-7)
- **risk:** a record template plus an honesty validator in wave 1 (internal/ship/testdata, next to the mocked
  suite), and a human-driven run in the last wave.
- **mvp:** a human pair-mode run recorded in `.dross/phases/<id>/live-smoke.toml`, with no validator.
- **verification:** a `//go:build adolive` Go harness that drives the cobra commands, plus a `.dross` record.
- **Provisional:** risk's form, with verification's per-step assertions grafted in as required evidence.
- **Why it matters:** c-7 includes a /dross-respond reply, a slash command a Go test cannot drive; verification
  substitutes `dross pr reply`. c-7 also says the run is "recorded next to the mocked suite". The validator
  turns "mock never reported as live" into a red test. The cost is that there is no repeatable automated live
  run for later regressions.

### D-12: Doc surfaces (watch.md, README.md, docs/dross.1)
- **risk:** updates all three; watch.md and README get tests.
- **mvp:** explicitly leaves them, saying the watch.md lines "go stale, but no criterion depends on them".
- **verification:** silent.
- **Provisional:** update them (t-16, t-17).
- **Why it matters:** two watch.md lines become false after this phase. Line 21 says a provider other than
  GitHub is absent, and line 65 says "no dross verb merges a PR". Without the README and man page updates, two new
  commands would ship undocumented.

### D-13: Where the prompt tasks sit, and whether review.md is its own task
- **risk:** review.md is its own wave-1 task (t-6), and ship.md waits for the cmd task.
- **verification:** ship.md and review.md are one task in wave 4, after the cmd task. It calls review.md alone
  too small.
- **mvp:** one prompt task in wave 1. The plan freezes the exit codes up front, and the cmd task adds a drift test.
- **Provisional:** risk's split, plus mvp's drift test inside t-17.
- **Why it matters:** review.md needs nothing from any other task, so holding it in wave 4 is a wave error.
  Putting ship.md in wave 1 would require freezing the exit codes before any code exists. The cost is that t-6
  sits at the granularity floor (a bullet and a test file), which two drafts call too small.

### D-14: Structure of the provider layer
- **risk:** implementation tasks write unexported functions only. Two wiring tasks own the dispatch arms (t-8
  for reads, t-13 for ci and merge), and t-8 alone retires TestAzureDeferredSurfacesFailClosed. ADO CI is split
  into a pure rollup (t-3) and a fetch (t-9), and the hostguard rows are centralised.
- **verification:** each implementation task adds its own dispatch arm and its own off-allowlist test in its own
  file. ADO CI is one task. t-7 waits a wave only because it shares the deferred-surface test.
- **mvp:** all four reads plus their dispatch in one 5-file task; ADO CI and merge plus the shared dispatch in
  another.
- **Provisional:** risk's structure.
- **Why it matters:** with risk's structure, prcomments.go, the hostguard table and the deferred-surface test
  each have one owner, and the preview-helper edge (t-9 → t-1) is explicit. The cost is 18 tasks across 6 waves,
  against 12 across 4 (verification) or 7 across 4 (mvp).

### D-15: Spec vs locked behaviour, c-4 "a failed lookup … so milestone branch deletion refuses"
- **Claimed by risk.** guardOpenPRsTargeting refuses only on a found dependent or on hostallow.ErrRefused. Any
  other lookup error prints "open-PR check skipped" and lets the delete proceed on the milestone-record scan.
  That is the locked `dependent_detection` decision from milestone-stacking, which I confirmed in
  internal/cmd/milestone_dependents.go:114-125. So c-4's "refuses" is not true for a transient ADO failure.
- **mvp and verification** do not raise it. Verification's "not errors.Is(ErrBasePRLookupUnsupported)" contract
  implies the guard separates errors in a way it does not.
- **Provisional:** keep announce-and-skip and change no guard behaviour. The ADO arm guarantees an error, never
  an empty list (t-2). t-14 proves three paths: a dependent refuses, a 500 gives the announced skip, and a
  disallowed host refuses. D-7 narrows the gap for full pages.
- **Why it matters:** /dross-verify will judge c-4 against text that the shipped behaviour only half meets.
  Either you accept the announced-skip reading for c-4, or you amend c-4 (or reopen dependent_detection, which
  would change every provider and is out of this phase's scope) before verify.

### D-16: Spec vs an existing guard, c-6 "§6 completes the PR … with deleteSourceBranch = false"
- **Claimed by verification.** TestShipPromptNoUnguardedSwitch normalises ship.md (lowercase, `_` and backticks
  stripped) and bans `deletesourcebranch`, so ship.md cannot spell the field that c-6 names. I confirmed this in
  internal/cmd/ship_prompt_test.go:14-23 and 175-189. mvp's "fails on deleteSourceBranch true" is therefore
  redundant: any spelling already fails.
- **Provisional:** the field exists only in Go (t-4). ship.md names only `dross ship merge`. The ban grows to
  cover closesourcebranch, bypasspolicy and autocompletesetby (t-17). This is consistent with ci_merge_home, so it
  is not a real conflict once the merge lives in Go.
- **Why it matters:** a reviewer or verifier who reads c-6 as "ship.md sends deleteSourceBranch=false" would look
  for it in the prompt and not find it. The evidence for c-6's field values is t-4's payload test and t-15's pin
  test, not ship.md text.
