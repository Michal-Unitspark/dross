# Panel synthesis — azure-devops-provider

## Scores

Scores are out of 5. Before scoring, every path and guard claim in the three drafts was checked against the repo. All referenced paths exist. The notes below record what that check found.

| Draft | Dimension | Score | Note |
|---|---|---|---|
| risk | criteria coverage | 5 | Covers 9/9 criteria. Each failure mode has one owning task. c-5, c-6 and c-7 are proven both at unit level and at the cmd level. Adds work beyond the literal spec: 4000-char truncation, a defaults.Apply guard, and tolerant TOML decode. |
| risk | test-contract specificity | 5 | Most specific of the three: exact inputs, off-by-one and lookalike-host rows, api-version injection, 203 sign-in pages, and the registry walkers named. Misses the guard-forced `randomLine` arm in secretscan_test.go. |
| risk | granularity | 3 | t-8 (10 files) and t-3 (6 files) are guard-forced, so they are fine. t-9 (diag + cmd + assets, 6 files) and t-11 (defaults + cmd + assets, 6 files) cross 3 layers without any guard forcing it. |
| risk | wave correctness | 2 | t-4 makes `remote.api_version` settable in wave 1, but its only reader (buildOpenOpts) arrives in t-10 in wave 4. TestNoSettableKeyIsInert therefore stays red through waves 1–3; I checked the guard, which needs a `.Remote.APIVersion` reader outside cmd/project.go. The other dependencies are strict. |
| mvp | criteria coverage | 3 | 9/9 on paper. But c-5 "printed" and c-6 "stops the ship" depend on unchanged cmd/ship.go with no cmd-level test, c-3 has unit rows only, and an unknown PR status is not treated as an error. |
| mvp | test-contract specificity | 3 | Rows are concrete, and it is the only draft to catch the `randomLine` generator arm and the setter↔reader pairing. It has few negative cases: no ignored-filter dedupe, no api-version injection, no sign-in page handling. |
| mvp | granularity | 2 | t-1 packs transport, open, reviewers, dedupe, status and comment into one task (6 criteria). t-2 spans project, cmd and diag (3 layers) without guard forcing. t-4's 12 files include headpr.go and a prompt test that no guard forces. |
| mvp | wave correctness | 3 | Dependencies are minimal and strict. But t-5 omits internal/cmd/testdata/pathfence_scan/not_paths.txt, so TestEveryPathShapedFieldIsDeclared goes red. Wave 1 is lopsided: t-1 alone is the critical path. |
| verification | criteria coverage | 5 | 9/9, with an anchoring test per criterion and a contract per locked decision. Proves c-5 "printed" and c-6 "stops the ship" at the cmd level. Has an end-to-end doctor test. |
| verification | test-contract specificity | 4 | Uses "if X breaks, Y fails" throughout, with concrete rows. Lacks risk's injection and sign-in checks. Also misses the `randomLine` arm and the not_paths.txt row. |
| verification | granularity | 4 | Every task is 6 files or fewer except the forced flip (10). t-9 sits at 5 files. t-8 mixes config plumbing with cmd e2e tests. |
| verification | wave correctness | 4 | Flip ordering is right, and setter and reader land together. t-8 is pinned to wave 4 even though its api_version plumbing needs only t-3. t-5's dependency on t-2 is only for a test fixture. t-8 omits the not_paths.txt row, so that commit is red. |

**Skeleton: verification.** It is the only draft with no structural red commit apart from a one-line ledger miss. Its granularity is balanced and its contracts are already written in contract form. risk has deeper contracts, but carries an inert-key red window and two over-wide wave-4 tasks.

## Merged plan

