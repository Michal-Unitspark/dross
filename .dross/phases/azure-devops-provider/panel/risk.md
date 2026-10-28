# Risk-lens draft — azure-devops-provider

Lens: failure modes drive the graph. Every ADO-specific way this can break (a misparsed
remote, a second credential header, a duplicate PR, an abandoned PR read as merged, a
token in an error) is owned by one task, and that task's contract is the test that goes
red. The locked `guard_atomicity` decision fixes the shape. Every behaviour lands
**unwired** in waves 1–2, where each risk can be tested in isolation. Wave 3 is the one
mechanical flip commit that makes azure-devops dispatchable. Wave 4 proves the result at
the command boundary.

```
Phase azure-devops-provider — 11 tasks across 4 waves

Wave 1
  t-1  Parse Azure DevOps remotes into canonical identity
       files:    internal/project/azure.go, internal/project/azure_test.go,
                 internal/project/remote.go, internal/project/remote_test.go
       covers:   c-1, c-2
       desc:     Pure ADO canonicalizer: any Services or Server form -> canonical https URL,
                 api_base, decoded project/repo, is-Services predicate; fed by parseGitRemote's
                 taint-cleared (host, path), which switches to EscapedPath. Not wired yet.
       contract: TestParseAzureRemoteForms: if any form regresses, its row fails. The rows
                   are https, https user@, https o:PAT@ (canonical must not contain "PAT"),
                   git@ssh.dev.azure.com:v3/o/p/r, ssh://git@ssh.dev.azure.com/v3/o/p/r, and
                   .git suffix. Every row must give https://dev.azure.com/o/p/_git/r with
                   api_base https://dev.azure.com/o.
                 TestParseAzureLegacyHostCarriesOrg: if the one-segment rule is applied to a
                   host that carries the org, the test fails. Inputs: o.visualstudio.com/p/_git/r
                   must give api_base https://o.visualstudio.com and project p (NOT base=p);
                   o.visualstudio.com/DefaultCollection/p/_git/r must give api_base
                   .../DefaultCollection; o@vs-ssh.visualstudio.com:v3/o/p/r must give
                   https://o.visualstudio.com/p/_git/r (it is NOT rewritten to dev.azure.com).
                 TestParseAzurePercentEncoding: if names are decoded into the URL or
                   double-encoded, the test fails. Input .../o/My%20Project/_git/My%20Repo must
                   keep "My%20Project" in the canonical URL (never "My Project", never
                   "My%2520Project") and give Project()=="My Project". The ssh form with %20
                   must give the same result.
                 TestParseAzureServer: if collection depth or port handling breaks, the test
                   fails. https://tfs.corp/tfs/DefaultCollection/P/_git/R must give api_base
                   https://tfs.corp/tfs/DefaultCollection. https://tfs.corp/Coll/P/_git/R must
                   give https://tfs.corp/Coll. ssh://tfs.corp:22/tfs/DefaultCollection/P/_git/R
                   must canonicalize with the port dropped, and https://tfs.corp:8443/... must
                   keep :8443.
                 TestParseAzureOneSegmentBase: if the repo==project rule breaks, the test fails.
                   dev.azure.com/o/_git/r must give project r and repo r, with api_base
                   https://dev.azure.com/o.
                 TestParseAzureRejects: if a malformed URL yields a half-filled identity, the
                   test fails. Each of these must return ok=false/err: no _git segment, _git as
                   the last segment, an empty segment ("o//_git/r"), a "." or ".." segment,
                   dev.azure.com/_git/r (no org), and github.com/x/y.
                 TestAzureServicesHost: if the host predicate is substring-matched, the test
                   fails. evil-visualstudio.com, visualstudio.com.evil.example and
                   dev.azure.com.evil.example must all be false.
                 TestDetectRemote/TestDetectRemoteDropsUserinfo rows stay unchanged; if the
                   EscapedPath switch alters a non-ADO remote, they fail.

  t-2  Teach secretscan and hostallow the ADO shapes
       files:    internal/secretscan/rules.go, internal/secretscan/secretscan_test.go,
                 internal/hostallow/hostallow.go, internal/hostallow/hostallow_test.go
       covers:   c-9, c-4
       desc:     New `azure-devops-pat` rule (84 word-bounded alnum, AZDO at 76–79, needle "AZDO",
                 runtime-built fixtures); hostallow saasDefaults gain dev.azure.com,
                 vssps.dev.azure.com and *.visualstudio.com.
       contract: TestEveryRuleHasAHitCase: if the rule is missing or misnamed, the test
                   fails. The hitCorpus row (runtime-built 76+"AZDO"+4) must yield exactly one
                   hit, of rule azure-devops-pat, and TestRedactCoversEveryRule redacts it.
                 TestAzurePATOffByOne: if the anchor drifts, the test fails. These must yield
                   zero azure-devops-pat hits: AZDO at index 75 or 77, 83- and 85-char runs,
                   and lowercase "azdo".
                 benignCorpus rows: if a reference trips the rule, a row fails. The rows are
                   "<your-pat>", "$AZURE_DEVOPS_EXT_PAT", `auth_env = "AZURE_DEVOPS_EXT_PAT"`,
                   and a bare 52-char lowercase base32 string (zero hits). A separate test
                   asserts `token = <52-char high-entropy>` still hits key-context (old tokens
                   are left to that rule).
                 TestAzurePATEverySink: if a sink stops screening, the test fails.
                   ScanPayload over {"title": PAT} and {"body": PAT} must return ErrHit naming
                   the field. Scan over a project.toml line `auth_env = "<PAT>"` (the persisted
                   artifact) must hit.
                 TestEveryRuleHasNeedles: if the needle is dropped, the test fails. candidates()
                   must prune a line without "AZDO".
                 TestADOBuiltinHosts: if the wildcard boundary or port pinning breaks, the test
                   fails. Check must accept https://dev.azure.com/o,
                   https://vssps.dev.azure.com/o, https://o.visualstudio.com and
                   https://o.vssps.visualstudio.com. It must refuse https://evil-visualstudio.com,
                   https://visualstudio.com.attacker.example, bare https://visualstudio.com,
                   https://o.visualstudio.com:8443 and http://dev.azure.com, each with
                   ErrRefused.

  t-3  Build ADO REST transport with single-credential auth
       files:    internal/ship/azure.go, internal/ship/azure_test.go, internal/ship/open.go,
                 internal/ship/comment.go, internal/secretscan/sinks.go, internal/ship/redact_test.go
       covers:   c-4
       desc:     azure.go: adoCredentials -> resolveToken; Basic ":PAT" / Bearer auth; validated api-version
                 (default 7.1); adoRequest (ScanPayload, FedAuthRedirect: Suppress, no redirects, redact.Scrub);
                 adoRepoEndpoint. OpenOpts/CommentOpts gain APIVersion; adoRequest registered in both walkers.
       contract: TestADOAuthHeaderExactlyOne: if a second credential or the wrong scheme leaks
                   in, the test fails. For scheme "", "private-token", " BASIC " the mock must
                   see len(r.Header["Authorization"])==1 with value Basic base64(":"+tok) and no
                   Private-Token header. For "bearer" and " Bearer " it must see exactly
                   Authorization: Bearer tok.
                 TestADOAPIVersionOnEveryCall: if api-version is missing, duplicated or
                   injectable, the test fails. With no query the request must carry
                   ?api-version=7.1. With an existing ?searchCriteria.status=active query,
                   Query()["api-version"] must be ["7.1"] exactly once. "6.0" and
                   "7.1-preview.1" must pass through. "7.1&x=y", "7.1#", " " and "../" must
                   error with ZERO mock requests and before the env var is read.
                 TestADORefusesOffAllowlistAPIBase: if the host check moves after the getenv,
                   the test fails. With a policy derived from https://dev.azure.com/o/p/_git/r
                   and api_base=httptest, the error must satisfy errors.Is(ErrRefused), the
                   fake host must count 0 hits, and the error must contain neither the token
                   nor the env name.
                 TestADOSignInIsAuthFailure: if a 203 HTML sign-in page or a 302 is read as
                   success, the test fails. Both must return an auth error. The 302 target
                   server must record 0 requests, so Authorization is never forwarded. The
                   request must carry X-TFS-FedAuthRedirect: Suppress.
                 TestADOBodyScrubbed (mirrorAuthServer, 403): if the scrub is dropped, the test
                   fails. The returned error must contain neither the PAT nor base64(":"+PAT).
                   Removing adoRequest from scrubbingHelpers must fail
                   TestShipHelpersScrubAtSource. Moving ScanPayload after http.NewRequest must
                   fail the transport walker in secretscan_transport_enum_test.
                 TestADORepoEndpointEscapesOnce: if names are double-escaped or unescaped, the
                   test fails. ("https://dev.azure.com/o","My Project","My Repo") must give
                   .../o/My%20Project/_apis/git/repositories/My%20Repo (never %2520). "#" and
                   "?" in a name must be escaped.

  t-4  Add [remote].api_version with tolerant TOML decode
       files:    internal/project/project.go, internal/project/project_test.go,
                 internal/cmd/project.go, internal/cmd/project_test.go,
                 internal/cmd/testdata/pathfence_scan/not_paths.txt
       covers:   c-2
       desc:     Remote gains APIVersion (api_version), decoding a quoted string or a bare TOML
                 number (6.0 -> "6.0"); readDotted/writeDotted arms; not_paths ledger row.
       contract: TestRemoteAPIVersionBareFloat: if a hand-edited `api_version = 6.0` breaks
                   load (the project_id "incompatible types" failure in the telemetry corpus)
                   or loses ".0", the test fails. 5.0 must give "5.0", 6.0 must give "6.0",
                   7.1 must give "7.1", and "7.1-preview.1" must stay verbatim.
                   `api_version = true` must be a load error naming remote.api_version.
                 TestExpandedDottedPathsRoundTrip gains remote.api_version: if the set/get arm
                   is missing, set 6.0 followed by get no longer returns "6.0". The lossless
                   save test asserts the written line is `api_version = "6.0"` and every other
                   line stays byte-identical.
                 Omitted field: an existing project.toml must save with no new api_version line.
                   If omitempty is lost, the lossless-save tests fail.
                 TestEveryPathShapedFieldIsDeclared: if project.Remote.APIVersion is not filed
                   in not_paths.txt, the test fails.

Wave 2 (depends t-1, t-3)
  t-5  Open ADO PR and add optional reviewers
       files:    internal/ship/azure_open.go, internal/ship/azure_open_test.go
       covers:   c-5
       desc:     openAzurePR: POST .../pullrequests -> Number=pullRequestId, URL=<canonical>/pullrequest/<id>.
                 Reviewers follow one by one: GUID as-is, else Identities at adoIdentityBase(api_base),
                 then PUT .../reviewers/<id> {isRequired:false}. Response structs function-local.
       contract: TestOpenAzurePRPayload: if the create body is wrong, the test fails. The mock
                   decodes the body and asserts sourceRefName "refs/heads/phase/x" (a head
                   already prefixed is not doubled), targetRefName "refs/heads/main", title and
                   description. Draft=false must send no isDraft key (older Servers); Draft=true
                   must send isDraft:true. The create body must have NO reviewers key.
                 TestOpenAzurePRResultURL: if the API "url" (_apis/...) is recorded instead of
                   the browser URL, the test fails. pullRequestId 42 must give
                   https://dev.azure.com/o/p/_git/r/pullrequest/42, and a My%20Repo URL must
                   stay single-encoded. A response with no pullRequestId must return a nil
                   result and an error. A 409 (TF401179, active PR exists) must return a nil
                   result and an error.
                 TestOpenAzurePRLongDescription: if a >4000-char body is sent raw, ADO 400s and
                   the ship dies. The sent description must be <=4000 runes and end in a
                   truncation note, with no split multi-byte rune.
                 TestOpenAzurePRReviewerFailureNonFatal: if a reviewer failure becomes fatal or
                   the wrong person is added, the test fails. A GUID must cause 0 identity
                   requests. An email must cause GET .../_apis/identities?searchFilter=General
                   &filterValue=<escaped>, then a PUT with the returned id. Identity responses
                   with 0 matches, 2 matches, or a 500, and a 500 on the PUT, must each leave the
                   result non-nil with Number 42 AND return a non-nil error naming the reviewer
                   but not the token. The remaining reviewers must still be added.
                 TestADOIdentityBase: if the identity host derivation is wrong, the test fails.
                   https://dev.azure.com/o and https://o.visualstudio.com must both give
                   https://vssps.dev.azure.com/o. https://tfs.corp/tfs/Coll must map to itself.

  t-6  Post ADO comment as non-blocking thread
       files:    internal/ship/azure_comment.go, internal/ship/azure_comment_test.go
       covers:   c-7
       desc:     postAzureComment POSTs .../pullRequests/<n>/threads with
                 {comments:[{parentCommentId:0, content, commentType:1}]} and no status. The
                 thread status is one named const, so the comment_thread_status fallback to
                 "closed" is a one-line change.
       contract: TestPostAzureCommentThreadShape: if the endpoint or payload drifts, the test
                   fails. For PRNumber=7 the path must be
                   /{project}/_apis/git/repositories/{repo}/pullRequests/7/threads with
                   api-version, and comments[0].content must equal Body. The top-level body
                   must have NO "status" key. A status of active (blocking under a
                   comment-resolution policy) must fail the test.
                 TestPostAzureCommentScreened: if the body escapes the scanner, the test fails.
                   A body holding a glpat-shaped token must return secretscan.ErrHit and the
                   mock must record 0 requests.
                 TestPostAzureCommentErrorRedacted (mirrorAuthServer, 403): if the error path
                   leaks, the test fails. The error must contain neither the PAT nor
                   base64(":"+PAT). A 203 sign-in page must return an error, never a "posted".

  t-7  Read ADO PR status and dedupe by source
       files:    internal/ship/azure_pr.go, internal/ship/azure_pr_test.go,
                 internal/ship/headpr.go, internal/ship/headpr_test.go
       covers:   c-6, c-8
       desc:     azurePRStatus: Merged iff status==completed; BaseRef = targetRefName minus refs/heads/.
                 azureOpenPRBySource queries active PRs by sourceRefName, re-checks the match client-side;
                 FindOpenPRByHead gains the azure-devops arm (not an enum-guard site).
       contract: TestAzurePRStatusMapping: if abandoned or active reads as merged, the test
                   fails. completed and "Completed" must give Merged=true; abandoned and active
                   must give false. An empty or missing status, or a non-JSON body, must return
                   an error (git-ancestry fallback), never a silent false. PRNumber 0 must
                   return an error with 0 requests.
                 TestAzurePRStatusBaseRef: if refs/heads/ is not stripped, or is stripped
                   twice, the test fails. refs/heads/release/x must give release/x, and a
                   retargeted PR must return its current targetRefName.
                 TestFindOpenPRByHeadAzureQuery: if the filter is misnamed or unescaped, the
                   test fails. The mock asserts searchCriteria.sourceRefName ==
                   refs/heads/phase/x (a head containing "&" or "+" must round-trip) and
                   searchCriteria.status == active. A match must return its Number and URL
                   <canonical>/pullrequest/<id>.
                 TestFindOpenPRByHeadAzureIgnoredFilter: if the server filter is trusted
                   blindly, a foreign PR gets recorded. A mock that ignores the query and
                   returns an active PR from refs/heads/other, an abandoned PR from phase/x,
                   and a forkSource PR from phase/x must give (nil, nil).
                 TestFindOpenPRByHeadAzureFailureIsError: if a failed lookup reads as "none" or
                   "unsupported", ship opens a duplicate. A 500, a 203 sign-in page and an
                   unparseable body must each return a non-nil error that is
                   !errors.Is(ErrHeadPRLookupUnsupported). An empty value list must give
                   (nil, nil).

Wave 3 (depends t-1, t-5, t-6, t-7)
  t-8  Register azure-devops across recognition and dispatch atomically
       files:    internal/configenum/configenum.go, internal/configenum/configenum_test.go,
                 internal/project/remote.go, internal/project/remote_test.go,
                 internal/ship/open.go, internal/ship/comment.go, internal/ship/merged.go,
                 internal/ship/azure_dispatch_test.go, assets/prompts/init.md, assets/prompts/onboard.md
       covers:   c-1
       desc:     ONE commit (guard_atomicity): ShipProviders + KnownHostProviders gain azure-devops;
                 DetectRemote routes Services forms via t-1 (api_base org URL, Public=false); trio arms;
                 init/onboard provider bullets and host mapping. Wiring only, no new logic.
       contract: Enum guards green in this single commit: TestShipDispatchMatchesShipProviders,
                   TestRemoteAPIBaseMatchesShipProviders, TestRemoteWriterSubsetOfShipProviders,
                   TestGuardsSeeNonEmptySets and TestDispatchUsesConfigenumNormalize. If ADO host
                   recognition inlines a tagged `switch host` ahead of DetectRemote's provider
                   switch, providerSwitchIn reads the wrong cases and the guard fails.
                 TestPromptProviderListsMatchShipProviders (init.md + onboard.md) and
                   TestSetListRendersPipeJoined ("github | forgejo | gitea | gitlab | bitbucket
                   | azure-devops"): if a list lags the Set, its test fails.
                 TestDetectRemote ADO rows: if autodetect writes the wrong fields, a row fails.
                   The https, user@, ssh v3, legacy https and vs-ssh rows must each give
                   provider azure-devops, Public=false (NOT the known-host true), the canonical
                   URL and the org-URL api_base. A Server /tfs/Coll/P/_git/R row must stay
                   Provider "" (manual config, c-2).
                 TestAzureDispatchNormalises: if any trio arm is missing or un-normalised, the
                   test fails. OpenPR, PostComment and GetPRStatus with Provider
                   " Azure-DevOps " must return the ADO backend's own config error, never
                   "unsupported provider".
                 TestAzureDeferredSurfacesFailClosed: if a deferred surface returns an empty
                   result with a nil error, milestone-dependents reads "no dependents" and
                   deletes a branch. ListPRComments, PRHeadOf, AuthenticatedUser and
                   OpenPRsTargeting for azure-devops must each return a non-nil error.
                 TestNoSpawnOutputEscapes stays green. If the git origin reaches project.toml
                   through an ADO path that bypasses parseGitRemote's taint-cleared site, the
                   gate fails.

Wave 4 (depends t-8; t-9 also t-1, t-10 also t-4)
  t-9  Diagnose ADO remotes in doctor and options
       files:    internal/diag/combinations.go, internal/diag/diag_test.go,
                 internal/cmd/doctor.go, internal/cmd/doctor_test.go,
                 assets/prompts/options.md, internal/cmd/options_docs_test.go
       covers:   c-3, c-1
       desc:     ADO origin match compares t-1 canonical forms (names case-insensitive, host kept);
                 RemoteCombination gains an ADO arm plus base-vs-api_base check; mismatch lines strip
                 userinfo. options.md documents azure-devops, api_version and the ADO auth_scheme rules.
       contract: TestDoctorADOOriginEquivalent: if equivalent forms read as mismatched, the test
                   fails. With url https://dev.azure.com/o/p/_git/r, each of these origins must
                   print "git origin matches [remote].url": git@ssh.dev.azure.com:v3/o/p/r,
                   https://user@dev.azure.com/o/p/_git/r, .../r.git, and a case-variant O/P/R.
                   vs-ssh against https on o.visualstudio.com must match. Server
                   ssh://tfs:22/tfs/C/P/_git/R against https://tfs/tfs/C/P/_git/R must match.
                 TestDoctorADOOriginMismatch: if the comparison over-matches, the test fails.
                   A different repo, a different project, and dev.azure.com/o vs
                   o.visualstudio.com (no cross-host equivalence, per canonical_host) must each
                   report "does not match". An azure-devops url without _git must be reported
                   as an issue, never as "matches".
                 TestDoctorMismatchStripsUserinfo: if a PAT embedded in origin is echoed, the
                   test fails. For origin https://o:SECRETPAT@dev.azure.com/o/p/_git/other the
                   output must not contain SECRETPAT.
                 TestRemoteCombinationAzure table: if a warning is wrong or missing, its row
                   fails. ("","") and ("bearer","") must warn nothing. ("private-token","") must
                   give exactly one has-no-effect warning. ("basic","") must give exactly one
                   ineffective warning, and must NOT give "every ship call will 401" or "sends
                   no Basic credential", which are false for ADO. ("","someone") must give one
                   auth_user-ignored warning. The bitbucket and gitlab rows stay unchanged.
                 TestRemoteCombinationAzureBaseMismatch: if a hand-set Server api_base that
                   misses the collection goes unflagged, the test fails. url
                   https://tfs/tfs/C/P/_git/R with api_base https://tfs/tfs must warn and name
                   https://tfs/tfs/C. A trailing-slash or case-only difference must stay silent.
                 TestDoctorCarriesNoProviderLiterals stays green (no hand-typed accept-set).
                   TestOptionsCoversTheProviderConditionalRemoteFields gains remote.api_version
                   and azure-devops; if options.md omits either, the test fails.

  t-10 Ship azure-devops end to end through cmd
       files:    internal/cmd/ship.go, internal/cmd/ship_test.go, internal/ship/hostguard_test.go
       covers:   c-2, c-4, c-5, c-6, c-7
       desc:     buildOpenOpts and buildCommentOpts copy remote.api_version. Cmd-level flows
                 run the REAL FindOpenPRByHead/OpenPR against an httptest ADO Server-shaped
                 remote. The hostguard tables gain ADO rows for every ADO entry point.
       contract: TestBuildOpenOptsCarriesAPIVersion / TestBuildCommentOptsCarriesAPIVersion: if
                   the field is dropped, the test fails (a 2019 Server would get 7.1 and 400).
                   api_version "6.0" must reach opts.APIVersion.
                 TestShipAzureFullFlow: if wiring or addressing breaks, the test fails. The
                   remote is url srv/coll/P/_git/R, api_base srv/coll, api_version 6.0. Ship
                   must GET pullrequests and then POST pullrequests. Every request path must
                   start /coll/P/_apis/git/repositories/R and carry api-version=6.0. The output
                   must contain "PR opened: .../_git/R/pullrequest/42 (#42)" and changes.json pr
                   must be 42.
                 TestShipAzureDedupeHit: if dedupe falls through to create, the test fails. The
                   mock GET returns active PR 17 for refs/heads/phase/<id>. The result must be
                   0 POSTs, recorded pr 17, and the narration "found open PR #17".
                 TestShipAzureLookupFailureRefuses: if a failed lookup opens anyway, the test
                   fails. A GET returning 500 must make ship refuse, naming `dross ship <id>`,
                   with 0 POSTs and pr still 0.
                 TestShipAzureCommentPostsThread: if `dross ship comment` misroutes, the test
                   fails. It must POST .../pullRequests/<n>/threads.
                 TestShipRefusesOffAllowlistAPIBase gains azure-devops open, comment, status and
                   head-lookup rows. If any ADO entry point (identity lookup included) reads the
                   token before the host check, its row sees >0 fake-host hits.
                   TestShipAllowsDerivedSelfHostedHost gains an ADO Server row; if it is
                   over-refused, the row fails.

  t-11 Seed ADO remotes without foreign credentials
       files:    internal/defaults/defaults.go, internal/defaults/defaults_test.go,
                 internal/cmd/init_test.go, assets/prompts/init.md, assets/prompts/onboard.md,
                 internal/cmd/prompt_provider_list_test.go
       covers:   c-1, c-4
       desc:     defaults.Apply skips auth_env/reviewers when the saved defaults' provider differs from
                 the detected one; init.md/onboard.md name AZURE_DEVOPS_EXT_PAT as the azure-devops
                 auth_env.
       contract: TestApplySkipsForeignProviderCredentials: if a foreign auth_env or reviewers
                   seed through, the test fails. Defaults {github, GITHUB_TOKEN, [alice]} applied
                   to a detected azure-devops remote must leave AuthEnv "" and Reviewers nil.
                   Same-provider defaults must still seed, and the existing
                   TestApplyOnlyFillsZeroFields subtests must stay green.
                 TestInitDetectsAzureRemote: if init persists the wrong remote, the test fails.
                   Use origin git@ssh.dev.azure.com:v3/o/p/r and a saved GitHub defaults.toml.
                   project.toml must hold url https://dev.azure.com/o/p/_git/r, provider
                   azure-devops and api_base https://dev.azure.com/o, with public absent/false
                   and no GITHUB_TOKEN.
                 TestInitNeverPersistsOriginPAT: if the userinfo token leaks into project.toml,
                   the test fails. For origin https://o:PATVALUE@dev.azure.com/o/p/_git/r, the
                   project.toml bytes must not contain PATVALUE.
                 TestPromptsNameAzureAuthEnv: if the locked default drifts, the test fails.
                   init.md and onboard.md must both contain AZURE_DEVOPS_EXT_PAT on their
                   auth_env bullets.
```

