# azure-devops-provider: verification-lens draft

Lens: design backward from the test contracts. For each criterion I wrote the test that
would prove it first, then cut the smallest task that can make that test pass on its own.
This produced a pre-land / flip / consume shape:

- Waves 1–2 build every Azure DevOps (ADO) function as unexported code. Each is tested
  directly, so its contract is green before any switch knows the provider exists.
- Wave 3 is the guard flip (locked `guard_atomicity`). It holds only the enum member, the
  host-map entries, the case lines and the prompt bullets the guards compare.
- Wave 4 holds the contracts that are only observable after dispatch works: doctor, the
  cmd-level ship run and the prompt prose.

```
Phase azure-devops-provider — 9 tasks across 4 waves

Wave 1
  t-1  Add ADO remote parser + identity split
       files:    internal/project/azuredevops.go, internal/project/azuredevops_test.go
       desc:     New exported ParseAzureDevOpsRemote(raw) -> {URL (canonical https), APIBase, Org,
                 Base, Project, Repo, Services} for every Services/Server form. Uses the escaped
                 path (no decode/re-encode) and strips userinfo. Pure function with no callers yet.
       covers:   c-1, c-2
       contract: if a Services form mis-canonicalizes, TestParseAzureDevOpsRemote fails:
                   git@ssh.dev.azure.com:v3/acme/proj/widget  -> https://dev.azure.com/acme/proj/_git/widget
                   https://acme@dev.azure.com/acme/proj/_git/widget(.git) -> same URL, api_base https://dev.azure.com/acme
                   acme@vs-ssh.visualstudio.com:v3/acme/proj/widget -> https://acme.visualstudio.com/proj/_git/widget
                   https://acme.visualstudio.com/proj/_git/widget -> api_base https://acme.visualstudio.com, project proj
                   (host NOT rewritten to dev.azure.com: canonical_host)
                 if the one-segment rule breaks, https://dev.azure.com/acme/_git/widget no longer yields project==repo=="widget"
                 if percent-encoding is decoded or double-encoded, https://dev.azure.com/acme/My%20Proj/_git/My%20Repo
                   no longer round-trips byte-identical as URL (and its ssh v3 twin gains %2520), while Project/Repo
                   still decode to "My Proj"/"My Repo"
                 if the Server marker rule breaks, the /tfs/DefaultCollection row (api_base https://ado.corp.example/tfs/DefaultCollection)
                   or the host-root collection row (https://ado.corp.example/Coll/proj/_git/widget -> api_base https://ado.corp.example/Coll) fails;
                   ssh://ado.corp.example:22/tfs/DefaultCollection/proj/_git/widget -> https URL with the port dropped;
                   an explicit https port (:8443) is kept in URL and APIBase
                 if userinfo leaks, a https://me:<84-char PAT>@dev.azure.com/... row whose URL/APIBase contain "me" or the PAT fails
                 if non-ADO input is accepted, https://github.com/o/r, ".../_git/" (empty repo) and https://host/_git/r return ok=false

  t-2  Add ADO PAT rule + ADO known hosts
       files:    internal/secretscan/rules.go, internal/secretscan/secretscan_test.go,
                 internal/hostallow/hostallow.go, internal/hostallow/hostallow_test.go
       desc:     New "azure-devops-pat" rule: 76 alnum + "AZDO" + 4 alnum, word-bounded, needle "AZDO".
                 saasDefaults gain dev.azure.com, vssps.dev.azure.com and *.visualstudio.com.
       covers:   c-9, c-4
       contract: if the rule is missing, TestEveryRuleHasAHitCase fails: its rule-set == corpus-keys check forces an
                   "azure-devops-pat" row, and that row must yield exactly one hit of that rule
                 if the signature position is loose, 84-char runs with AZDO at offset 75 or 77, an 85-char run, and an 83-char
                   run each produce zero hits (benign rows), and "<your-pat>" / auth_env = "AZURE_DEVOPS_EXT_PAT" stay benign
                 if old tokens get a bare-shape rule, a bare 52-char [a-z2-7] string fires (must be zero hits) while
                   `token = <same 52 chars>` must still produce exactly one key-context hit (not azure-devops-pat)
                 if the rule is not shared by every sink, ScanPayload("POST x", {"title": <pat>}) and a Scan of a
                   persisted-artifact path holding the PAT do not return ErrHit / one hit
                 if a host is missing, Derive("", nil).Check rejects https://dev.azure.com/acme, https://vssps.dev.azure.com/acme
                   or https://acme.visualstudio.com; if the wildcard is a substring match, https://evil-visualstudio.com,
                   https://visualstudio.com.attacker.example, bare https://visualstudio.com, https://x.dev.azure.com and
                   https://acme.visualstudio.com:8443 are no longer refused (all must wrap ErrRefused)

  t-3  Add ADO REST transport (auth, screen, scrub, api-version)
       files:    internal/ship/azuredevops.go, internal/ship/azuredevops_test.go, internal/ship/open.go,
                 internal/ship/comment.go, internal/secretscan/sinks.go, internal/ship/redact_test.go
       desc:     adoReq (the ONLY ADO HTTP entry): ScanPayload before encode, exactly one auth header, appends
                 api-version (default 7.1), redact.Scrub on the body. Adds adoCredentials (via resolveToken) and the
                 OpenOpts/CommentOpts.APIVersion fields. Registers adoReq in Transports() and scrubbingHelpers.
       covers:   c-4
       contract: if a second or wrong credential header is sent, TestAzureDevOpsAuthHeader fails: default scheme ->
                   len(Header.Values("Authorization"))==1 and == "Basic "+base64(":"+tok), PRIVATE-TOKEN absent;
                   " Bearer " -> "Bearer tok"; "private-token" and "basic" -> still Basic :PAT (what doctor calls ineffective)
                 if the host check moves after os.Getenv, TestAzureDevOpsRefusesBeforeTokenRead fails: Hosts derived from
                   https://dev.azure.com/acme/proj/_git/widget with APIBase=httptest URL -> errors.Is(ErrRefused), server hits==0,
                   and with the env var UNSET the error is still the refusal, never "$X is not set"
                 if api-version is per-call-site, TestAzureDevOpsAPIVersionOnEveryCall fails: "" -> api-version=7.1,
                   "6.0" -> 6.0, and an endpoint already carrying ?searchCriteria.status=active keeps it with exactly one api-version
                 if screening is skipped, adoReq POST with a body holding a ghp_ token returns the secretscan error, hits==0
                 if the scrub is skipped, a 401 body echoing the token and base64(":"+tok) comes back with neither present
                 if adoReq is unregistered, TestEveryOutboundSeamIsRegistered / TestScreenPrecedesRequestInEveryScreenedTransport
                   (internal/cmd) and TestShipHelpersScrubAtSource (internal/ship) fail, naming adoReq
                 if api_base/auth_env are empty, adoCredentials errors naming [remote].api_base / [remote].auth_env with zero requests

Wave 2 (t-4 depends t-1, t-3; t-5 depends t-1, t-2, t-3)
  t-4  Open ADO PR with optional reviewers
       files:    internal/ship/azuredevops_open.go, internal/ship/azuredevops_open_test.go
       desc:     openAzureDevOpsPR: POST {api_base}/{project}/_apis/git/repositories/{repo}/pullrequests, then one PUT
                 .../pullrequests/{id}/reviewers/{rid} per reviewer (isRequired=false). A GUID passes through; an
                 email/UPN goes through the Identities API (adoIdentityBase). Browser URL = remote URL + /pullrequest/{id}.
       covers:   c-5, c-2
       contract: if the create payload is wrong, TestOpenAzureDevOpsPRPayload fails: sourceRefName "refs/heads/phase/x",
                   targetRefName "refs/heads/main", title, description; isDraft==true only when Draft; title has no "Draft:" prefix
                 if the result URL comes from the response's REST "url" (an _apis link), the assertion
                   URL == "https://dev.azure.com/acme/proj/_git/widget/pullrequest/42" and Number == 42 (pullRequestId) fails
                 if Server addressing breaks (c-2), a /tfs/DefaultCollection remote no longer posts to
                   /tfs/DefaultCollection/proj/_apis/git/repositories/widget/pullrequests
                 if encoded names are double-encoded, the My%20Proj/My%20Repo case's EscapedPath gains "%2520"
                 if reviewer resolution is wrong, TestOpenAzureDevOpsPRReviewers fails: ["<GUID>", "alice@corp.example"] ->
                   exactly one identities GET (filterValue=alice@corp.example, api-version present), the GUID never looked up,
                   and two PUTs whose bodies carry isRequired:false
                 if the identity base is wrong, TestAzureDevOpsIdentityBase fails: https://dev.azure.com/acme and
                   https://acme.visualstudio.com -> https://vssps.dev.azure.com/acme; https://ado.corp.example/tfs/DefaultCollection unchanged
                 if reviewer failure becomes fatal, TestOpenAzureDevOpsPRReviewerFailureNonFatal fails: identities 404, empty value[],
                   or a PUT 500 each return (result{Number:42}, non-nil err), while a create 500 returns (nil, err)

  t-5  ADO comment thread, PR status, head lookup
       files:    internal/ship/azuredevops_pr.go, internal/ship/azuredevops_pr_test.go, internal/ship/headpr.go
       desc:     postAzureDevOpsComment (POST .../pullRequests/{id}/threads, no status field), azureDevOpsPRStatus
                 (GET .../pullrequests/{id}), and adoOpenPRBySource. FindOpenPRByHead gains the "azure-devops" arm
                 (not a guard scan site) and the stale "deferred azure-devops arm" comment goes.
       covers:   c-6, c-7, c-8, c-2
       contract: if merged is "status != active", TestAzureDevOpsPRStatusMapping fails: completed->Merged true;
                   abandoned->false; active->false; BaseRef "release/x" from targetRefName "refs/heads/release/x"
                 if a lookup failure reads as "not merged", a 404 must return a non-nil error (never PRStatus{}, nil);
                   PRNumber 0 must error with zero requests
                 if the thread payload is wrong, TestPostAzureDevOpsThread fails: comments[0].content == body,
                   parentCommentId 0, commentType 1, and the decoded top level has NO "status" key (comment_thread_status)
                 if screening is bypassed, a body holding the t-2 84-char AZDO fixture returns the secretscan error, server hits==0
                 if errors leak, a 403 echoing the token and base64(":"+tok) yields an error containing neither
                 if the head query is wrong, TestFindOpenPRByHeadAzureDevOps fails: Provider "azure-devops" ->
                   searchCriteria.sourceRefName=refs/heads/phase/x and searchCriteria.status=active; value[0].pullRequestId 17 ->
                   OpenResult{17, <remote>/pullrequest/17}; empty value[] -> (nil, nil)
                 if a lookup failure falls through to create, a 500 must return non-nil err with
                   !errors.Is(err, ErrHeadPRLookupUnsupported) (that sentinel makes ship open a duplicate)
                 if the server ignores searchCriteria (old Server), a returned row whose sourceRefName != refs/heads/phase/x
                   must be skipped -> (nil, nil), never someone else's PR recorded

Wave 3 (depends t-1, t-4, t-5)
  t-6  Flip azure-devops into recognition + dispatch trio
       files:    internal/configenum/configenum.go, internal/configenum/configenum_test.go,
                 internal/project/remote.go, internal/project/remote_test.go,
                 internal/ship/open.go, internal/ship/comment.go, internal/ship/merged.go,
                 internal/ship/azuredevops_dispatch_test.go,
                 assets/prompts/init.md, assets/prompts/onboard.md
       desc:     One change (guard_atomicity): ShipProviders += azure-devops; KnownHostProviders += the ADO hosts;
                 DetectRemote routes ADO remotes through ParseAzureDevOpsRemote (canonical url, org api_base,
                 Public=false); case arms in OpenPR/PostComment/GetPRStatus; provider bullets in init/onboard.
       covers:   c-1, c-5, c-7, c-8, c-4
       contract: if any of the four sites lags ShipProviders, internal/cmd/enum_divergence_test.go names it:
                   TestShipDispatchMatchesShipProviders/{ship.OpenPR,ship.PostComment,ship.GetPRStatus},
                   TestRemoteAPIBaseMatchesShipProviders, TestRemoteWriterSubsetOfShipProviders
                 if the writer prompts lag, TestPromptProviderListsMatchShipProviders fails on init.md/onboard.md;
                   TestSetListRendersPipeJoined pins "github | forgejo | gitea | gitlab | bitbucket | azure-devops"
                 if autodetect is wrong, TestDetectRemote's ADO rows fail: the five Services forms -> provider "azure-devops",
                   canonical https URL, api_base = org URL, wantPublic=false (the known-host Public=true default must not
                   leak: autodetect_defaults); an ado.corp.example Server remote -> Provider "" (set by hand)
                 if userinfo persists, TestDetectRemoteDropsUserinfo's https://me:<PAT>@dev.azure.com/... row fails
                 if an arm mis-routes, TestAzureDevOpsDispatchRoutes fails: Provider " Azure-DevOps " (padded, mixed case)
                   through OpenPR -> POST .../_apis/git/repositories/widget/pullrequests; PostComment -> POST
                   .../pullRequests/7/threads; GetPRStatus -> reads targetRefName (a gitlab/forgejo mis-route hits
                   /projects/ or /repos/ and fails); OpenPR with an off-allowlist api_base -> ErrRefused, zero hits
                 if ADO wrongly gets a protection probe, the ShipProviders-iterating TestBaseChoresLegacyTable and
                   TestMilestoneHeadLegacyTable gain a provider=azure-devops subtest that must stay green with 0 probes

Wave 4 (t-7 depends t-1, t-6; t-8 depends t-3, t-6; t-9 depends t-6)
  t-7  Doctor: ADO origin equivalence + combination warnings
       files:    internal/cmd/doctor.go, internal/cmd/doctor_test.go, internal/diag/combinations.go,
                 internal/diag/diag_test.go
       desc:     sameRemoteURL canonicalizes both sides through project.ParseAzureDevOpsRemote when either parses.
                 RemoteCombination gains an azure-devops arm: private-token/basic flagged ineffective, stray auth_user
                 flagged. The bitbucket-oriented basic warnings are suppressed for ADO.
       covers:   c-3
       contract: if equivalence breaks, TestSameRemoteURL's ADO rows fail: ssh v3 vs https -> true; https://acme@... vs bare
                   -> true; .git suffix -> true; vs-ssh v3 vs acme.visualstudio.com https -> true; different repo -> false;
                   acme.visualstudio.com vs dev.azure.com same org/proj/repo -> false (canonical_host, no cross-host rewrite)
                 if doctor misreports end to end, TestDoctorAcceptsAzureDevOpsRemote fails: origin
                   git@ssh.dev.azure.com:v3/acme/proj/widget -> init autodetects -> doctor prints "git origin matches [remote].url"
                   with no "does not match" and no "cannot open a PR"; repointing origin at .../other prints "does not match"
                 if the combination arm is wrong, TestCombinationWarnings' ADO rows fail: ("azure-devops","","")
                   and ("azure-devops","bearer","") -> none; ("azure-devops","private-token","") -> one "no effect" warning;
                   ("azure-devops","basic","") -> exactly one warning, the no-effect one, and NOT "auth_user is not set" or
                   "sends no Basic credential" (both false for ADO); ("azure-devops","","me") -> one warning naming auth_user ignored

  t-8  Wire remote.api_version + ADO ship end-to-end
       files:    internal/project/project.go, internal/cmd/project.go, internal/cmd/project_test.go,
                 internal/cmd/ship.go, internal/cmd/ship_test.go
       desc:     Remote.APIVersion (`toml:"api_version"`); readDotted/writeDotted arms for remote.api_version;
                 buildOpenOpts/buildCommentOpts copy it. Refresh the Remote provider/auth_scheme doc comments.
                 Add the cmd-level ADO ship tests against an httptest org.
       covers:   c-5, c-6, c-2, c-4
       contract: if the field or dotted arm is missing, a project set-then-get of remote.api_version "6.0" fails or
                   project.toml lacks api_version = "6.0" under [remote]
                 if Server hand-config is refused (c-2), `project set remote.provider azure-devops` (enumKeys gate) errors
                 if wiring drops it, TestBuildOpenOptsMapsAzureDevOpsFields / TestBuildCommentOptsMapsAzureDevOpsFields
                   see APIVersion == ""
                 if any wire call misses config, TestShipAzureDevOpsFullFlow fails: every request carries api-version=6.0
                   and one Basic :PAT header; the sourceRefName GET (empty) precedes the create POST (pullRequestId 42);
                   stdout contains <srv>/acme/proj/_git/widget/pullrequest/42; changes.json records 42
                 if dedupe is not honored at ship level, a GET returning pullRequestId 17 -> no POST seen and 17 recorded;
                   a GET 500 -> ship errors "could not check origin" and no POST is seen
                 if reviewer failure fails the ship, identities 404 -> exit 0, "Warning:" printed, PR 42 still recorded

  t-9  Teach init/onboard/options prompts ADO
       files:    assets/prompts/init.md, assets/prompts/onboard.md, assets/prompts/options.md,
                 internal/cmd/prompt_provider_list_test.go, internal/cmd/options_docs_test.go
       desc:     Host mapping (dev.azure.com / *.visualstudio.com -> azure-devops), api_base = org URL, default auth_env
                 AZURE_DEVOPS_EXT_PAT, public=false. Server is configured by hand with remote.api_version (2019 5.0 /
                 2020 6.0 / 2022 7.0). options.md lists azure-devops and remote.api_version. Prompt-only (rule r-01: make install).
       covers:   c-1
       contract: if the prose is missing, TestPromptsTeachAzureDevOps fails: init.md and onboard.md each contain
                   "dev.azure.com", "visualstudio.com", "AZURE_DEVOPS_EXT_PAT" and "remote.api_version"
                 if options.md drops the provider, the same test fails on its missing "azure-devops"; and
                   TestOptionsCoversTheProviderConditionalRemoteFields (extended with "remote.api_version") fails
```