```
Phase azure-devops-provider — 13 tasks across 4 waves

Wave 1
  t-1  Parse ADO remotes into canonical identity  [verification+risk+mvp]
       files:    internal/project/azuredevops.go, internal/project/azuredevops_test.go,
                 internal/project/remote.go, internal/project/remote_test.go
       covers:   c-1, c-2
       depends:  —
       desc:     Exported ParseAzureDevOpsRemote(raw) -> {URL, APIBase, Base, Project, Repo, Services},
                 fed internally by parseGitRemote's taint-cleared (host, path). parseGitRemote moves
                 to EscapedPath. Host-aware one-segment rule (Disagreement 1). Pure, no callers yet.
       contract: if a Services form mis-canonicalizes, TestParseAzureDevOpsRemote fails:
                   git@ssh.dev.azure.com:v3/o/p/r, ssh://git@ssh.dev.azure.com/v3/o/p/r,
                   https://o@dev.azure.com/o/p/_git/r and .../r.git must each give
                   https://dev.azure.com/o/p/_git/r with api_base https://dev.azure.com/o
                 if userinfo leaks, the https://o:PAT@dev.azure.com/... row fails when URL or APIBase contains "PAT"
                 if the one-segment rule hits an org-carrying host, TestParseAzureLegacyHostCarriesOrg fails:
                   o.visualstudio.com/p/_git/r must give api_base https://o.visualstudio.com and project p;
                   o.visualstudio.com/DefaultCollection/p/_git/r must give api_base .../DefaultCollection;
                   o@vs-ssh.visualstudio.com:v3/o/p/r must give https://o.visualstudio.com/p/_git/r (not dev.azure.com)
                 if %-encoding is decoded or doubled, .../o/My%20Project/_git/My%20Repo stops round-tripping
                   byte-identical (no raw space, no %2520), Project() stops being "My Project", or the ssh twin diverges
                 if Server depth/port handling breaks, these rows fail:
                   /tfs/DefaultCollection/P/_git/R must give api_base https://tfs.corp/tfs/DefaultCollection;
                   host-root /Coll/P/_git/R must give https://tfs.corp/Coll;
                   ssh://tfs.corp:22/... must drop the port; :8443 must stay
                 if the repo==project rule breaks, dev.azure.com/o/_git/r stops giving project r, repo r
                 if malformed input half-parses, TestParseAzureRejects fails. Each must return ok=false:
                   no _git, _git last, an empty segment, a "."/".." segment, dev.azure.com/_git/r, github.com/x/y  [risk]
                 if the host predicate is a substring match, TestAzureServicesHost fails: evil-visualstudio.com,
                   visualstudio.com.evil.example and dev.azure.com.evil.example must all be false  [risk]
                 if EscapedPath alters a non-ADO remote, the existing TestDetectRemote / TestDetectRemoteDropsUserinfo rows fail

  t-2  Add ADO PAT rule and known hosts  [verification+risk+mvp]
       files:    internal/secretscan/rules.go, internal/secretscan/secretscan_test.go,
                 internal/hostallow/hostallow.go, internal/hostallow/hostallow_test.go
       covers:   c-9, c-4
       depends:  —
       desc:     "azure-devops-pat" rule: 76 alnum + "AZDO" + 4 alnum, word-bounded, needle "AZDO", with
                 fixtures built at runtime so the repo self-scan stays clean. saasDefaults gain
                 dev.azure.com, vssps.dev.azure.com and *.visualstudio.com.
       contract: if the rule or its corpus row is missing, TestEveryRuleHasAHitCase fails. The row must give
                   exactly one azure-devops-pat hit, and TestRedactCoversEveryRule must redact it.
                   TestEveryRuleHasNeedles fails if "AZDO" is dropped.
                 if randomLine has no azure-devops-pat arm, prefilterSeeds and TestReportNeverEchoesTheValue
                   panic "no generator for rule"; both iterate Rules()  [mvp; checked in the repo]
                 if the anchor is loose, AZDO at 75 or 77, 83- or 85-char runs, and lowercase "azdo" must give zero hits
                 if a reference trips the rule, a benign row fails. "<your-pat>", $AZURE_DEVOPS_EXT_PAT,
                   auth_env = "AZURE_DEVOPS_EXT_PAT" and a bare 52-char [a-z2-7] string must give zero hits.
                   `token = <same 52>` must still give exactly one key-context hit
                 if a sink stops screening, ScanPayload {"title": PAT} / {"body": PAT} must return ErrHit,
                   and a Scan of a project.toml line holding the PAT must hit
                 if a host is missing or the wildcard is loose, TestADOBuiltinHosts fails.
                   Must accept: dev.azure.com/o, vssps.dev.azure.com/o, o.visualstudio.com, o.vssps.visualstudio.com.
                   Must refuse with ErrRefused: evil-visualstudio.com, visualstudio.com.attacker.example,
                   bare visualstudio.com, x.dev.azure.com, o.visualstudio.com:8443, http://dev.azure.com

  t-3  Build ADO REST transport with single credential  [verification+risk+mvp]
       files:    internal/ship/azuredevops.go, internal/ship/azuredevops_test.go, internal/ship/open.go,
                 internal/ship/comment.go, internal/secretscan/sinks.go, internal/ship/redact_test.go
       covers:   c-4
       depends:  —
       desc:     adoRequest is the only ADO HTTP entry. It checks the host before reading the token, runs
                 ScanPayload before http.NewRequest, and sends one auth header plus a validated api-version
                 (default 7.1) and FedAuthRedirect: Suppress. It follows no redirects and runs redact.Scrub on bodies.
                 OpenOpts/CommentOpts gain APIVersion. The guard-forced registrations in sinks.go and scrubbingHelpers explain the 6 files.
       contract: if a second or wrong credential leaks, TestAzureDevOpsAuthHeader fails.
                   Schemes "", "private-token" and " BASIC " must give exactly one
                   Authorization: Basic base64(":"+tok) and no PRIVATE-TOKEN. "bearer" must give Bearer tok
                 if the host check moves after the getenv, TestAzureDevOpsRefusesBeforeTokenRead fails.
                   With a policy from https://dev.azure.com/o/p/_git/r and an httptest api_base: ErrRefused, 0 hits,
                   neither token nor env name in the error, and still a refusal with the env UNSET.
                   TestNoGetenvOutsideHostguard fails if azuredevops.go calls os.Getenv  [mvp]
                 if api-version is missing, duplicated or injectable, TestAzureDevOpsAPIVersionOnEveryCall fails.
                   "" must give 7.1; "6.0" and "7.1-preview.1" must pass through;
                   an existing ?searchCriteria.status=active must keep exactly one api-version;
                   "7.1&x=y", "7.1#", " " and "../" must error with 0 requests before the env read  [risk]
                 if a sign-in page reads as success, TestADOSignInIsAuthFailure fails. A 203 HTML page and a 302
                   must both be auth errors; the 302 target must see 0 requests  [risk]
                 if the scrub is dropped, a mirrorAuthServer 401/403 echoing the token and base64(":"+tok) leaks
                   either value into the error. A ghp_ body must be refused with 0 hits
                 if adoRequest is unregistered, these fail naming adoRequest: TestEveryOutboundSeamIsRegistered and
                   TestScreenPrecedesRequestInEveryScreenedTransport (internal/cmd), TestShipHelpersScrubAtSource
                 if names are double- or un-escaped, TestADORepoEndpointEscapesOnce fails. ("My Project","My Repo") must
                   give My%20Project/_apis/git/repositories/My%20Repo, never %2520; "#" and "?" must be escaped  [risk]
                 if config is empty, an empty api_base/auth_env must error naming [remote].api_base/auth_env, 0 requests  [verification]

Wave 2 (t-4, t-5, t-6 depend t-1, t-3; t-7 depends t-3)
  t-4  Open ADO PR with optional reviewers  [verification+risk]
       files:    internal/ship/azuredevops_open.go, internal/ship/azuredevops_open_test.go
       covers:   c-5, c-2
       depends:  t-1, t-3
       desc:     openAzureDevOpsPR posts .../{project}/_apis/git/repositories/{repo}/pullrequests. Then one PUT
                 .../reviewers/<id> {isRequired:false} per reviewer: a GUID as-is, otherwise an Identities lookup
                 at adoIdentityBase. Response structs are function-local, so this task adds no not_paths rows.
                 Description capped at 4000 runes (provisional, see D5).
       contract: if the create body is wrong, TestOpenAzureDevOpsPRPayload fails. Requirements:
                   sourceRefName refs/heads/phase/x (an already-prefixed head is not doubled); targetRefName
                   refs/heads/main; title with no "Draft:" prefix; description.
                   Draft=true must send isDraft:true; Draft=false must send no isDraft key  [risk].
                   No reviewers key in the create body
                 if the API url is recorded instead of the browser URL, TestOpenAzureDevOpsPRResultURL fails:
                   pullRequestId 42 must give https://dev.azure.com/o/p/_git/r/pullrequest/42, with My%20Repo single-encoded.
                   A missing pullRequestId and a 409 TF401179 must each give (nil, err)  [risk]
                 if Server addressing breaks, a /tfs/DefaultCollection remote stops posting to
                   /tfs/DefaultCollection/P/_apis/git/repositories/R/pullrequests
                 if reviewer resolution is wrong, TestOpenAzureDevOpsPRReviewers fails. For ["<GUID>","alice@corp.example"]:
                   exactly one identities GET (searchFilter=General&filterValue=<escaped>), the GUID never looked up,
                   and two PUTs with isRequired:false
                 if a reviewer failure turns fatal, TestOpenAzureDevOpsPRReviewerFailureNonFatal fails.
                   Identities 404/500, 0 matches, 2 matches (ambiguous, skipped  [risk]) and a PUT 500 must each return
                   (result{Number:42}, err naming the reviewer, not the token), and the remaining reviewers must still be added.
                   A create 500 must return (nil, err)
                 if the identity host is wrong, TestAzureDevOpsIdentityBase fails: dev.azure.com/o and
                   o.visualstudio.com must give https://vssps.dev.azure.com/o; https://tfs.corp/tfs/Coll must map to itself
                 if a long body is sent raw, TestOpenAzureDevOpsPRLongDescription fails: the description sent must be
                   ≤4000 runes, end in a truncation note, and not split a multi-byte rune  [risk, provisional]

  t-5  Post ADO comment as non-blocking thread  [risk+verification]
       files:    internal/ship/azuredevops_comment.go, internal/ship/azuredevops_comment_test.go
       covers:   c-7
       depends:  t-1, t-3
       desc:     postAzureDevOpsComment POSTs .../pullRequests/<n>/threads with
                 {comments:[{parentCommentId:0, content, commentType:1}]} and no status. The thread status is one
                 named const, so the locked "closed" fallback is a one-line change.
       contract: if the endpoint or payload drifts, TestPostAzureDevOpsThread fails. PRNumber 7 must post to
                   /{project}/_apis/git/repositories/{repo}/pullRequests/7/threads with api-version, and
                   comments[0].content must equal Body. The top level must have NO "status" key; status active fails
                 if the body escapes the scanner, a body holding a glpat-/ghp_ token must return
                   secretscan.ErrHit with 0 requests
                 if the error path leaks, TestPostAzureDevOpsCommentErrorRedacted (mirrorAuthServer 403) fails when the
                   error contains the PAT or base64(":"+PAT). A 203 sign-in page must error, never report "posted"  [risk]

  t-6  Read ADO PR status and dedupe by source  [risk+verification]
       files:    internal/ship/azuredevops_pr.go, internal/ship/azuredevops_pr_test.go,
                 internal/ship/headpr.go, internal/ship/headpr_test.go
       covers:   c-6, c-8, c-2
       depends:  t-1, t-3
       desc:     azureDevOpsPRStatus: Merged only when status==completed; BaseRef = targetRefName minus
                 refs/heads/. adoOpenPRBySource re-checks source, status and forkSource client-side.
                 FindOpenPRByHead gains the azure-devops arm (headpr.go is not an enumScanSites entry; checked in
                 the repo) and the stale "deferred azure-devops arm" comment is removed.
       contract: if abandoned or active reads as merged, TestAzureDevOpsPRStatusMapping fails:
                   completed and "Completed" must give Merged true; abandoned and active must give false
                 if an unknown status reads as not-merged, the test fails. An empty/missing status, a non-JSON body
                   or a 404 must return an error, never PRStatus{}, nil. PRNumber 0 must error with 0 requests
                 if refs/heads/ is not stripped, or stripped twice, refs/heads/release/x stops giving release/x.
                   A retargeted PR must report its current targetRefName
                 if the filter is misnamed or unescaped, TestFindOpenPRByHeadAzureDevOps fails.
                   The mock asserts searchCriteria.sourceRefName == refs/heads/phase/x (a head with "&" or "+"
                   round-trips) and searchCriteria.status == active. Hit 17 must give {17, <canonical>/pullrequest/17};
                   an empty value list must give (nil, nil)
                 if the server filter is trusted blindly, a foreign PR gets recorded. A mock that ignores the query
                   and returns PRs from refs/heads/other, an abandoned phase/x and a forkSource phase/x must give (nil, nil)
                 if a failed lookup reads as "none" or "unsupported", the test fails. A 500, a 203 sign-in page and
                   an unparseable body must each return an error that is !errors.Is(ErrHeadPRLookupUnsupported)

  t-7  Add remote.api_version and thread it to opts  [mvp+verification+risk]
       files:    internal/project/project.go, internal/project/project_test.go,
                 internal/cmd/testdata/pathfence_scan/not_paths.txt, internal/cmd/project.go,
                 internal/cmd/project_test.go, internal/cmd/ship.go, internal/cmd/ship_test.go
       covers:   c-2
       depends:  t-3
       desc:     Remote.APIVersion (toml and json "api_version,omitempty") also accepts a bare TOML number
                 (6.0 -> "6.0"). Adds the readDotted/writeDotted arms and a ledger row, and buildOpenOpts and
                 buildCommentOpts copy the field. The 7 files are guard-forced: the field needs its ledger row,
                 and the setter needs a reader.
       contract: if the setter lands without the buildOpenOpts reader, TestNoSettableKeyIsInert fails naming remote.api_version  [mvp]
                 if the ledger row is missing, TestEveryPathShapedFieldIsDeclared fails. The row is
                   `project.Remote.APIVersion api_version`  [risk]
                 if the toml and json tags differ, TestTomlFieldsCarryMatchingJSONTags fails  [found by repo check]
                 if the set/get arm is missing, TestExpandedDottedPathsRoundTrip fails: set 6.0 then get must
                   return "6.0". The saved line must be `api_version = "6.0"` with other lines byte-identical,
                   and an omitted field must add no line
                 if a hand-edited bare number breaks load, TestRemoteAPIVersionBareFloat fails: 5.0/6.0/7.1 must give
                   "5.0"/"6.0"/"7.1"; "7.1-preview.1" must stay verbatim;
                   `api_version = true` must be a load error naming remote.api_version  [risk]
                 if the copy is dropped, TestBuildOpenOptsCarriesAPIVersion / TestBuildCommentOptsCarriesAPIVersion
                   fail: "6.0" must reach opts.APIVersion

Wave 3 (depends t-1, t-4, t-5, t-6)
  t-8  Flip azure-devops into recognition and dispatch  [verification+risk+mvp]
       files:    internal/configenum/configenum.go, internal/configenum/configenum_test.go,
                 internal/project/remote.go, internal/project/remote_test.go,
                 internal/ship/open.go, internal/ship/comment.go, internal/ship/merged.go,
                 internal/ship/azuredevops_dispatch_test.go, assets/prompts/init.md, assets/prompts/onboard.md
       covers:   c-1, c-4, c-5, c-7, c-8
       depends:  t-1, t-4, t-5, t-6
       desc:     One commit, per the locked guard_atomicity: ShipProviders and KnownHostProviders gain azure-devops
                 (dev.azure.com, ssh.dev.azure.com, vs-ssh.visualstudio.com); DetectRemote routes ADO remotes,
                 including *.visualstudio.com, through t-1 with an api-base arm and Public=false; the trio dispatch
                 arms; the provider bullets. Wiring only.
       contract: if any site lags ShipProviders, these name it: TestShipDispatchMatchesShipProviders/{ship.OpenPR,
                   ship.PostComment,ship.GetPRStatus}, TestRemoteAPIBaseMatchesShipProviders,
                   TestRemoteWriterSubsetOfShipProviders, TestGuardsSeeNonEmptySets, TestDispatchUsesConfigenumNormalize
                 if ADO host recognition adds a tagged `switch host` ahead of DetectRemote's provider switch,
                   providerSwitchIn reads the wrong switch and TestRemoteAPIBaseMatchesShipProviders fails  [risk]
                 if the writer lists lag, TestPromptProviderListsMatchShipProviders (init.md, onboard.md) and
                   TestSetListRendersPipeJoined ("github | forgejo | gitea | gitlab | bitbucket | azure-devops") fail
                 if autodetect writes the wrong fields, a TestDetectRemote ADO row fails. https, user@, ssh v3,
                   legacy https and vs-ssh must each give provider azure-devops, the canonical URL, the org api_base
                   and Public=false. A Server /tfs/Coll/P/_git/R row must stay Provider "".
                   TestDetectRemoteDropsUserinfo gains https://me:<PAT>@dev.azure.com/...
                 if an arm mis-routes, TestAzureDevOpsDispatchRoutes fails. With Provider " Azure-DevOps ":
                   OpenPR must POST .../pullrequests, PostComment must POST .../pullRequests/7/threads,
                   GetPRStatus must read targetRefName, and none may return "unsupported provider".
                   An off-allowlist api_base must give ErrRefused with 0 hits
                 if a deferred surface returns empty with a nil error, TestAzureDeferredSurfacesFailClosed fails.
                   ListPRComments, PRHeadOf, AuthenticatedUser and OpenPRsTargeting must each return an error  [risk]
                 These existing tests pick up ADO with no edits and must stay green:
                   TestBaseChoresLegacyTable and TestMilestoneHeadLegacyTable (ADO subtests, 0 probes)
                   and TestNoSpawnOutputEscapes

Wave 4 (all depend t-8; t-9 also t-1; t-10, t-11, t-12 also t-7)
  t-9  Diagnose ADO remotes in doctor  [verification+risk]
       files:    internal/cmd/doctor.go, internal/cmd/doctor_test.go,
                 internal/diag/combinations.go, internal/diag/diag_test.go
       covers:   c-3
       depends:  t-1, t-8
       desc:     sameRemoteURL canonicalizes both sides through project.ParseAzureDevOpsRemote when either parses
                 (host kept, names case-insensitive); the mismatch line strips userinfo. RemoteCombination gains
                 an ADO arm: private-token/basic ineffective, stray auth_user flagged, bitbucket Basic warnings
                 suppressed; Server api_base-vs-url-base check.
       contract: if equivalent forms read as mismatched, TestSameRemoteURL's ADO rows fail. These must be true:
                   ssh v3 vs https, user@ vs bare, .git, a case-variant O/P/R, vs-ssh vs o.visualstudio.com https,
                   Server ssh:22 vs https. These must be false: a different repo, a different project,
                   dev.azure.com/o vs o.visualstudio.com (canonical_host). The github rows stay unchanged
                 if doctor misreports end to end, TestDoctorAcceptsAzureDevOpsRemote fails. With origin
                   git@ssh.dev.azure.com:v3/acme/proj/widget, init then doctor must print "git origin matches [remote].url"
                   with no "cannot open a PR". Repointing origin to .../other must print "does not match".
                   An azure-devops url without _git must be an issue, never "matches"  [risk]
                 if a PAT in origin is echoed, TestDoctorMismatchStripsUserinfo fails: for
                   https://o:SECRETPAT@dev.azure.com/o/p/_git/other the output must not contain SECRETPAT  [risk]
                 if a warning is wrong or missing, a TestCombinationWarnings ADO row fails:
                   ("","") and ("bearer","") must warn nothing; ("private-token","") must give one "no effect";
                   ("basic","") must give exactly the no-effect warning and NOT "auth_user is not set" or
                   "sends no Basic credential"; ("","me") must give one auth_user-ignored warning.
                   The bitbucket and gitlab rows stay unchanged
                 if a Server api_base missing the collection goes unflagged, TestRemoteCombinationAzureBaseMismatch fails:
                   url https://tfs/tfs/C/P/_git/R with api_base https://tfs/tfs must warn and name https://tfs/tfs/C;
                   a trailing-slash or case-only difference must stay silent  [risk]
                 if equivalence is done with a provider switch in doctor.go, TestDoctorCarriesNoProviderLiterals fails

  t-10 Prove ADO ship end to end through cmd  [risk+verification]
       files:    internal/cmd/ship_test.go, internal/ship/hostguard_test.go
       covers:   c-2, c-4, c-5, c-6, c-7
       depends:  t-7, t-8
       desc:     Cmd-level flows run the real FindOpenPRByHead, OpenPR and PostComment against an httptest
                 Server-shaped ADO remote configured through `dross project set`. The hostguard tables gain
                 ADO rows for every entry point that reads the token.
       contract: if wiring or addressing breaks, TestShipAzureDevOpsFullFlow fails. Remote: url srv/coll/P/_git/R,
                   api_base srv/coll, api_version 6.0, all set via `project set` (an enumKeys refusal fails it).
                   The GET must come before the POST. Every path must start /coll/P/_apis/git/repositories/R and
                   carry api-version=6.0 and one Basic :PAT header. Stdout must contain
                   "PR opened: .../_git/R/pullrequest/42 (#42)", and changes.json pr must be 42
                 if dedupe falls through, TestShipAzureDevOpsDedupeHit fails: GET active PR 17 must give 0 POSTs,
                   pr 17 recorded and "found open PR #17"
                 if a failed lookup opens anyway, TestShipAzureDevOpsLookupFailureRefuses fails: GET 500 must give
                   "could not check origin", name `dross ship <id>`, and leave 0 POSTs with pr still 0
                 if a reviewer failure fails the ship, identities 404 must still exit 0, print "Warning:" and record PR 42  [verification]
                 if `dross ship comment` misroutes, TestShipAzureDevOpsCommentPostsThread fails: it must POST .../pullRequests/<n>/threads  [risk]
                 if any ADO entry point reads the token before the host check, its new
                   TestShipRefusesOffAllowlistAPIBase row (open, comment, status, head-lookup, identity lookup)
                   sees >0 fake-host hits. If the guard over-refuses, the new
                   TestShipAllowsDerivedSelfHostedHost ADO Server row fails  [risk]

  t-11 Teach init and onboard prompts ADO defaults  [verification+mvp+risk]
       files:    assets/prompts/init.md, assets/prompts/onboard.md, internal/cmd/prompt_provider_list_test.go
       covers:   c-1
       depends:  t-7, t-8
       desc:     Host-mapping prose (dev.azure.com / *.visualstudio.com -> azure-devops), api_base = the org URL,
                 default auth_env AZURE_DEVOPS_EXT_PAT, public=false. Server remotes are configured by hand,
                 including remote.api_version (2019 5.0 / 2020 6.0 / 2022 7.0). Run make install afterwards (r-01).
       contract: if the prose or the locked default drifts, TestPromptsTeachAzureDevOps fails. init.md and onboard.md
                   must each contain "dev.azure.com", "visualstudio.com", "remote.api_version", and
                   AZURE_DEVOPS_EXT_PAT on the auth_env bullet
                 if the bullet is disturbed, TestPromptProviderListsMatchShipProviders fails (it must stay green from t-8)

  t-12 Document azure-devops and api_version in options  [mvp+verification+risk]
       files:    assets/prompts/options.md, internal/cmd/options_docs_test.go
       covers:   c-1
       depends:  t-7, t-8
       desc:     options.md lists azure-devops. It covers ADO auth (Basic :PAT by default, bearer for Entra,
                 private-token and basic ineffective, auth_user ignored), remote.api_version (default 7.1, with
                 the Server versions), and notes that http-only on-prem TFS stays refused by hostallow  [risk].
       contract: if options.md drops either "remote.api_version" or "azure-devops",
                   TestOptionsCoversTheProviderConditionalRemoteFields (extended with both) fails

  t-13 Seed ADO remotes without foreign credentials  [risk]
       files:    internal/defaults/defaults.go, internal/defaults/defaults_test.go, internal/cmd/init_test.go
       covers:   c-1, c-4
       depends:  t-8
       desc:     defaults.Apply skips auth_env and reviewers when the saved defaults' provider differs from a
                 non-empty detected provider, so a saved GITHUB_TOKEN never rides onto an ADO repo. Also adds the
                 init-level proof that an ADO remote is autodetected.
       contract: if a foreign auth_env or reviewer seeds through, TestApplySkipsForeignProviderCredentials fails:
                   {github, GITHUB_TOKEN, [alice]} on a detected azure-devops remote must give AuthEnv "" and Reviewers nil.
                   Same-provider defaults must still seed, and the TestApplyOnlyFillsZeroFields subtests must stay green
                 if init persists the wrong remote, TestInitDetectsAzureRemote fails. With origin git@ssh.dev.azure.com:v3/o/p/r
                   and a saved GitHub defaults.toml, project.toml must hold url https://dev.azure.com/o/p/_git/r,
                   provider azure-devops and api_base https://dev.azure.com/o, with public absent and no GITHUB_TOKEN
                 if a userinfo token reaches project.toml, TestInitNeverPersistsOriginPAT fails: for origin
                   https://o:PATVALUE@dev.azure.com/o/p/_git/r the file must not contain PATVALUE
```

