# azure-boards-backend — panel synthesis

## Scores

| Dimension | risk (17 / 7) | mvp (7 / 4) | verification (20 / 6) |
|---|---|---|---|
| Criteria coverage | 4/5. All 10 covered, including c-10's milestone path and parenting for task cards that finalize creates. c-1, c-5 and c-7 name commands but are proved only at the boardsync level. | 3/5. All 10 covered on paper, but c-10 stops at routeBaseChores, and t-1/t-3 would break existing guards: TestBoardFieldsAllAddressable (no `project set` arms), TestEveryPathShapedFieldIsDeclared (no not_paths.txt rows) and TestEveryOutboundSeamIsRegistered (no sinks.go entry). | 5/5. All 10 covered, with cobra-level proofs for c-1/c-5/c-7 and a ship-then-complete twin for c-10. Its only gap is parenting for task cards that finalize creates (risk has it). |
| Test-contract specificity | 5/5. The most adversarial contracts: ADO error codes (TF201036, TF401320), stale /rev, GUID relation URLs, WIQL injection, partial batch failure. Each one names its test and values. | 4/5. Tests are named with concrete values, but most lines bundle 3–4 assertions and there are few edge cases (no rev, batching or injection beyond quote doubling). | 5/5. Per-process state tables, exact pull envelopes and request-log assertions. It has slightly fewer hostile-input cases than risk. |
| Granularity | 3/5. Four tasks touch 5 files each (t-1, t-2, t-10, t-14), and t-14 spans forge, configenum, boardsync and cmd. | 2/5. t-1 is the whole forge client in one task (transport, CRUD, WIQL, Epic, close, path probe, dispatch). t-3 is 5 files across project, boardsync and cmd. | 4/5. Every task has ≤4 files and ≤2 layers, sized like the two prior ADO plans. It missed two guard files (enum_divergence_test.go, friction_window_test.go), which push its t-14 and t-18 to 5 files once added. |
| Wave correctness | 3/5. 7 waves, because it keeps waves file-disjoint by design (forge.go is edited in W1, W4 and W5). t-11 waits on t-7 although its tests stub AutoMergePRFunc, and dispatch (t-14) holds back all boardsync work. | 4/5. A tight 4 waves with real dependencies. t-5 waits on t-2 although its tests stub BranchRules. | 5/5. Every dependency is real, and stub seams let t-10 run beside t-9. Tasks in the same wave touch disjoint files. |

**Skeleton: verification.** It is the only draft where every task fits the binding granularity rule. Its dependencies are real, and it proves the criteria that name commands (c-1, c-5, c-7) by running those commands. Most of the grafted contracts come from risk. mvp supplies the friction-window fixture, the milestone-base routing case and the doctor probe that proves the config is mapped.

## Merged plan

Changes to the skeleton:
- **Doctor task split.** The doctor task is split in two (diag warnings become t-11) to keep it under 5 files once friction_window_test.go is added.
- **Dispatch task.** It gains enum_divergence_test.go (the NewBoard case count goes from 3 to 4). Verification's separate dispatch test file is folded into t-20.
- **Finalize parenting.** The finalize proof task gains risk's one code change in finalize.go.
- **Parenting design.** Parenting follows risk's hybrid (see D2).

Origin tags give the draft task ids. "(risk)" or "(mvp)" at the end of a contract line marks a grafted line.

