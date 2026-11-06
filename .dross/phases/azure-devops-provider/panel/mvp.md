# Panel draft — mvp lens (smallest task set that satisfies every criterion)

```
Phase azure-devops-provider — 5 tasks across 2 waves

Wave 1
  t-1  Add Azure DevOps REST transport to ship
       files:    internal/ship/azuredevops.go, internal/ship/azuredevops_test.go,
                 internal/ship/open.go, internal/ship/comment.go,
                 internal/ship/redact_test.go, internal/secretscan/sinks.go
       desc:     New azuredevops.go holds every ADO call, all unexported and not yet dispatched:
                 adoRequest (ScanPayload → encode → one auth header → api-version=<APIVersion|7.1>
                 → redact.Scrub on the body), adoAuth (Basic ":"+PAT by default, Bearer when
                 auth_scheme=bearer), token read only through resolveToken, and project/repo split
                 around _git. On top of it: openAzureDevOpsPR (refs/heads/ source/target, isDraft,
                 URL = <remote.url>/pullrequest/<id>), reviewers as optional (GUID passes through,
                 anything else goes through the Identities API, any failure returns result+error),
                 adoOpenPRBySource, azureDevOpsPRStatus, postAzureDevOpsComment (threads).
                 OpenOpts/CommentOpts gain APIVersion. adoRequest is registered as a Screened
                 transport in sinks.go and added to redact_test's scrubbingHelpers.
       covers:   c-2, c-4, c-5, c-6, c-7, c-8
       contract: - auth: with auth_scheme "" the mock sees exactly `Authorization: Basic base64(":"+PAT)`
                   and no PRIVATE-TOKEN / `token` header; with "bearer" it sees `Authorization: Bearer <PAT>`.
                   A copy-pasted gitlab/forgejo header fails this.
                 - host guard: Hosts=Derive("https://dev.azure.com/acme/P/_git/R") with api_base = the
                   httptest URL makes open/comment/status/by-source each return an error wrapping
                   hostallow.ErrRefused, the server counts 0 requests, and the error names neither the token
                   nor the env var. TestNoGetenvOutsideHostguard fails if azuredevops.go calls os.Getenv.
                 - identity (c-2): the POST for URL https://<srv>/tfs/DefaultCollection/Proj/_git/Repo goes to
                   /tfs/DefaultCollection/Proj/_apis/git/repositories/Repo/pullrequests. For a host-root
                   collection (https://<srv>/Coll/Proj/_git/Repo) it goes to /Coll/Proj/.... For .../acme/_git/Repo
                   the project is Repo. For https://acme.visualstudio.com/Proj/_git/Repo the project is Proj.
                   For My%20Proj the request path carries My%20Proj, never %2520 and never a raw space.
                 - api-version: every request carries api-version=7.1 when APIVersion is "", and 6.0 when it is "6.0".
                 - open (c-5): the create body has sourceRefName "refs/heads/phase/x", targetRefName
                   "refs/heads/main", title, description and isDraft=true for a draft. The response
                   pullRequestId 42 maps to Number 42 and URL "<remote.url>/pullrequest/42".
                 - reviewers (c-5): a GUID entry PUTs .../pullrequests/42/reviewers/<guid> with isRequired=false
                   and makes no identities call. An email entry first GETs identities?searchFilter=General&filterValue=<email>.
                   An identities 404, an empty result or a reviewer PUT 500 returns a NON-nil result AND an error,
                   so ship warns instead of failing. adoIdentityBase maps https://dev.azure.com/acme and
                   https://acme.visualstudio.com to https://vssps.dev.azure.com/acme, and a Server api_base to itself.
                 - dedupe (c-6): adoOpenPRBySource sends searchCriteria.sourceRefName=refs/heads/phase/x and
                   searchCriteria.status=active. One hit returns {Number, URL}, an empty `value` returns (nil,nil),
                   and HTTP 500 returns a non-nil error, never (nil,nil).
                 - comment (c-7): POST .../pullRequests/7/threads with comments[0].content == body and NO
                   top-level "status" key (locked comment_thread_status). A body carrying a ghp_ token is refused
                   with 0 requests sent. A 403 whose body echoes the PAT returns an error with the PAT scrubbed.
                 - status (c-8): status completed returns Merged=true. abandoned and active both return Merged=false.
                   targetRefName "refs/heads/release/1.2" returns BaseRef "release/1.2".
                 - registries: drop the sinks.go entry and the transport walker (secretscan_transport_enum_test)
                   reports adoRequest as an undeclared transport. Drop the scrubbingHelpers entry and
                   TestShipHelpersScrubAtSource reports a body read outside the helpers.

  t-2  Canonicalize ADO remotes; doctor equivalence and combinations
       files:    internal/project/azuredevops.go, internal/project/azuredevops_test.go,
                 internal/cmd/doctor.go, internal/cmd/doctor_test.go,
                 internal/diag/combinations.go, internal/diag/diag_test.go
       desc:     Exported project.CanonicalAzureDevOps(raw) returns (url, apiBase, ok) for every
                 Services/legacy/Server form: it drops userinfo, maps ssh v3 to https on the matching
                 web host, drops the Server ssh port and keeps percent-encoding. doctor's sameRemoteURL
                 compares ADO-shaped remotes through it. diag.RemoteCombination gains ADO arms: private-token
                 and basic are ineffective, a stray auth_user is ignored, and Basic no longer demands auth_user.
       covers:   c-1, c-2, c-3, c-4
       contract: - forms (c-1):
                     git@ssh.dev.azure.com:v3/acme/Proj/Repo → https://dev.azure.com/acme/Proj/_git/Repo, api_base https://dev.azure.com/acme
                     https://acme@dev.azure.com/acme/Proj/_git/Repo → the same pair
                     acme@vs-ssh.visualstudio.com:v3/acme/Proj/Repo → https://acme.visualstudio.com/Proj/_git/Repo, api_base https://acme.visualstudio.com (NOT rewritten to dev.azure.com)
                     https://dev.azure.com/acme/My%20Proj/_git/My%20Repo round-trips byte-identical
                     https://github.com/o/r → ok=false
                 - Server (c-2): ssh://ado.corp:22/tfs/DefaultCollection/Proj/_git/Repo →
                   https://ado.corp/tfs/DefaultCollection/Proj/_git/Repo (port dropped).
                 - token never persisted (c-4): https://acme:S3CRET@dev.azure.com/... returns a url and api_base
                   that do not contain "S3CRET".
                 - doctor origin match (c-3): TestSameRemoteURL rows — the ssh v3 origin vs the https remote.url is
                   true, the user@ and .git variants are true, the Server ssh:22 form vs https is true, and
                   .../_git/Other vs .../_git/Repo is false. The existing github rows stay unchanged.
                   TestDoctorCarriesNoProviderLiterals fails if the equivalence is done with a provider switch in doctor.go.
                 - combinations (c-3), as presence/absence assertions:
                     ("azure-devops","private-token","") has a warning naming private-token as having no effect
                     ("azure-devops","basic","") has "no effect" and does NOT have "auth_user is not set"
                     ("azure-devops","","me") has an auth_user-is-ignored warning
                     ("azure-devops","bearer","") has none of those three
                   The existing bitbucket rows still pass.

  t-3  Teach built-in security tables Azure DevOps
       files:    internal/secretscan/rules.go, internal/secretscan/secretscan_test.go,
                 internal/hostallow/hostallow.go, internal/hostallow/hostallow_test.go
       desc:     Add an "azure-devops-pat" rule (76 alnum + "AZDO" + 4 alnum, word-bounded, needle
                 "AZDO") to the fixed rule table. saasDefaults gains dev.azure.com, vssps.dev.azure.com
                 and *.visualstudio.com.
       covers:   c-4, c-9
       contract: - c-9: hitCorpus gains "azure-devops-pat" with an 84-char value built at runtime (so the repo
                   self-scan stays clean). That line yields exactly one hit of that rule.
                 - c-9: the property generator gets an azure-devops-pat arm. Its random 84-char values with AZDO
                   at offset 76 always hit.
                 - c-9: benignCorpus gains "<your-pat>" (no hit) and a bare 52-char [a-z2-7] value (no hit).
                   `token = <that 52-char value>` yields exactly one key-context hit and no azure-devops-pat hit.
                 - c-4: Derive("",nil) accepts https://dev.azure.com/acme, https://vssps.dev.azure.com/acme and
                   https://acme.visualstudio.com. It refuses https://evil-visualstudio.com and
                   https://dev.azure.com.attacker.example with ErrRefused. The existing
                   len(Allowed())==len(saasDefaults) check still holds.

Wave 2 (t-4 depends t-1, t-2; t-5 depends t-1)
  t-4  Recognize and dispatch azure-devops atomically
       files:    internal/configenum/configenum.go, internal/configenum/configenum_test.go,
                 internal/project/remote.go, internal/project/remote_test.go,
                 internal/ship/open.go, internal/ship/comment.go, internal/ship/merged.go,
                 internal/ship/headpr.go, internal/ship/azuredevops_test.go,
                 internal/cmd/prompt_provider_list_test.go,
                 assets/prompts/init.md, assets/prompts/onboard.md
       desc:     The locked guard_atomicity change: ShipProviders += azure-devops. KnownHostProviders
                 maps dev.azure.com and the ssh hosts. DetectRemote routes ADO-shaped remotes through
                 CanonicalAzureDevOps, with an "azure-devops" api-base arm and Public=false. OpenPR,
                 PostComment and GetPRStatus get the arm, plus FindOpenPRByHead so ship dedupes instead of
                 refusing. The init and onboard provider bullets list azure-devops with AZURE_DEVOPS_EXT_PAT
                 as the default auth_env. (r-01: make install before any live check.)
       covers:   c-1, c-5, c-6, c-7, c-8
       contract: - guards: if any of OpenPR/PostComment/GetPRStatus or the DetectRemote api-base switch lacks the
                   arm, TestShipDispatchMatchesShipProviders / TestRemoteAPIBaseMatchesShipProviders fail naming
                   that function. A bare ToLower tag fails TestDispatchUsesConfigenumNormalize.
                 - configenum: the TestSetListRendersPipeJoined "ship" row expects "github | forgejo | gitea | gitlab | bitbucket | azure-devops".
                 - prompts: if init.md or onboard.md omit azure-devops from the provider bullet,
                   TestPromptProviderListsMatchShipProviders fails. A new assertion fails if either prompt lacks
                   AZURE_DEVOPS_EXT_PAT.
                 - c-1 DetectRemote table: the https, user@, ssh v3, legacy https and vs-ssh forms each give
                   Provider "azure-devops", the canonical URL, the org api_base and Public == false. A remote with
                   user:PAT@ persists no PAT, and the %20 names survive.
                 - dispatch: with Provider " Azure-DevOps " (padded and mixed case), OpenPR, PostComment, GetPRStatus
                   and FindOpenPRByHead each hit the mock at the ADO path. None returns "unsupported provider".
                 - c-6: FindOpenPRByHead on azure-devops with HTTP 500 returns an error that is NOT
                   ErrHeadPRLookupUnsupported, so cmd/ship.go stops instead of falling through to OpenPR.

  t-5  Add [remote].api_version and thread it to ship
       files:    internal/project/project.go, internal/cmd/project.go, internal/cmd/ship.go,
                 internal/cmd/ship_test.go, assets/prompts/options.md, internal/cmd/options_docs_test.go
       desc:     Remote gains APIVersion (`api_version`), with readDotted/writeDotted arms. buildOpenOpts
                 and buildCommentOpts copy it onto the opts. options.md documents azure-devops (Basic :PAT
                 default, bearer for Entra, auth_user ignored) and remote.api_version (7.1 default; Server
                 2019=5.0, 2020=6.0, 2022=7.0).
       covers:   c-1, c-2
       contract: - c-2: writeDotted(p,"remote.api_version","6.0") is followed by readDotted == "6.0", and both
                   buildOpenOpts(p,…).APIVersion and buildCommentOpts(p,…).APIVersion are "6.0". This catches a
                   missing setter arm and a dropped copy in one test.
                 - if buildOpenOpts stops reading p.Remote.APIVersion, TestNoSettableKeyIsInert fails naming
                   remote.api_version. That is why the setter and the reader land in this one task.
                 - c-1: TestOptionsCoversTheProviderConditionalRemoteFields gains "remote.api_version" and
                   "azure-devops". It fails if options.md drops either.
```