Same-wave file overlap: none. Only t-7 touches not_paths.txt. That depends on t-4, t-5 and t-6 keeping their response structs function-local, which the pathfence walker skips.

## Coverage

| Criterion | Merged tasks |
|---|---|
| c-1 | t-1, t-8, t-11, t-12, t-13 |
| c-2 | t-1, t-4, t-6, t-7, t-10 |
| c-3 | t-9 |
| c-4 | t-2, t-3, t-8, t-10, t-13 |
| c-5 | t-4, t-8, t-10 |
| c-6 | t-6, t-10 |
| c-7 | t-5, t-8, t-10 |
| c-8 | t-6, t-8 |
| c-9 | t-2 |

All 9 criteria are covered. No new criteria were added.

## Disagreements

**D1. How the locked repo_identity rule reads on *.visualstudio.com. This needs the user's call.**
- The drafts agree: all three treat the org as part of the hostname. So for o.visualstudio.com/p/_git/r the base is empty and the project is p.
- That reading conflicts with the literal lock text, "a one-segment base before _git means repo == project". Read literally, the same URL gives base p and api_base https://o.visualstudio.com/p. That breaks c-1's "api_base (the org URL)".
- Provisional default: the host-aware reading in t-1. Confirm it, or amend the lock's wording.
- Related: only risk names the Server short form /tfs/Coll/_git/Repo, which is ambiguous under the marker rule. risk handles it with a doctor warning (t-9); mvp and verification are silent.