## Coverage

| Criterion | Tasks | Anchoring test(s) |
|---|---|---|
| c-1 autodetect + canonical + prompts | t-1, t-6, t-9 | TestParseAzureDevOpsRemote, TestDetectRemote (ADO rows), TestPromptProviderListsMatchShipProviders, TestPromptsTeachAzureDevOps |
| c-2 Server by hand, derived identity | t-1, t-4, t-5, t-8 | TestParseAzureDevOpsRemote (/tfs/DefaultCollection + host-root rows), Server-form path assertions in t-4/t-5, `project set remote.provider azure-devops` |
| c-3 doctor equivalence + combos | t-7 | TestSameRemoteURL, TestDoctorAcceptsAzureDevOpsRemote, TestCombinationWarnings |
| c-4 one header, allowlist before token | t-2, t-3, t-6, t-8 | TestAzureDevOpsAuthHeader, TestAzureDevOpsRefusesBeforeTokenRead, hostallow built-in rows, dispatch-level refusal in TestAzureDevOpsDispatchRoutes |
| c-5 open PR + URL + optional reviewers | t-4, t-6, t-8 | TestOpenAzureDevOpsPRPayload, TestOpenAzureDevOpsPRReviewers, TestOpenAzureDevOpsPRReviewerFailureNonFatal, TestShipAzureDevOpsFullFlow |
| c-6 dedupe by sourceRefName, fail-closed | t-5, t-8 | TestFindOpenPRByHeadAzureDevOps, TestShipAzureDevOpsFullFlow (17-recorded and 500-refuses variants) |
| c-7 comment as thread, screened, token-free errors | t-5, t-6 | TestPostAzureDevOpsThread, TestAzureDevOpsDispatchRoutes (PostComment) |
| c-8 merged only when completed + base ref | t-5, t-6 | TestAzureDevOpsPRStatusMapping, TestAzureDevOpsDispatchRoutes (GetPRStatus) |
| c-9 new-format PAT rule | t-2 | TestEveryRuleHasAHitCase, benign position/length rows, ScanPayload + artifact rows |