## Coverage

| Criterion | Tasks | What delivers it |
|---|---|---|
| c-1 autodetect + prompts | t-2, t-4, t-5 | t-2 canonical forms and percent-encoding; t-4 DetectRemote/KnownHostProviders, init+onboard bullets; t-5 options.md |
| c-2 Server by hand | t-1, t-2, t-5 | t-1 _git identity for /tfs/DefaultCollection and host-root collection; t-2 Server ssh canonical; t-5 api_version for older Server |
| c-3 doctor | t-2 | origin equivalence (sameRemoteURL) and combination warnings (diag) |
| c-4 one credential, allowlist | t-1, t-2, t-3 | t-1 single header and refusal before resolveToken; t-2 userinfo stripped so no PAT reaches project.toml; t-3 built-in hosts |
| c-5 open + reviewers | t-1, t-4 | t-1 payload, URL and non-fatal reviewers; t-4 dispatch arm. The URL is printed by the existing `narrate("PR opened: %s (#%d)")` in cmd/ship.go |
| c-6 dedupe | t-1, t-4 | t-1 by-source query, failure is an error; t-4 FindOpenPRByHead arm. The existing cmd/ship.go step (b') records a hit or refuses on error |
| c-7 comment thread | t-1, t-4 | t-1 thread POST, screening, scrubbed error; t-4 PostComment arm |
| c-8 merged/base | t-1, t-4 | t-1 completed-only plus BaseRef trim; t-4 GetPRStatus arm |
| c-9 PAT scanner | t-3 | azure-devops-pat rule plus hit/benign/property corpora |

All 9 of 9 criteria are covered.

## Judgment calls

- **Pre-land the transport, then wire it.** I put all callee bodies in t-1, unexported and tested directly. I rejected landing the transport inside the atomic task, because enum_divergence reads only switch case literals. guard_atomicity binds recognition and the dispatch *arms*, not the callees, so this keeps t-4 to wiring.
- **One transport task, not split per endpoint.** I rejected separate open, comment and status tasks. All six calls share one request helper, one identity parser and the two registries (sinks.go, scrubbingHelpers) that must change in the helper's first commit. This mirrors bitbucket.go being one file.
- **FindOpenPRByHead arm goes in the atomic task.** I rejected a separate dedupe task. Once azure-devops is in ShipProviders, headpr's default arm returns "unsupported provider" rather than the sentinel. cmd/ship.go would then refuse every ADO ship at step (b'), leaving a commit where the provider is recognized but cannot ship.
- **diag warnings go in wave 1 (t-2) as presence/absence asserts.** I rejected adding rows to the exactly-one-warning table in t-4. Before t-4, the "cannot open a PR" warning also fires. Presence asserts keep c-3 whole in one task and keep t-4 at the lock's minimum.
- **api_version setter and reader stay in one task (t-5).** TestNoSettableKeyIsInert forces this, so I rejected putting Remote.APIVersion in the wave-1 project task: a settable key with no reader would turn that commit red.
- **Duplicate the small _git split in project and ship.** I rejected a shared package (speculative) and ship importing project (ship has never done so, and it would push t-1 behind t-2).
- **One segment before _git on \*.visualstudio.com means the project.** On that host the org is in the hostname. Elsewhere, one segment is the base and repo == project (repo_identity). This reads the two locks together; it overrides neither.
- **Security tables are merged.** The secretscan rule and the hostallow hosts are both built-in tables that a cloned repo cannot rewrite, and each is under 15 minutes alone.
- **options.md goes in t-5, not t-4.** The prompt-list guard does not check it, and it documents the new api_version key, so it does not need to ride the atomic change.
- **Dropped: no ADO rows in the hostguard_test/redact_test/ship-secretscan_test tables.** t-1 tests the same functions the arms call, and t-4 proves the arms reach them.
- **Dropped: no "azure-devops backend" telemetry bucket.** No criterion asks for it, and bitbucket shipped without one.
- **Dropped: no live-smoke task.** verify_posture puts the live run at verify, and records it separately.
- **Dropped: no ship.md CI or merge prompt work.** It is deferred to azure-devops-ship-loop.
- **Accepted: t-4 has 12 files.** guard_atomicity plus TestPromptProviderListsMatchShipProviders force configenum(+test), remote.go, the three dispatch files and init/onboard into one commit. Everything not forced (diag, options.md, api_version, transport bodies) was moved out.