## Risk ownership

Each failure mode has exactly one owning task. Its contract is where the failure goes red.

| Failure mode | Owner |
|---|---|
| A remote form misparsed (ssh v3, vs-ssh, user@, .git, /tfs, host-root, port) | t-1 |
| A legacy visualstudio.com remote misread under the one-segment rule | t-1 |
| %20 decoded into the URL or double-encoded on the round-trip | t-1 |
| `..` or an empty segment injected into identity | t-1 |
| A PAT in origin userinfo surviving canonicalization | t-1 |
| A PAT in origin userinfo surviving into project.toml after init | t-11 |
| An ADO PAT not caught (or a false positive) in title, body, comment or artifact | t-2 |
| The *.visualstudio.com wildcard admitting a lookalike host or odd port | t-2 |
| Two credential headers, or the wrong scheme | t-3 |
| api-version missing, duplicated, or used for query injection | t-3 |
| The token read before the host check (transport) | t-3 |
| The token read before the host check (any dispatched entry point) | t-10 |
| A 203/302 sign-in page read as success, or Authorization forwarded on redirect | t-3 |
| The token in an error body (raw or base64) | t-3 transport, t-6 comment path |
| A bare `api_version = 6.0` breaking every command, or losing ".0" | t-4 |
| refs/heads/ missing or doubled; isDraft sent to an old Server | t-5 |
| The API url recorded instead of the browser URL | t-5 (open), t-7 (dedupe) |
| A >4000-char body 400ing the ship | t-5 |
| A reviewer failure failing the ship, or an ambiguous identity adding the wrong person | t-5 |
| A thread created blocking (status active) | t-6 |
| Abandoned or active read as merged; unknown status read as not-merged | t-7 |
| An ignored server filter recording a foreign PR | t-7 |
| A lookup failure read as "none" or "unsupported", opening a duplicate | t-7 unit, t-10 integration |
| The enum guard red mid-phase; DetectRemote's first switch hijacked | t-8 |
| A deferred surface returning empty with a nil error | t-8 |
| Spawn output escaping through the new ADO path | t-8 |
| A doctor false match or false mismatch; wrong or misleading combination warnings | t-9 |
| A hand-set Server api_base missing the collection | t-9 |
| api_version dropped between config and opts | t-10 |
| A GITHUB_TOKEN from saved defaults seeded onto an ADO repo | t-11 |

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 | t-1, t-8, t-9, t-11 |
| c-2 | t-1, t-4, t-10 |
| c-3 | t-9 |
| c-4 | t-2, t-3, t-10, t-11 |
| c-5 | t-5, t-10 |
| c-6 | t-7, t-10 |
| c-7 | t-6, t-10 |
| c-8 | t-7 |
| c-9 | t-2 |