9/9 criteria covered. The locked decisions each have a contract:
- `guard_atomicity`: t-6.
- `repo_identity` and `canonical_host`: t-1 and t-7.
- `auth_scheme`: t-3 and t-7.
- `api_version`: t-3 and t-8.
- `reviewers`: t-4.
- `comment_thread_status`: t-5.
- `autodetect_defaults`: t-6 and t-9.
- `transport`: t-3.
- `verify_posture`: every contract here is an httptest mock. The live smoke run belongs to verify and is not a task.

## Judgment calls

- **Pre-land the implementations, keep the flip thin.** Chose: every ADO function lands unexported and tested directly in waves 1–2. The flip adds only literals the guards compare. Rejected: building the provider inside the flip (one ~15-file task that can't be reviewed). Also rejected: landing case arms with "not implemented" stubs, which would ship a provider that dispatches but is broken. Why: the guards compare case literals, not bodies, so the bodies can come first without any red commit.
- **The flip carries init.md/onboard.md bullets and the configenum List test.** Chose: put them in t-6. Rejected: deferring them to the prompt task. Why: TestPromptProviderListsMatchShipProviders and TestSetListRendersPipeJoined are set-equality guards outside enum_divergence_test.go. They go red the moment ShipProviders gains a member, so they belong to the same atomic change.
- **The FindOpenPRByHead arm lands in t-5, not the flip.** Chose: wave 2. Rejected: wave 3. Why: headpr.go is not an enumScanSites entry, so the arm is guard-neutral. Landing it early lets c-6's dispatch-level contract run a wave sooner and shrinks the flip.
- **One parser in internal/project, imported by ship and doctor.** Rejected: a second parser in ship, because two parsers drift on %-encoding, which is exactly the c-1 round-trip bug. Rejected: a new leaf package, which is unnecessary. project has no internal imports, and ship already reaches project through phase, so there's no cycle.
- **How the one-segment rule reads on visualstudio.com.** Chose: the org is in the host, so `acme.visualstudio.com/proj/_git/widget` has an empty base and project "proj". Rejected: the literal reading (base "proj", project == repo). Why: c-1 requires api_base to be "the org URL" for legacy hosts, so the literal reading would break the criterion. t-1 pins this with a row. It interprets repo_identity rather than overriding it.
- **api-version is appended inside adoReq.** Rejected: appending it in each endpoint builder. Why: putting it at the single transport entry makes "every call" true by construction. The test pins exactly one api-version when a query string already exists.
- **Reviewers are added with one PUT each after create.** Rejected: putting reviewers in the create payload. Why: this keeps "assignment failed" separate from "PR failed" and non-fatal, like bitbucket's follow-up update.
- **The head lookup re-checks sourceRefName and status client-side.** Rejected: trusting the server-side searchCriteria filter. Why: an older Server that ignores an unknown query param would otherwise return someone else's active PR, and ship would record it. That is the false dedupe c-6 exists to prevent.
- **hostallow built-ins share a task with the PAT rule (t-2).** Rejected: two separate tasks. Why: each is a fixed security table in a leaf package, and on its own each is a one-file change under 10 minutes.
- **t-3 adds the OpenOpts/CommentOpts.APIVersion fields and touches 6 files.** Why: the two wave-2 tasks can then both compile without either editing open.go or comment.go. Four of the six files are one-line, guard-forced edits: two registry entries and two struct fields.
- **No telemetry "azure-devops backend" bucket.** Rejected: following the GitLab precedent. Why: no criterion asks for it, and adding it would push t-7 past 5 files. A judge can add it to t-7 if wanted.
- **The cmd-level end-to-end test lives with the api_version wiring (t-8).** Why: it's the only test that proves api_version flows from project.toml to every wire call. It's also the only place c-5's "printed" and c-6's "stops the ship" can be observed past the provider seam.
- **Doctor `basic` on ADO emits only the ineffective warning.** Rejected: letting the existing bitbucket rules fire too. Why: "auth_user is not set — every call will 401" and "sends no Basic credential" are both false for ADO, which sends Basic :PAT. The contract asserts exactly one warning.
- **Functions that sit unreferenced between t-3 and t-6 are accepted.** Why: `go vet` (the typecheck gate) doesn't flag unexported functions with no callers. Only the quality deadcode analyzer would flag them, and only until t-6 lands.