**D2. Where the remote parser lives and what it reads.**
- risk feeds the canonicalizer parseGitRemote's taint-cleared output, switches parseGitRemote to EscapedPath, and rejects a raw-string parser because it would trip TestNoSpawnOutputEscapes.
- verification uses one exported raw-string parser in project. mvp does too, and also duplicates the _git split inside ship.
- Provisional default: one parser in project, exposed as a raw entry for doctor and ship, but fed internally by parseGitRemote. There is no import cycle: ship already reaches project through phase.
- Why it matters: two parsers drift on %-encoding (the c-1 round-trip). A second userinfo-stripping site needs its own taint marker. EscapedPath also touches non-ADO DetectRemote, so the existing rows must stay green.

**D3. Which wave remote.api_version lands in. Two of three drafts leave a red commit.**
- risk: field and setter in wave 1 with no reader. TestNoSettableKeyIsInert stays red until wave 4.
- mvp: field, setter and reader together in wave 2, but no ledger row.
- verification: wave 4, bundled with the cmd e2e tests, and also no ledger row. Both of those leave TestEveryPathShapedFieldIsDeclared red.
- Provisional default: t-7 in wave 2 with field, setter, reader and ledger row together; the e2e tests are split out to t-10. That follows the wave rule, since t-7 needs only t-3.