All 9 criteria are covered.

## Judgment calls

- **When the backends are built.** Chosen: build every backend unwired in waves 1–2 and flip the provider in one mechanical commit (t-8). Rejected: implementing the ADO logic inside the atomic task. Why: guard_atomicity forces a single commit, and keeping the logic out of it means each risk is tested in isolation and the flip commit carries no new behaviour.
- **t-8 exceeds the 5-file limit (10 files).** Chosen: accept it. Rejected: splitting it. Why: TestShipDispatchMatchesShipProviders, TestRemoteAPIBaseMatchesShipProviders, TestPromptProviderListsMatchShipProviders and TestSetListRendersPipeJoined all pin the same Set, so any split leaves a red commit.
- **t-3 lists 6 files.** Chosen: accept it. Why: four of those files are one-line registrations or fields (the sinks.go entry, the scrubbingHelpers entry, and the two APIVersion fields). The registry walkers fail unless those land with adoRequest.
- **Where the dedupe arm is wired.** Chosen: wire it in t-7. Rejected: wiring it in t-8. Why: FindOpenPRByHead is not an enumScanSites entry, so c-6 can be tested through the public entry. Before t-8, nobody can configure azure-devops (`project set` validates against ShipProviders).
- **Where the parser lives.** Chosen: a single parser in internal/project, used by DetectRemote, doctor and ship. Rejected: ship-local splitting in the style of bbRepoRef. Why: doctor and ship must not disagree about identity, and ship already depends on project transitively.
- **How the canonicalizer reads the remote.** Chosen: it consumes parseGitRemote's output, and parseGitRemote switches to EscapedPath. Rejected: a separate raw-string ADO parser. Why: a raw-string parser adds an unmarked spawn-output path that trips TestNoSpawnOutputEscapes, and url.Path would decode %20.
- **How repo_identity is read for visualstudio.com.** Chosen: the one-segment rule applies only where the host does not carry the org. Rejected: applying it uniformly. Why: uniformly, o.visualstudio.com/p/_git/r would parse as base=p, which contradicts c-1's "api_base (the org URL)". This interprets the lock; it does not override it.
- **Server short form (/tfs/Coll/_git/Repo).** Chosen: doctor warns when the url-derived base and the hand-set api_base disagree (t-9). Rejected: having the parser guess. Why: under the marker rule this form is ambiguous.
- **Legacy identity base.** Chosen: vssps.dev.azure.com/<org>. Rejected: <org>.vssps.visualstudio.com. Why: one built-in Services identity host, and every visualstudio.com org also answers at dev.azure.com/<org>.
- **How reviewers are added.** Chosen: a follow-up PUT per reviewer. Rejected: putting reviewers in the create payload. Why: a single bad reviewer would otherwise fail PR creation, which violates "never failing the ship". An identity lookup with more than one match is skipped with a warning rather than taking the first match.
- **Long PR descriptions.** Chosen: truncate to ADO's 4000-char PR-description cap. Rejected: letting ADO 400. Why: a 400 kills the ship on long dross-generated bodies. This is the one addition beyond the literal spec text; drop it if the judge wants strict scope.
- **Bare-number api_version in TOML.** Chosen: decode a bare TOML number tolerantly (6.0 → "6.0"). Rejected: string-only. Why: hand-set Server configs (c-2) will write `api_version = 6.0`, and a string-only field turns that into a decode error that breaks every dross command in the repo.
- **api_version validation.** Chosen: validate it against a version pattern before the token is read. Rejected: raw interpolation. Why: a value like "7.1&…" would inject into the query string.
- **ADO sign-in responses.** Chosen: send X-TFS-FedAuthRedirect: Suppress, follow no redirects, and treat 203 as an auth failure. Rejected: generic `status >= 300` checks. Why: ADO answers an expired PAT with a 203 HTML sign-in page, which a generic check reads as success.
- **Dedupe filtering.** Chosen: re-check sourceRefName, active status and forkSource client-side. Rejected: trusting searchCriteria alone. Why: a mistyped parameter name is silently ignored by ADO, which then returns every active PR.
- **Unknown or empty PR status.** Chosen: treat it as an error, which triggers the git-ancestry fallback. Rejected: Merged=false. Why: this matches the "lookup failure is an error" posture of the existing backends.
- **Cross-provider defaults.** Chosen: the defaults.Apply provider-mismatch guard (t-11). Rejected: a prompt-only fix. Why: init.md asks only for fields the defaults "didn't seed", so a saved GITHUB_TOKEN would silently ride onto an ADO repo.
- **Wave-2 response types.** Chosen: function-local response structs (the bitbucket.go style). Rejected: package-level types. Why: package-level types would need three same-wave edits to not_paths.txt.
- **Secret-scan needles.** Chosen: no "pat" key-context needle. Rejected: adding one. Why: "pat" matches path and pattern everywhere. The known gap `AZURE_DEVOPS_EXT_PAT=<52-char>` stays, per c-9's "left to key-context".
- **AZDO rule alphabet.** Chosen: [A-Za-z0-9]. This alphabet is unconfirmed against a real token. At verify, the live smoke run should scan the user's real PAT in memory (never echoed) to confirm the rule fires.
- **Telemetry bucket.** Chosen: no "azure-devops backend" telemetry bucket. Rejected: adding one. Why: the bitbucket precedent didn't add one either, and the "unsupported provider" and "[remote]" buckets already classify these errors.
- **Deferred surfaces.** Chosen: they get a fail-closed test only (t-8). Rejected: adding sentinel arms. Why: those arms belong to azure-devops-ship-loop.
- **Known risk not owned here: protected base branches.** Branch-protection probing is GitHub-only. An ADO base under a branch policy with unpushed .dross chores fails closed at the push with git's own TF402455 message, and the live smoke run should exercise it.
- **Known risk not owned here: http-only on-prem TFS.** Servers reachable only over http stay refused by hostallow's https rule. t-9 documents this in options.md rather than relaxing the rule.