```
Phase azure-boards-backend — 21 tasks across 6 waves

Wave 1
  t-1  Add ADO fields to the board block            [verification t-1 + risk t-1]
       files:      internal/project/project.go, internal/cmd/project.go, internal/cmd/project_test.go,
                   internal/cmd/testdata/pathfence_scan/not_paths.txt
       covers:     c-1, c-9
       depends_on: —
       desc:       project.Board gains api_version (project.APIVersion, so a bare 6.0 decodes), auth_scheme, area_path
                   and iteration_path, each with omitempty toml and json tags. None of them inherits from [remote]
                   (locked board_config). `dross project get/set/unset` gets arms for all four, and board.auth_scheme
                   joins enumKeys, which checks it against configenum.AuthSchemes. Four not_paths.txt rows are added.
                   The project-set arms and the ledger rows have to land in the same commit as the fields because of
                   the reflection guards below (mvp omitted them).
       contract:
         - if a key is unwired, TestBoardFieldsAllAddressable (existing, reflects over project.Board) fails, and
           TestProjectSetGetADOBoardKeys fails unless set then get round-trips board.api_version=6.0,
           board.auth_scheme=bearer, board.area_path=`Fabrikam\Web` and board.iteration_path=`Fabrikam\Sprint 1`
           verbatim, backslashes intact
         - if auth_scheme is not enum-checked, `dross project set board.auth_scheme token` exits non-zero naming the
           AuthSchemes list and leaves project.toml byte-identical
         - if the float decode is lost, TestProjectGetBoardAPIVersionBareNumber fails: `[board] api_version = 6.0`
           reads back "6.0", never "6"
         - if [board] inherits from [remote], TestBoardDoesNotInheritRemote fails: [remote] api_version "6.0" and
           auth_scheme "bearer" with an empty [board] leave board.api_version and board.auth_scheme "" (risk)
         - if a zero value leaks into the file, a project.toml with no ADO [board] keys saves byte-identical
         - if a field is left unfiled, TestEveryPathShapedFieldIsDeclared fails until the four fields are in
           not_paths.txt, and TestTomlFieldsCarryMatchingJSONTags fails if any one lacks a json tag

  t-2  Build Azure Boards transport and constructor  [verification t-2 + risk t-2 + mvp t-1]
       files:      internal/forge/forge.go, internal/forge/azureboards.go, internal/forge/azureboards_test.go,
                   internal/secretscan/sinks.go
       covers:     c-1
       depends_on: —
       desc:       forge.Config gains APIVersion, AreaPath and IterationPath. NewAzureBoards checks, in order: base_url,
                   project and auth_env (each named), the api_version shape (default 7.1), the base_url shape
                   (exported CheckADOBaseURL) and hostallow. Only then does it call os.Getenv, and it makes no
                   request. doRaw runs secretscan.ScanPayload before encoding and sends exactly one api-version param.
                   It authenticates with Basic ":"+PAT (Bearer under auth_scheme bearer) and sets
                   X-TFS-FedAuthRedirect: Suppress. It follows no redirect and treats 203/3xx as a sign-in error.
                   Every body goes through redact.Scrub, and work-item writes are sent as
                   application/json-patch+json. doRaw is registered as Screened in secretscan.Transports. mvp lacked
                   this registration; the transport walker covers forge, so it would go red.
       contract:
         - if a field check is dropped, TestNewAzureBoardsNamesMissingFields fails: empty base_url, project or
           auth_env each errors naming [board].base_url / [board].project / [board].auth_env while the env is unset,
           with zero requests; api_version "7.1&x=1", "7.1#x" and "../7.1" error naming [board].api_version before
           any Getenv (risk)
         - if the token is read before the host check, TestNewAzureBoardsRefusesHostBeforeToken fails: base_url
           https://attacker.example with auth_env unset gives errors.Is(err, hostallow.ErrRefused), never "is not
           set"; with a sentinel env set, the sentinel never appears in the error (risk)
         - if Services or Server URLs are mishandled, TestADOBoardBaseURLShapes fails: https://dev.azure.com/acme,
           https://acme.visualstudio.com, https://acme.visualstudio.com/DefaultCollection and
           https://tfs.corp.example/tfs/DefaultCollection are accepted and keep their collection path;
           https://dev.azure.com and https://dev.azure.com/acme/Proj are refused naming the https://dev.azure.com/<org> form
         - if URLs double-escape, project "Scratch project" builds /acme/Scratch%20project/_apis/... (never %2520), and
           an endpoint already carrying api-version=5.0 goes out with exactly one api-version, the configured one (risk)
         - if the credential drifts, TestADOBoardRequestShape fails: default, basic and private-token send Basic
           base64(":"+PAT) even with AuthUser set; bearer sends "Bearer <PAT>"; every request carries api-version=7.1
           by default (6.0 when set) and X-TFS-FedAuthRedirect: Suppress (risk)
         - if the credential can leak, a 400 body echoing the PAT and base64(":"+PAT) yields an error containing
           neither; a 302 to a second httptest server records zero hits there; a 203 errors naming $AUTH_ENV
         - if the transport is unregistered or unscreened, TestEveryOutboundSeamIsRegistered and
           TestScreenPrecedesRequestInEveryScreenedTransport (internal/cmd) report forge.AzureBoardsClient.doRaw

  t-3  Document ADO board keys in options          [verification t-3 + risk t-1]
       files:      assets/prompts/options.md, internal/cmd/options_docs_test.go
       covers:     c-1, c-9
       depends_on: —
       desc:       Section 12 adds azure-devops to board.provider. It adds bullets for board.api_version (7.1 default;
                   Server 2019 → 5.0, 2020 → 6.0, 2022 → 7.0), board.auth_scheme (Basic with an empty user and the PAT
                   by default, bearer for Entra), and board.area_path / board.iteration_path (backslash form; unset
                   means the project defaults). It also notes that [board] never inherits from [remote] and that
                   milestone_mode is ignored, because milestones are Epics. Not live until `make install` (r-01).
                   mvp left the docs out because no criterion asks for them.
       contract:
         - if a key goes undocumented, TestOptionsCoversADOBoardFields fails unless §12 names azure-devops under
           board.provider, board.api_version with `7.1`, board.auth_scheme with `bearer`, board.area_path and
           board.iteration_path
         - if the no-inheritance rule is lost, the same test fails unless §12 says [board] does not inherit [remote]

  t-4  Extract the live-record honesty check       [verification t-4] (disputed: D5)
       files:      internal/liverecord/liverecord.go, internal/liverecord/liverecord_test.go,
                   internal/ship/ado_live_record_test.go
       covers:     c-8
       depends_on: —
       desc:       Moves the honesty rules into liverecord.Validate(rec, keys, proof): the postures (mock-only |
                   partial | live), the loopback screen, exactly-once step keys, the RFC 3339 run_at check, and
                   "evidence names an ADO id or a URL on api_base's host". The package uses plain structs with no
                   toml/json tags. Ship's test decodes into its own test-local struct, converts it, and keeps its own
                   per-step proofs and statusless-thread rule.
       contract:
         - if the extraction changes the ship verdict, TestADOLiveRecordHonest and TestADOLiveRecordStepEvidence
           still pass unchanged on internal/ship/testdata/ado_live_smoke.toml (posture live), and their fixtures still
           give reject / accept-as-mock-only / accept-as-live
         - if a posture rule weakens, TestValidatePostures fails unless each is rejected: live with one not-run step;
           partial with every step pass; mock-only carrying a pass; a step key twice or an unknown key; an api_base or
           evidence naming 127.0.0.1/localhost/[::1]; a pass naming neither an ADO id nor a URL on api_base's host;
           run_at "yesterday"
         - if a pathfence ledger row becomes necessary, TestEveryPathShapedFieldIsDeclared stays green with no
           not_paths.txt edit, because liverecord types carry no serialization tags

  t-5  Read ADO branch policies as push protection  [verification t-5 + risk t-4 + mvp t-2]
       files:      internal/ship/azuredevops_policy.go, internal/ship/azuredevops_policy_test.go,
                   internal/ship/hostguard_test.go
       covers:     c-10
       depends_on: —
       desc:       ADOBranchProtection(opts, branch) only issues GETs, through ship's existing adoRequest. It reads the
                   repo (id, defaultBranch) and lists _apis/policy/configurations. A branch is protected iff a
                   non-deleted, enabled, blocking config has a scope with repositoryId null or equal to this repo, and
                   that scope's refName matches refs/heads/<branch> exactly or as a prefix (case-insensitive), or its
                   matchKind DefaultBranch names this branch. A scope with no refName (a repo-wide file-size policy,
                   say) does not count. Any read failure, or a truncated list, gives Known=false with a reason.
                   adoMergeCommitAllowed(target) reads blocking "Require a merge strategy" policies (fa4e907d-…) for
                   t-9. The overridable seam is ship.ADOBranchProtectionFunc. Locked ado_branch_protection: dross only
                   reads policies, never writes them.
       contract:
         - if scoping is wrong, TestADOBranchProtectionScopes fails: protected for {enabled, blocking, Exact
           refs/heads/main, this repo} on main and for {Prefix refs/heads/milestone/, repositoryId null} on
           milestone/v1.8; refs/heads/Main protects main; matchKind DefaultBranch protects main but not milestone/v1.8;
           unprotected when the only config has isBlocking false, isEnabled false, isDeleted true, another repositoryId,
           a repo-wide scope with no refName, or Exact refs/heads/main checked against milestone/v1.8 (risk, mvp)
         - if a failed read reads as unprotected, a 401, a 500, a 203 sign-in page, non-JSON, a body without value and
           a full $top page each give Known=false with a reason naming the policy read, never Known=true/unprotected (risk)
         - if merge-commit policy is misread, a blocking merge-strategy policy with allowNoFastForward false on main
           gives allowed=false; allowNoFastForward true, or no such policy, gives true; an unreadable list gives
           known=false (risk)
         - if dross writes a policy, TestADOBranchProtectionReadOnly fails when any request recorded across these tests
           is not a GET
         - if the host check is skipped, the new azure-devops branch-policy row of TestShipRefusesOffAllowlistAPIBase
           sees fake-host hits or a reason without the host refusal

Wave 2
  t-6  Read ADO process metadata and work items    [verification t-6 + risk t-6/t-8]
       files:      internal/forge/azureboards_meta.go, internal/forge/azureboards_meta_test.go,
                   internal/redact/redact_test.go
       covers:     c-2, c-3, c-9
       depends_on: t-2
       desc:       Lazy reads, cached per client. The requirement type comes from
                   <project>/_apis/work/backlogconfiguration (requirementBacklog default type; locked hierarchy). Also
                   read: the type names, each type's states with their categories in API order, and
                   PathExists(area | iteration) via classificationnodes, where 404 means missing. GetIssue does GET
                   workitems/<id>?$expand=relations with no fields param. It maps title, description, System.Tags
                   (dross-vocabulary tags lowercased) and System.State. WorkflowState is the state name. Resolved and
                   State closed are set iff the category is Completed, so a Removed item reads open and unresolved.
       contract:
         - if the requirement type is guessed, TestADORequirementTypeFromBacklogConfig fails: Agile / Scrum / Basic
           fixtures yield User Story / Product Backlog Item / Issue and a custom "Customer Request" yields that name; a
           500 or a body without requirementBacklog errors naming the backlog configuration (never a "User Story"
           fallback); a second call makes no request (risk)
         - if categories are misread, TestADOStatesByCategory fails: GET .../workitemtypes/Product%20Backlog%20Item/states
           (escaped once) yields New/Approved Proposed, Committed InProgress, Done Completed, Removed Removed, in API
           order; a second call makes zero requests
         - if close truth comes from the name, TestADOGetIssueResolved fails: Done (Completed) reads Resolved=true,
           closed, WorkflowState Done; Resolved (category Resolved) and Removed read Resolved=false, open
         - if tag casing breaks identity, System.Tags "Dross; DROSS/phase:p; Bug" reads back as Labels
           [dross, dross/phase:p, Bug], so HasMarker is true
         - if a bad key reaches the wire, GetIssue("PROJ-7") errors with zero requests; a 404 errors (never nil, nil);
           the request carries $expand=relations and no fields param (risk)
         - if a missing path reads as present, TestADOCheckPaths fails: area `Fabrikam\Web` and `Web` both GET
           .../classificationnodes/Areas/Web; `Fabrikam` alone GETs .../Areas; iteration `Fabrikam\Sprint 1` GETs
           .../Iterations/Sprint%201; 404 → missing; 500 or 203 → error, never "missing" (risk)
         - if the scrub is removed, the AzureBoardsClient row of TestEveryForgeClientRedacts fails: a canary token
           echoed by the server never appears in GetIssue's error

  t-7  Relate and parent ADO work items            [verification t-7 + risk t-12]
       files:      internal/forge/forge.go, internal/forge/azureboards_links.go, internal/forge/azureboards_links_test.go
       covers:     c-2, c-6
       depends_on: t-2
       desc:       Adds an optional forge.ParentLinker {LinkParent(parent, child string) error} beside IssueLinker
                   (disputed: D2). LinkIssues adds System.LinkTypes.Related. LinkParent adds Hierarchy-Reverse on the
                   child, replacing a different existing parent in one PATCH: test /rev, remove the old relation, add
                   the new one. Both read relations first and send nothing when the link already exists. Relation
                   targets are compared by trailing id. Compile-time assertions pin IssueLinker and ParentLinker on
                   AzureBoardsClient.
       contract:
         - if relate is not idempotent, TestADOLinkIssuesRelated fails: item 10 with no relations gets one PATCH adding
           rel System.LinkTypes.Related to .../workItems/20, never Hierarchy; a Related already present from either
           side makes zero PATCH (risk)
         - if parenting uses the wrong link, TestADOLinkParent fails: LinkParent("5","9") PATCHes 9 with
           Hierarchy-Reverse to workItems/5; already under 5 → 1 GET, 0 PATCH, including when the relation url carries
           a project-GUID segment; under 4 → one PATCH whose ops are test /rev, remove the old index, add the new one
           (never an add alone) (risk)
         - if a concurrent edit is hidden, a stale-rev rejection errors naming the child and sends no second PATCH (risk)
         - if bad ids reach ADO, LinkParent(child, child) and a non-numeric parent each error with zero requests (risk)
         - if GitHub gains a lossy stub, TestGitHubIsNotAParentLinker fails when *GitHubClient satisfies ParentLinker

  t-8  Add Boards live-smoke record and validator  [verification t-8 + risk t-3 + mvp t-7]
       files:      internal/forge/testdata/ado_boards_live_smoke.toml, internal/forge/ado_boards_live_record_test.go
       covers:     c-8
       depends_on: t-4
       desc:       The template's comments are the runbook. Prerequisites: a scratch ADO project (ado-scratch) with
                   dross init; [board] provider azure-devops with base_url, project and auth_env; the process name; a
                   Bug filed by hand before the pull step; `make install` first. The steps (create, transition, close,
                   parent-link, pull) all start not-run, and posture is mock-only. Validation goes through
                   liverecord.Validate plus per-step proofs.
       contract:
         - if mock can be filed as live, TestADOBoardsLiveRecordHonest fails when posture = live while any step is
           not-run/fail, or run_at, api_version, api_base or process is missing; "partial" with every step passing is
           rejected; the committed template validates as mock-only
         - if a hollow leg passes, TestADOBoardsLiveRecordStepEvidence rejects: create without three distinct ids typed
           Epic / requirement / Task with _workitems/edit/<id> URLs on api_base's host; transition without a read-back
           state that differs from the created one and is InProgress-category; close without a Completed-category
           read-back (Resolved and Removed rejected); parent-link unless the ids chain task → phase → Epic via
           Hierarchy-Reverse; pull unless the envelope has "error":null, lists the hand-filed Bug and none of the dross
           ids (risk, mvp)
         - if httptest evidence slips in, a step naming 127.0.0.1, localhost or [::1] is rejected
         - if the validator is vacuous, in-test fixtures live-with-gap / mock-only / complete-live give reject /
           accept-as-mock / accept-as-live (risk, mvp)
         - if the runbook loses its prerequisites, the test fails unless the comments name dross init on a scratch ADO
           project, provider = "azure-devops", the process, the hand-filed Bug and make install

  t-9  Arm ADO chore auto-complete with merge commit  [verification t-9 + risk t-7 + mvp t-2]
       files:      internal/ship/automerge.go, internal/ship/azuredevops_automerge.go,
                   internal/ship/azuredevops_automerge_test.go, internal/ship/hostguard_test.go
       covers:     c-10
       depends_on: t-5
       desc:       AutoMergePR gains an azure-devops arm that accepts method "merge" only. It checks the target's
                   merge-strategy policy through t-5; a forbidding or unreadable policy gives ErrAutoMergeUnavailable
                   and nothing is written. Otherwise it PATCHes pullrequests/<n> with autoCompleteSetBy set to the
                   connectionData identity and completionOptions {mergeStrategy noFastForward, bypassPolicy false},
                   then reads the outcome back. Locked chore_pr_completion.
       contract:
         - if the payload drifts, TestADOArmChoreAutoComplete fails: exactly one PATCH to .../pullrequests/41 whose
           autoCompleteSetBy.id equals connectionData's authenticatedUser id and whose completionOptions.mergeStrategy is
           "noFastForward"; bypassPolicy absent or false, no bypassReason; no recorded body anywhere carries
           bypassPolicy true (risk)
         - if a forbidden merge commit is armed, a blocking "Require a merge strategy" config on the target with
           allowNoFastForward false, or an unreadable policy list, gives errors.Is(err, ErrAutoMergeUnavailable) and
           zero PATCH (risk)
         - if a refusal is mishandled, a 400 or 403 on the PATCH gives ErrAutoMergeUnavailable; a 500 gives a plain
           error that is not ErrAutoMergeUnavailable
         - if the outcome is guessed, read-back completed → Merged; active with autoCompleteSetBy → AutoEnabled; active
           without it, or a 200 PATCH answer lacking autoCompleteSetBy, is never AutoEnabled (risk)
         - if the phase-PR rule regresses, TestADOCompletePayload still bans autoCompleteSetBy in the phase completion body
         - if another method is armed, method "squash" on azure-devops errors with zero requests; provider github still
           takes the gh path (existing automerge tests unchanged) (risk)
         - if the host check is skipped, the azure-devops auto-complete row of TestShipRefusesOffAllowlistAPIBase sees
           zero fake-host hits and ErrRefused

  t-10 Route base chores through ADO chore PRs      [verification t-10 + mvp t-5]
       files:      internal/cmd/baseprotect.go, internal/cmd/basebranch.go, internal/cmd/chorepr.go,
                   internal/cmd/basebranch_ado_test.go
       covers:     c-10
       depends_on: t-5
       desc:       baseprotect.go is a single protection dispatch (seam disputed: D3): github goes to BranchRulesFunc
                   and protect.RequiresPR, azure-devops to ship.ADOBranchProtectionFunc, and every other provider is
                   not checked. routeBaseChores uses it. That one change covers phase complete (phase.go),
                   finalizeBoard (phase_finalize.go), and the safety nets in ship and ship-recover. On ADO, the
                   "protection unknown" error names [remote].auth_env and the PAT, not `gh auth login`. chorePR learns
                   its provider: its narration says auto-complete is armed or that the PR must be completed by hand,
                   and pullURL builds <url>/pullrequest/<n>.
       contract:
         - if an ADO base is pushed, TestProtectedADOBaseRoutesThroughChorePR fails: on an azure-devops remote with a
           hook rejecting main and the seam stubbed protected, routeBaseChores opens one chore PR with
           dross-chores/main == local main, offers the hook no refs/heads/main, and arms only ["merge"]
         - if the milestone base is missed, the same test on base milestone/v1.8 opens dross-chores/milestone-v1.8 →
           milestone/v1.8 and the hook sees no refs/heads/milestone/ update (mvp)
         - if an unprotected ADO base is routed, a stub answering unprotected gives Pushed=true, origin main == local
           main, no chore branch and zero PR opens
         - if an unknown read pushes, a Known=false stub gives an error naming the reason and [remote].auth_env (never
           "gh auth login") and an empty hook log
         - if code rides a chore PR, a non-.dross commit ahead on a protected ADO main is refused naming the file,
           with zero PRs opened
         - if narration stays GitHub-shaped, TestChorePRNarrationADO fails: ErrAutoMergeUnavailable on azure-devops
           narrates "complete it by hand" with .../_git/r/pullrequest/41 (never /pull/41); an armed PR narrates
           auto-complete (mvp)
         - if GitHub regresses, every basebranch_chores_test.go and chorepr_test.go case passes unchanged

  t-11 Warn on ineffective ADO board keys           [risk t-10 + verification t-18, diag half split out]
       files:      internal/diag/combinations.go, internal/diag/diag_test.go
       covers:     c-1
       depends_on: t-1
       desc:       Board-combination warnings for azure-devops, mirroring the existing azureDevOpsCombination for
                   [remote]. They say that [board].auth_scheme private-token or basic has no effect, that auth_user
                   is ignored, and that milestone_mode is ignored (milestones are Epics). A further warning says
                   [board].auth_scheme set on a non-ADO board has no effect (pairs with the D6 default). These are
                   warnings only; t-19 wires them into doctor. The split keeps the doctor task under 5 files.
       contract:
         - if an ineffective key reads as configured, the azure-devops cases fail unless auth_scheme private-token and
           basic, a set auth_user, and milestone_mode agile each yield one warning naming the key; bearer and an empty
           auth_scheme yield none
         - if a warning leaks across providers, [board].auth_scheme set on a jira board yields a "no effect" warning,
           and the existing jira / youtrack / github BoardCombination cases are unchanged

Wave 3
  t-12 Create, update and list ADO work items      [verification t-11 + risk t-8/t-9/t-12 + mvp t-1]
       files:      internal/forge/azureboards_items.go, internal/forge/azureboards_items_test.go,
                   internal/forge/secretscan_test.go
       covers:     c-2, c-5, c-9
       depends_on: t-6
       desc:       - CreateIssue posts $Task when the labels carry dross/task:, else the requirement type. One
                     createWorkItem helper serves every type, the Epic included. It sends System.Title (at most 255
                     runes), System.Description (HTML-escaped, newlines as <br>, per risk; verification sent it as-is)
                     and System.Tags joined with "; ". AreaPath and IterationPath go only when configured. When
                     in.Milestone > 0 it adds one Hierarchy-Reverse op to that Epic (D2 default).
                   - UpdateIssue PATCHes only the fields given. Labels replace the whole tag set. It never sends
                     area/iteration, ignores Milestone (re-parenting belongs to LinkParent) and refuses a State patch.
                   - ListIssues runs a WIQL query scoped by [System.TeamProject] = @project and POSTed to
                     /<org>/<project>/_apis/wit/wiql. Each non-dross label is OR'd as a tag CONTAINS clause, plus
                     [System.WorkItemType] = when the project has a type of that name, case-insensitive (locked
                     pull_match). dross labels match tags only, and ' is doubled. A post-filter checks the exact tag
                     or type. State open drops the Completed and Removed categories. Ids are read through
                     workitemsbatch, at most 200 per call.
                   - EnsureMilestoneEpic(version, title, desc) finds the Epic tagged dross/milestone:<version>. On
                     duplicates it takes the lowest id and warns, and it PATCHes a stale title. With no match it
                     creates an Epic tagged "dross; dross/milestone:<version>". EnsureMilestone delegates to it.
       contract:
         - if type selection breaks, TestADOCreateIssueType fails: [dross, dross/phase:p, dross/task:p/t-1] POSTs
           .../workitems/$Task; [dross, dross/phase:p] on Scrum POSTs .../workitems/$Product%20Backlog%20Item (escaped
           once) and on Basic $Issue, with Content-Type application/json-patch+json and System.Tags "dross; dross/phase:p"
         - if area/iteration leak or drop, TestADOCreateIssueClassification fails: with AreaPath `Fabrikam\Web` and
           IterationPath `Fabrikam\Sprint 1` the requirement, Task and Epic creates each carry both ops with those
           values; unset, none of the three bodies has either op (no empty string); UpdateIssue never carries either
         - if the phase loses its Epic, Milestone 42 adds exactly one relations op rel System.LinkTypes.Hierarchy-Reverse
           with a url ending /_apis/wit/workItems/42; Milestone 0 adds none; UpdateIssue with a Milestone sends no
           relation op (risk, mvp)
         - if bad input reaches ADO, a 300-rune title (multi-byte at the cut) is sent as exactly 255 runes;
           "a < b\n- x & y" is sent as "a &lt; b<br>- x &amp; y"; a label containing ';' errors naming it with zero
           requests (risk)
         - if tags accumulate, UpdateIssue Labels [a] on an item tagged "a; b" sends System.Tags "a"; nil Labels sends
           no tags op; a State patch errors with zero requests (risk)
         - if the Epic duplicates, TestADOEnsureMilestoneEpic fails: WIQL answering id 3 returns "3" with zero POST; two
           matches pick 99 over 100 and warn; an empty WIQL gives one POST .../workitems/$Epic tagged
           "dross; dross/milestone:v1.8" and titled "v1.8 — <title>"; a stale title is PATCHed once, an unchanged one
           never (risk)
         - if pull matching is tag-only or type-only, TestADOListIssuesLabelMatch fails: labels [bug, enhancement] on
           Agile send [System.Tags] CONTAINS 'bug' OR [System.WorkItemType] = 'Bug' OR [System.Tags] CONTAINS
           'enhancement', with no type clause for enhancement; Basic gets no type clause; labels dross and dross/phase:x
           never produce one; `o'brien` is sent as 'o''brien' and "x' OR [System.Id] > '0" stays one literal (risk)
         - if a pull crosses projects, every WIQL carries [System.TeamProject] = @project and is POSTed to
           /<org>/<project>/_apis/wit/wiql, never the org root (risk)
         - if CONTAINS is trusted for identity, an id the WIQL returns for dross/task:p/t-10 is dropped from a
           dross/task:p/t-1 lookup (risk)
         - if open includes done work, State open omits Done and Removed and keeps Active and Resolved-category items;
           State all keeps all of them
         - if failure reads as zero, a 500 or 203 on WIQL or on the types read returns an error, never an empty slice
           with a nil error; 450 ids batch as 200/200/50 and a 500 on the second returns (nil, err); a null batch
           entry (deleted mid-query) is skipped (risk)
         - if the payload screen is bypassed, the azure-devops row added to allBackends() fails
           TestCreateIssueRefusedBeforeRequest / TestUpdateIssueBodyPatchRefused when a token in the body reaches the server

  t-13 Resolve ADO states by category              [verification t-12 + risk t-6/t-13 + mvp t-4]
       files:      internal/forge/azureboards_state.go, internal/forge/azureboards_state_test.go,
                   internal/forge/state_map_test.go, internal/boardsync/board_lifecycle_divergence_test.go
       covers:     c-3, c-4, c-6
       depends_on: t-6
       desc:       - defaultADOStateCategory is a plain map[string]string literal keyed by LifecycleStatuses, so the
                     AST guards can read it. planned maps to Proposed; in-progress and task-in-progress to InProgress;
                     uat and task-in-review to Resolved (InProgress where the type has no Resolved state); shipped
                     (disputed: D1), complete and task-complete to Completed.
                   - The state written is the first state of that category in the type's order, never Removed.
                     [board].state_map overrides by state name (locked state_resolution).
                   - SetState sends zero PATCH when the item is already in the target state. It warns, sends no
                     PATCH and returns nil when no state is reachable, when the override names a state the type
                     lacks, or on a 400 rule rejection. A 500 returns an error.
                   - CloseIssueAs refuses before writing when the resolved target is not Completed-category (risk;
                     mvp wrote first and failed on the read-back). Otherwise it PATCHes, re-reads, and errors naming
                     the id unless the read-back is Completed. CloseIssue is CloseIssueAs("complete").
                   - SetStateRaw writes the literal state and verifies the read-back (EqualFold); a 400 on the write
                     is an error. StateWriter is asserted.
       contract:
         - if defaults become per-process tables, TestADODefaultStateResolution fails on any row: Scrum PBI
           in-progress→Committed, uat→Committed, planned→New (not Approved), complete→Done; Scrum Task
           task-in-progress→In Progress, task-in-review→In Progress, task-complete→Done; Agile User Story
           in-progress→Active, uat→Resolved, complete→Closed; Agile Task task-in-review→Active; Agile Epic
           complete→Closed; Basic Issue in-progress→Doing, uat→Doing, complete→Done
         - if a default lands in Removed, a table over Agile/Scrum/Basic × every LifecycleStatuses member never
           resolves to a Removed-category state (risk)
         - if the override is ignored or trusted blindly, state_map {in-progress: Approved} PATCHes System.State
           Approved on a PBI; {in-progress: Doing} on Basic beats the default; an override naming a state the Task type
           lacks warns on stderr naming the status, the item and the state, sends zero PATCH and returns nil (risk)
         - if an unreachable state fails the sync, planned on a type with no Proposed state warns and returns nil with
           zero PATCH; a 400 TF401320 rule rejection warns and returns nil; a 500 returns an error; an item already in
           the target makes zero PATCH (risk)
         - if a close is trusted unread, TestADOCloseIssueAsVerifies fails: read-back Done → nil; a 200 PATCH whose
           read-back is Active errors naming work item 42 and the written state; a read-back GET 500 errors; override
           {complete: Resolved} on an Agile User Story, or a blank complete override, errors naming the id with zero
           PATCH (risk)
         - if undo goes through the map, TestADOSetStateRawLiteral fails: SetStateRaw("42","Resolved") PATCHes Resolved
           verbatim and a read-back of Closed errors naming 42; a 400 on the write errors (unlike SetState); the
           azure-devops rows of TestSetStateRawBypassesTheStateMap and TestSetStateRawVerifiesReadBack pass (risk)
         - if a key goes missing or the lifecycle collapses, the azure-devops rows of
           TestStateMapsKeyExactlyTheEmittedStatuses, TestStateMapsKeepTheLifecycleStatesDistinct (uat ≠ shipped),
           TestEveryReapTerminalIsMapped and TestLaneTerminalStatesAreDistinctWithinTheirLane fail on
           defaultADOStateCategory; the last passes only with one azure-devops Phases complete/shipped exemption that
           carries its why

  t-14 Route milestone chores; prove ADO ship-to-complete  [verification t-13 + risk t-11] (disputed: D3)
       files:      internal/cmd/milestone.go, internal/cmd/milestone_protected_ado_test.go,
                   internal/cmd/protected_base_ado_test.go
       covers:     c-10
       depends_on: t-10
       desc:       routeProtectedMilestone and waitForMilestoneChores ask t-10's dispatch instead of GitHub only, and
                   the waiting error's PR URL follows the provider's shape. Also adds ADO twins of the protected
                   ship → complete flow.
       contract:
         - if milestone chores push, TestADOMilestoneChoresRouteThroughChorePR fails: a protected milestone/v1.8 gets
           dross-chores/milestone-v1.8, and the hook is never offered refs/heads/milestone/v1.8
         - if the integration PR outruns its chores, TestADOMilestoneCompleteWaitsForOpenChorePR fails: an open ADO
           chore PR into milestone/v1.8 makes milestone complete error naming .../pullrequest/<n>
         - if ship's safety net or phase complete pushes, TestProtectedADOBaseShipThenComplete fails: on an azure-devops
           remote with protected main, `dross ship` then `dross phase complete x` reach origin only via
           dross-chores/main, the pre-receive log holds no refs/heads/main, and every armed method is "merge"
         - if unprotected ADO bases lose the push, TestUnprotectedADOBaseStillPushesDirectly fails unless origin main ==
           local main after ship and no dross-chores/main exists
         - if GitHub regresses, milestone_protected_test.go passes unchanged (its BranchRulesFunc stubs still decide) (risk)