**D4. The defaults.Apply foreign-credential guard (t-13): risk only.**
- risk says a saved GITHUB_TOKEN silently overrides the locked AZURE_DEVOPS_EXT_PAT default, because init.md only asks about fields the defaults did not seed. mvp and verification have no such task; mvp's lens drops anything that no criterion requires.
- Provisional default: include it. It changes seeding for every provider pair, not just ADO.
- It also does not cover Server remotes, whose detected provider is "", so they still take foreign defaults. The user may cut it to just the init-level ADO test.

**D5. risk-only hardening beyond the literal criteria.**
- The additions: 4000-char description truncation (risk itself offers to drop it), bare-number api_version decode, api-version injection validation, 203/FedAuthRedirect handling, skipping an ambiguous identity, and erroring on an unknown status.
- The status-as-error item is shared with verification. mvp prunes all of these.
- Provisional default: include them all, because each sits inside an existing task's contract.
- The truncation is the one that changes user-visible output: it rewrites the PR body. Drop it if you want strict scope.

**D6. When doctor lands, and how strict its warning assertions are.**
- mvp: wave 1, with presence/absence asserts. Before the flip, RemoteCombination also emits "cannot open a PR", so exact counts would fail there.
- risk and verification: wave 4, after the flip, with exactly-one-warning rows and an end-to-end doctor test.
- Provisional default: wave 4. Presence asserts would let a false bitbucket warning ("auth_user is not set") ride alongside the right one. mvp's placement would buy c-3 in parallel.