Wave 4
  t-15 Dispatch azure-devops boards through NewBoard  [verification t-14 + risk t-14]
       files:      internal/forge/forge.go, internal/configenum/configenum.go, internal/configenum/configenum_test.go,
                   internal/cmd/enum_divergence_test.go
       covers:     c-1
       depends_on: t-12, t-13
       desc:       BoardProviders gains azure-devops and NewBoard routes it to NewAzureBoards, in the same commit
                   (mvp: otherwise TestBoardDispatchMatchesBoardProviders is red between commits). Adds
                   var _ BoardClient = (*AzureBoardsClient)(nil). MilestoneModesFor("azure-devops") stays nil.
                   enum_divergence_test.go's NewBoard case-count fixture goes from 3 to 4 (risk; verification missed
                   this file). Verification's separate azureboards_dispatch_test.go is folded out to stay under 5
                   files; t-20's TestEveryBoardCommandUsesAzureBoards proves the built client end to end, and NewBoard
                   already normalizes the provider for every backend. The cmd change is a test fixture only; the
                   production layers stay at two (configenum, forge).
       contract:
         - if dispatch and the accept-set diverge, TestBoardDispatchMatchesBoardProviders fails when azure-devops is in
           only one of NewBoard and BoardProviders, and the NewBoard fixture must count 4 cases (risk)
         - if the set text drifts, configenum_test's board row must read
           "forgejo | gitea | gitlab | youtrack | jira | github | azure-devops"
         - if milestone_mode starts to apply, MilestoneModesFor("azure-devops") must stay nil
         - if a command path rejects it, TestIssueEnableAcceptsEveryBoardProvider/azure-devops and
           TestDoctorAcceptsEveryDispatchableBoardProvider/azure-devops fail
         - if the client drifts off the board surface, var _ BoardClient = (*AzureBoardsClient)(nil) stops compiling

  t-16 Drive ADO states and verified closes        [verification t-15 + risk t-14/t-15]
       files:      internal/boardsync/ctx.go, internal/boardsync/phase.go, internal/boardsync/task.go,
                   internal/boardsync/azureboards_state_sync_test.go
       covers:     c-1, c-3, c-4, c-9
       depends_on: t-1, t-12, t-13
       desc:       boardsync.Config carries api_version, area_path and iteration_path into forge.Config, and
                   auth_scheme for azure-devops only (D6 default). SyncPhase and setBoardState gain an
                   AzureBoardsClient arm (SetState with [board].state_map). CloseIssue gains one too (CloseIssueAs,
                   with read-back). The new test file adds an adoRepo helper (NewAzureBoards via Config) carrying
                   Agile/Scrum/Basic metadata fixtures.
       contract:
         - if Config drops a field, TestConfigCarriesADOFields fails unless Board{api_version 6.0, auth_scheme bearer,
           area_path, iteration_path} reaches the tracker as api-version=6.0, a Bearer header and an AreaPath op on create
         - if [board] inherits [remote], [remote].api_version 5.0 with [board].api_version unset still sends 7.1 (risk)
         - if auth_scheme leaks to other boards, a gitlab board's forge.Config.AuthScheme stays "" with
           [board].auth_scheme set (risk; D6)
         - if a process resolves wrong in sync, TestADOSyncStateByProcess fails: SyncPhase --status in-progress PATCHes
           System.State Active / Committed / Doing (Agile/Scrum/Basic); SyncTasks --status task-in-progress PATCHes
           Active / In Progress / Doing on each task item
         - if state_map is not passed, {in-progress: Approved} makes the Scrum phase PATCH carry Approved
         - if an unreachable state fails the sync, a state_map value the type lacks warns once per item naming its id,
           sends no state PATCH, and SyncPhase / SyncTasks return nil with board.json saved (risk)
         - if "(closed)" outruns the tracker, TestADOPhaseCloseReadBack fails: --status complete --close with read-back
           Active errors naming the work item and ctx.Out has no "(closed)"; read-back Done prints it
         - if one refused task close strands the rest, SyncTasks --status task-complete --close with task 11 reading back
           Active and task 12 Done errors naming t-1 only, and task 12 still got its close PATCH
         - if other boards regress, the existing YouTrack and Jira boardsync tests pass unchanged (risk)

Wave 5
  t-17 Join ADO items by Epic and parents          [verification t-16 + risk t-12/t-15/t-16]
       files:      internal/boardsync/milestone.go, internal/boardsync/phase.go, internal/boardsync/task.go,
                   internal/boardsync/azureboards_hierarchy_test.go
       covers:     c-2, c-4
       depends_on: t-7, t-16
       desc:       EnsureMilestoneLink gains an ADO arm (EnsureMilestoneEpic(version, title, desc)).
                   CheckMilestoneClosable accepts ADO in any mode, because an Epic is an issue. A new phase item is
                   parented on create through IssueInput.Milestone (t-12); an updated one is re-parented under its
                   Epic via ParentLinker (D2 default). SyncTasks prefers ParentLinker over IssueLinker for
                   task → phase and warns once per run if a link fails.
       contract:
         - if the hierarchy is wrong, TestADOSyncBuildsHierarchy fails unless milestone sync POSTs $Epic; phase sync
           POSTs $User%20Story / $Product%20Backlog%20Item / $Issue (Agile/Scrum/Basic) carrying one Hierarchy-Reverse
           to the Epic; task sync POSTs $Task per task, each then PATCHed Hierarchy-Reverse to the phase item; and no
           task is ever sent a System.LinkTypes.Related relation
         - if a re-run duplicates, TestADOResyncUpdatesInPlace fails: with board.json emptied and WIQL returning the
           tagged items, the re-run sends PATCH /workitems/<id> only — zero POST to /_apis/wit/workitems/$* and zero
           relation PATCH for links already present
         - if a moved phase keeps its old parent, a phase whose milestone changes ends up under the new Epic only (risk)
         - if a link failure strands the sync, a LinkParent fault on one Task warns once, the other Tasks are parented,
           and the next run heals it (risk)
         - if the milestone close is refused or unverified, TestADOMilestoneClose fails: CheckMilestoneClosable(ADO) is
           nil with milestone_mode unset; closing the Epic reads back Closed → nil, Active or Resolved → error naming
           it; forgejo is still refused; with an Epic tagged dross/milestone:v1.8 already on the board and board.json
           empty, milestone sync reuses it with zero creates (risk)
         - if a linker-only backend loses links, the linkBoard double still receives LinkIssues(parent, task) for every task

  t-18 Parent finalize cards; prove backlog, undo and finalize  [verification t-17 + risk t-16 + mvp t-6]
       files:      internal/boardsync/finalize.go, internal/boardsync/azureboards_backlog_test.go,
                   internal/boardsync/azureboards_finalize_test.go
       covers:     c-2, c-4, c-6, c-7
       depends_on: t-7, t-16
       desc:       Verification's proof task plus risk's one code change: finalizePlanTask parents any task card it
                   creates under the phase card via ParentLinker. Without it, such a card is orphaned outside the
                   hierarchy, which no other draft caught. The proofs: routed backlog items relate to their target
                   phase item; reap apply then undo restores the exact prior state; FinalizePhase closes the tasks,
                   then the phase; and a re-run only reads.
       contract:
         - if backlog links are wrong or repeat, TestADOBacklogRelatesRoutedItem fails: a deferred item routed to phase p
           (item 20) gets one PATCH adding System.LinkTypes.Related to workItems/20; a second SyncBacklog sends zero
           relation PATCH
         - if undo is not exact, TestADOReapUndoRestoresPriorState fails: a card swept from Committed, and a User Story
           swept from Resolved, are restored by PATCH System.State to exactly that state plus their prior tags; when the
           board refuses one restore, Undo errors naming that id and the restored count covers only the others (risk)
         - if finalize order or terminal states break, TestADOFinalizeTasksThenPhase fails: both task items PATCH to Done
           before the phase item's PATCH to Done, and each reads back Completed
         - if finalize-created cards are orphaned, a task card missing from the board is created with exactly one
           Hierarchy-Reverse to the phase card (risk)
         - if a re-run writes, TestADOFinalizeRerunOnlyReads fails: the second FinalizePhase sends zero PATCH and zero
           POST to /_apis/wit/workitems/$* (WIQL POSTs are reads) and narrates "already complete" (risk)
         - if a refused task close is hidden, a task reading back In Progress after its close makes FinalizePhase error
           naming that task while the other cards and the phase item still close (risk)

  t-19 Validate ADO boards in doctor               [verification t-18 + risk t-10 + mvp t-3]
       files:      internal/cmd/doctor.go, internal/cmd/doctor_board_ado.go, internal/cmd/doctor_board_ado_test.go,
                   internal/cmd/friction_window_test.go
       covers:     c-1, c-9
       depends_on: t-6, t-11, t-15, t-16
       desc:       Adds the azure-devops arm of doctor's [board] section:
                   - it names each missing base_url / project / auth_env
                   - it checks base_url (forge.CheckADOBaseURL) and hostallow (remote url plus local allow_hosts)
                     before reading $auth_env through an injected getenv, and skips that read when the host is
                     refused. Only the azure-devops arm does this, as all three drafts chose.
                   - it marks a malformed board.api_version or board.auth_scheme ✗ and adds t-11's warnings
                   - with area_path / iteration_path set, it builds the client through boardsync.Config and flags a
                     path that PathExists reports absent
                   Network calls stay in forge; cmd imports no net/http (boundary_test). friction_window_test.go
                   gives azure-devops an allow-listed ADO base_url, because the host check would refuse
                   board.example.com (mvp caught this).
       contract:
         - if a missing field is unnamed, TestDoctorADOBoardNamesMissingFields fails: with base_url, project and
           auth_env unset, doctor prints three ✗ lines naming each, counts 3 issues and exits non-zero
         - if the token is read first, TestDoctorADORefusedHostSkipsToken fails: base_url https://attacker.example with
           $auth_env unset prints a ✗ naming attacker.example, never "is not set in this shell"; the injected getenv
           is called 0 times for auth_env and no HTTP request is made (risk)
         - if a URL form is rejected, TestDoctorADOBoardURLForms fails unless https://dev.azure.com/acme,
           https://acme.visualstudio.com and https://tfs.corp.example/tfs/DefaultCollection (host via remote url or
           allow_hosts) print "✓ [board] is well-formed (azure-devops @ …)"; https://dev.azure.com/acme/Proj is ✗
           naming the <org> form (risk only warned here; ✗ matches t-2's constructor)
         - if api_version/auth_scheme go unchecked, "7.1&x=1" and "token" are each ✗; private-token, a set auth_user
           and milestone_mode agile are ⚠ only, with the exit unaffected
         - if a missing path passes, TestDoctorADOFlagsMissingPath fails: area_path `Fabrikam\Nope` answered 404 is ✗
           naming the path and the project; 200 is ✓; 500 or 203 is ⚠ "could not verify", never ✗ or ✓; unset paths
           send no classification-nodes request; the probe carries api-version=6.0 when [board].api_version = 6.0,
           proving the Config mapping (mvp, risk)
         - if the BoardProviders loops regress, TestDoctorAcceptsEveryDispatchableBoardProvider/azure-devops and
           TestFrictionWindow_DoctorAcceptsWorkingProviders/azure-devops go red; TestDoctorValidatesBoardBlock passes
           unchanged for the other providers (mvp)

Wave 6
  t-20 Prove ADO board commands end to end         [verification t-19 + risk t-5 + mvp t-6] (disputed: D4)
       files:      internal/cmd/ado_board_fake_test.go, internal/cmd/issue_ado_test.go,
                   internal/cmd/phase_complete_board_ado_test.go
       covers:     c-1, c-4, c-5, c-7
       depends_on: t-15, t-17, t-18
       desc:       A stateful httptest Azure Boards fake behind an azure-devops [board] on a different host than
                   [remote] (allow_hosts), driving the real cobra commands. It models backlog configuration, types and
                   states, WIQL by tag or type, batch reads, json-patch creates, field and relation PATCHes, and keeps
                   a request log. It refuses what ADO refuses (risk's rules), so it doesn't just echo the client's
                   assumptions.
       contract:
         - if the fake accepts what ADO rejects, its self-test fails unless: a second Hierarchy-Reverse on a child →
           400 TF201036 with the item unchanged; a create or update without application/json-patch+json → 415; a WIQL
           without [System.TeamProject] = @project → 400; a System.State not in the type's states → 400; a
           workitemsbatch with 201 ids → 400; CONTAINS 'dross/task:p/t-1' does not match dross/task:p/t-10 (risk)
         - if a command skips the ADO client, TestEveryBoardCommandUsesAzureBoards fails unless issue pull, issue phase
           sync, issue task sync, issue milestone sync, issue backlog sync and issue reap (plan) each send ≥1 request
           under /<org>/<project>/_apis/ on the fake and none to the [remote] host
         - if pull leaks mirrors or misses types, TestIssuePullADOEnvelope fails: `issue pull --labels bug,enhancement
           --json` returns {"issues":[7 (type Bug, untagged), 9 (tag enhancement)],"error":null}; item 8 (type Bug,
           tagged dross), a closed Bug and board.json-linked items never appear (risk)
         - if an outage reads as zero, with WIQL answered 503 the same command exits 0 with "issues":[] and a non-null
           "error" naming HTTP 503
         - if "(closed)" is printed over an open item, TestIssueCloseADOReadBack fails: with state writes refused,
           `issue phase sync x --status complete --close` and `issue milestone sync v1.8 --close` exit non-zero naming
           the work item id, and stdout never contains "(closed)"
         - if complete leaves items open or re-writes, TestCompleteFinalizesADOBoard fails: `dross phase complete x`
           leaves both task items then the phase item in Done, with task PATCHes ahead of the phase PATCH in the fake's
           log; a second run sends zero PATCH and zero POST to /_apis/wit/workitems/$* and leaves board.json
           byte-identical

  t-21 Run live Boards smoke and record it         [verification t-20 + risk t-17 + mvp t-7]
       files:      internal/forge/testdata/ado_boards_live_smoke.toml
       covers:     c-8
       depends_on: t-8, t-15, t-17, t-19
       desc:       After `make install` (r-01), configure [board] on the scratch org's project
                   (/home/rivil/development/ado-scratch) and run dross doctor. (risk and mvp gate the run on doctor;
                   verification didn't.) File a Bug by hand. Then, in pair-mode with the installed binary, run the
                   five steps:
                   - create: milestone / phase / task sync
                   - transition: --status in-progress
                   - parent-link: task sync
                   - close: --status complete --close
                   - pull: --labels bug,enhancement --json
                   Fill in each step's result and evidence, with read-backs from read-only REST GETs. A failing step
                   becomes a /dross-quick fix to both the client and the t-20 fake, then a re-run; it never becomes a
                   mock-only pass. This run is the first real test of the API assumptions the mocks encode: project-
                   scoped backlogconfiguration, Tags CONTAINS matching whole tags, System.Tags writes replacing the
                   set, and the status a rev-test failure returns (risk).
       contract:
         - if mock is reported as live, TestADOBoardsLiveRecordHonest (t-8) accepts posture live only with all five
           steps passing and evidence on the real api_base host; any failed step leaves posture partial
         - if a leg is hollow, TestADOBoardsLiveRecordStepEvidence (t-8) rejects it unless the record shows three
           distinct typed ids with _workitems/edit URLs, an InProgress-category transition read-back, a
           Completed-category close read-back, a task → phase → Epic Hierarchy-Reverse chain, and a pull envelope with
           "error":null listing the hand-filed Bug and none of the dross ids
         - if the run never happens, the committed record stays mock-only and the honesty test rejects any edit that
           flips it to live without run_at, api_version, api_base and process
```

## Coverage

| Criterion | Merged tasks |
|---|---|
| c-1 | t-1, t-2, t-3, t-11, t-15, t-16, t-19, t-20 |
| c-2 | t-6, t-7, t-12, t-17, t-18 |
| c-3 | t-6, t-13, t-16 |
| c-4 | t-13, t-16, t-17, t-18, t-20 |
| c-5 | t-12, t-20 |
| c-6 | t-7, t-13, t-18 |
| c-7 | t-18, t-20 |
| c-8 | t-4, t-8, t-21 |
| c-9 | t-1, t-3, t-6, t-12, t-16, t-19 |
| c-10 | t-5, t-9, t-10, t-14 |

All 10 criteria are covered. The deferred item d4c0a4f04bb53856 is absorbed by t-5, t-9, t-10 and t-14.

## Disagreements

### D1. What "shipped" maps to on Azure Boards
- **What diverged:** the default state category for the `shipped` lifecycle status.
- **mvp:** Completed, like Jira's "Done". mvp rejected Resolved because it collides with uat.
- **verification:** Completed, plus one azure-devops Phases-lane exemption for complete/shipped that carries its why. This mirrors Jira's existing exemption.
- **risk:** Resolved, falling back to InProgress where the type has no Resolved state. risk treats shipped as an in-review status, and rejected Completed because the phase lane's working and terminal states would then match.
- **Provisional default:** Completed, plus the lane exemption (t-13).
- **Why it matters:**
  - **When shipped is written.** `dross ship` writes it when it opens the PR (internal/cmd/ship.go:509), before merge and before `phase complete`.
  - **Completed:** the PBI or User Story shows Done/Closed on the team board while its PR is still open. Finalize's `Resolved || State == "closed"` skip then treats the phase card as already finished.
  - **Resolved:** the item stays visibly "in review" on Agile. But uat also maps to Resolved, and the existing distinctPairs guard {uat, shipped} in internal/forge/state_map_test.go forbids sharing a category, with no exemption mechanism. risk's map passes only because it never registers ADO in that guard. On Scrum and Basic, risk's shipped also collapses into InProgress.
  - **Overrides and the lock.** Either default can be overridden per project (`state_map.shipped`). The locked state_resolution decision says "in-review statuses use Resolved" but doesn't list which statuses count as in-review, so this is the user's call.

### D2. How parent-child links are written
- **What diverged:** the mechanism for phase→Epic and task→phase Hierarchy links.
- **mvp:** no new interface. Phase→Epic rides IssueInput.Milestone: CreateIssue adds the Hierarchy-Reverse, and UpdateIssue adds it only if missing. Task→phase is a concrete `*AzureBoardsClient.LinkParent` call in task.go.
- **risk:**
  - A new forge.ParentLinker interface.
  - CreateIssue carries the parent op for in.Milestone atomically, and UpdateIssue ignores Milestone.
  - SyncPhase re-parents an updated phase via LinkParent in one PATCH (test /rev, remove old, add new).
  - Tasks, including cards that finalize creates, are parented through LinkParent.
- **verification:** ParentLinker is the only parent path. CreateIssue and UpdateIssue both ignore Milestone, and every parent link is a LinkParent call after the create.
- **Provisional default:** risk's hybrid (t-7, t-12, t-17, t-18). It is the only position consistent with both majorities: Milestone on create (mvp + risk) and a ParentLinker interface (risk + verification).
- **Why it matters:**
  - **One parent per item.** ADO rejects a second Hierarchy-Reverse with TF201036.
  - **mvp:** a phase moved to another milestone is unspecified and likely a hard error.
  - **verification:** the cleanest single path, but cards created with IssueInput.Milestone are left outside the Epic: the phase card finalize creates (finalize.go:68) and backlog items (backlog.go:454).
  - **risk:** pays with two link paths, the create-time op and LinkParent.
  - **What it decides.** The forge.go surface and the contracts of four tasks.

### D3. Scope and seam of the c-10 chore-PR fix
- **What diverged:** whether `dross milestone complete` is in scope, and where the protection dispatch lives.
- **mvp:**
  - **Scope:** it fixes only routeBaseChores, which covers phase complete, finalizeBoard, and the safety nets in ship and ship-recover. That matches c-10's text exactly. routeProtectedMilestone and waitForMilestoneChores stay GitHub-only.
  - **Seam:** BranchRules itself dispatches azure-devops and returns a synthesized push-refusing LiveRule, so protect.RequiresPR and every caller work unchanged.
- **risk:** all three functions move to a new ship.BaseProtection (internal/ship/protection.go), in one cmd task.
- **verification:** a cmd/baseprotect.go dispatch (github → BranchRulesFunc; azure-devops → ship.ADOBranchProtectionFunc) for routeBaseChores, then a separate task for the milestone path plus an ADO ship → complete twin.
- **Provisional default:** include the milestone path (t-14), using verification's cmd-side seam (t-10).
- **Why it matters:**
  - **Scope.** The deferred item c-10 absorbs (d4c0a4f04bb53856) names routeProtectedMilestone in milestone.go. Without it, `dross milestone complete` still pushes directly to a policy-protected ADO milestone branch and gets TF402455: the same bug, one command later.
  - **Cost.** One 3-file task in wave 3, off the critical path.
  - **Seam.** mvp's synthesized rule touches fewer files, but it makes a GitHub rules API answer for ADO. doctor_protection.go and protect.go are github-gated today, so nothing leaks yet.

### D4. Where the end-to-end proof lives, and what fake it runs on
- **What diverged:** whether the proofs run at boardsync or cobra level, and whether a stateful ADO fake is built first and independently of the client.
- **mvp:** one stateful fake in boardsync, folded into the sync task. No cmd-level tests, because "the pull/envelope path is provider-agnostic and already tested".
- **risk:** a stateful fake built first, as its own wave-1 task. It is modelled on ADO's documented refusals rather than on the client, self-tested and strict at cleanup. Every boardsync proof runs on it, and there are no cmd-level tests.
- **verification:** boardsync proofs on scriptedTracker fixtures, plus one stateful fake in internal/cmd (wave 6) that drives the real commands.
- **Provisional default:** verification's two levels (t-16 to t-18, plus t-20). risk's refusal rules are grafted onto the t-20 fake as its own self-test contract.
- **Why it matters:**
  - **For cobra level.** c-1 ("every board command"), c-5 (`dross issue pull`) and c-7 (`dross phase complete`) name commands, and only a cobra-level run proves the wiring that dross-verify will map them to.
  - **risk's objection still stands.** Scripted per-test handlers encode the client's own assumptions and can't express the one-parent or rev rules. A fake that lands in wave 6 also surfaces client mistakes late.
  - **Adding risk's wave-1 fake as well** costs one more task and a second fake, because _test.go helpers can't be shared across packages.

### D5. Shared honesty check vs a test-local mirror for the live record
- **What diverged:** how c-8's honesty check is implemented, and when the template lands.
- **verification:** extract the posture, loopback, exactly-once and run_at rules into a new internal/liverecord package shared by both records (t-4). This refactors ship's existing record test.
- **mvp and risk:** a test-local validator in internal/forge that mirrors internal/ship/ado_live_record_test.go. mvp also folds the template, validator and live run into one final task. risk and verification land the template early and the run last.
- **Provisional default:** extract (t-4), and split template (t-8) from run (t-21).
- **Why it matters:**
  - **What c-8 asks.** "The same honesty check as the ship smoke". Extraction enforces sameness; a mirror only claims it and can drift.
  - **Cost.** A new package, plus a refactor of the test that guards an already-recorded live run. That test must keep accepting internal/ship/testdata/ado_live_smoke.toml unchanged. Either way the work is off the critical path.

### D6. Whether [board].auth_scheme applies to non-ADO boards
- **What diverged:** which providers boardsync.Config passes [board].auth_scheme to.
- **risk:** azure-devops only. Doctor warns that the key has no effect on any other board.
- **verification:** every provider, so a GitLab board honours it. Its reasoning: an ignored key that reads as configured is exactly what doctor exists to catch.
- **mvp:** maps it into forge.Config without saying for which providers.
- **Provisional default:** ADO-only plus the warning (t-16, t-11).
- **Why it matters:** forge.Config.AuthScheme already selects GitLab's credential header. Passing the new key through changes how GitLab boards authenticate, which is outside this phase's criteria. No existing project sets the key, so the risk is future misconfiguration rather than a regression. The question is whether [board].auth_scheme is an ADO key or a general [board] key.