**D7. Whether the hostguard tables get ADO rows.**
- mvp explicitly drops ADO rows from TestShipRefusesOffAllowlistAPIBase and the redact tables, on the grounds that function-level tests suffice.
- risk adds rows for open, comment, status, head-lookup and the identity lookup, plus a Server over-refusal row. verification has only a dispatch-level OpenPR refusal.
- Provisional default: include the rows (t-10). The table documents itself as covering "every path that reads the token". The identity lookup hits a second host (vssps), and only risk covers it.

**D8. What rides inside the atomic flip.**
- mvp puts the FindOpenPRByHead arm (headpr.go) and the AZURE_DEVOPS_EXT_PAT prose into the flip, giving 12 files.
- risk and verification land the headpr arm in wave 2 and the prose in wave 4.
- I checked headpr.go. It is not a guard scan site, and its default arm returns plain "unsupported provider", not the sentinel. So it must land no later than the flip, but both placements stay green.
- Provisional default: wave 2 (t-6). That keeps the flip to the files the guards force and makes c-6 testable a wave earlier.

**D9. How finely the ADO backend is split.**
- mvp: one task holding all six calls, arguing they share one helper and its registries.
- verification: open, plus a combined comment+status+dedupe task.
- risk: open, comment, and status+dedupe as three tasks.
- Provisional default: risk's three-way split (t-4, t-5, t-6) on top of a shared transport (t-3). mvp's t-1 would put 6 criteria into one review and serialize wave 1; the split keeps each task at 4 files or fewer and runs them in parallel.
