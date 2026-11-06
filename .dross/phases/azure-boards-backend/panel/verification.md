# Planner draft — verification lens

Lens: each criterion's ideal test contract was written first, then the smallest task that makes it satisfiable.
Every task is `status = pending`. Mocked httptest suites are the per-criterion bar (verify_posture). c-8's live
run is a separate task and never stands in for them.

```
Phase azure-boards-backend — 20 tasks across 6 waves

Wave 1
  t-1  Add ADO fields to the board block
       files:    internal/project/project.go, internal/cmd/project.go, internal/cmd/project_test.go,
                 internal/cmd/testdata/pathfence_scan/not_paths.txt
       desc:     project.Board gains api_version (the APIVersion type, so a bare 6.0 decodes), auth_scheme,
                 area_path and iteration_path (omitempty toml+json tags). `dross project get/set` wires
                 board.api_version, board.auth_scheme (added to enumKeys → configenum.AuthSchemes),
                 board.area_path and board.iteration_path. Four not_paths.txt rows.
       covers:   c-1, c-9
       depends:  —
       contract: if a key is unwired, TestProjectSetGetADOBoardKeys fails: set then get round-trips
                 board.api_version=6.0, board.auth_scheme=bearer, board.area_path=`Fabrikam\Web` and
                 board.iteration_path=`Fabrikam\Sprint 1` verbatim, backslashes intact
                 if auth_scheme is not enum-checked, `dross project set board.auth_scheme token` exits non-zero
                 naming the AuthSchemes list and leaves project.toml byte-identical
                 if the float decode is lost, TestProjectGetBoardAPIVersionBareNumber fails: a project.toml with
                 `[board] api_version = 6.0` makes `dross project get board.api_version` print "6.0", never "6"
                 if a zero value leaks into the file, a project.toml with no ADO [board] keys saves byte-identical
                 if a field is left unfiled, TestEveryPathShapedFieldIsDeclared fails until
                 project.Board.{APIVersion,AuthScheme,AreaPath,IterationPath} are in not_paths.txt, and
                 TestTomlFieldsCarryMatchingJSONTags fails if any one lacks a json tag

  t-2  Build Azure Boards transport and constructor
       files:    internal/forge/forge.go, internal/forge/azureboards.go, internal/forge/azureboards_test.go,
                 internal/secretscan/sinks.go
       desc:     forge.Config gains APIVersion, AreaPath and IterationPath. NewAzureBoards names each missing
                 base_url/project/auth_env, checks the api_version shape (default 7.1) and the base_url shape
                 (exported CheckADOBaseURL), checks the host allowlist before Getenv, and makes no request.
                 doRaw: ScanPayload before encoding, then the api-version query param, Basic ":"+PAT (or Bearer),
                 X-TFS-FedAuthRedirect: Suppress, no followed redirects, sign-in → error, and redact.Scrub on every
                 body. Work-item writes use the json-patch content type. Declared in secretscan.Transports().
       covers:   c-1
       depends:  —
       contract: if a field check is dropped, TestNewAzureBoardsNamesMissingFields fails: an empty base_url, project
                 or auth_env each errors naming [board].base_url / [board].project / [board].auth_env, with zero requests
                 if the token is read before the host check, TestNewAzureBoardsRefusesHostBeforeToken fails:
                 base_url https://attacker.example with auth_env unset gives errors.Is(err, hostallow.ErrRefused),
                 never "is not set"
                 if Services or Server URLs are mishandled, TestADOBoardBaseURLShapes fails: https://dev.azure.com/acme,
                 https://acme.visualstudio.com, https://acme.visualstudio.com/DefaultCollection and
                 https://tfs.corp.example/tfs/DefaultCollection are accepted; https://dev.azure.com and
                 https://dev.azure.com/acme/Proj are refused naming [board].base_url and the https://dev.azure.com/<org> form
                 if api-version or auth drift, TestADOBoardRequestShape fails: every request carries api-version=7.1
                 by default and 6.0 when APIVersion=6.0. Authorization is Basic base64(":"+PAT) by default and
                 "Bearer <PAT>" under AuthScheme bearer. X-TFS-FedAuthRedirect: Suppress is set. An api_version of
                 "7.1&x=1" is refused at construction with zero requests
                 if the credential can leak, a 400 body echoing the PAT and base64(":"+PAT) yields an error with
                 neither; a 302 to a second httptest server is not followed (it records zero hits) and the error
                 names the sign-in redirect
                 if the transport is undeclared or unscreened, TestEveryOutboundSeamIsRegistered /
                 TestScreenPrecedesRequestInEveryScreenedTransport (internal/cmd) report forge.AzureBoardsClient.doRaw

  t-3  Document ADO board keys in options
       files:    assets/prompts/options.md, internal/cmd/options_docs_test.go
       desc:     §12 adds azure-devops to board.provider and documents board.api_version (7.1 default; Server
                 2019 5.0, 2020 6.0, 2022 7.0) and board.auth_scheme (Basic with an empty user + PAT by default,
                 bearer for Entra). It also documents board.area_path / board.iteration_path (backslash form;
                 unset = project defaults), notes that [board] never inherits [remote], and that milestone_mode
                 is ignored because milestones are Epics. Not live until `make install` (r-01).
       covers:   c-1, c-9
       depends:  —
       contract: if a key goes undocumented, TestOptionsCoversADOBoardFields fails unless §12 names azure-devops,
                 board.api_version with `7.1`, board.auth_scheme with `bearer`, board.area_path and board.iteration_path
                 if the no-inheritance rule is lost, the same test fails unless §12 says [board] does not inherit [remote]

  t-4  Extract the live-record honesty check
       files:    internal/liverecord/liverecord.go, internal/liverecord/liverecord_test.go,
                 internal/ship/ado_live_record_test.go
       desc:     Move posture rules (mock-only | partial | live), the loopback screen, exactly-once step keys,
                 the RFC 3339 run_at check and "evidence names an ADO id or a URL on api_base's host" into
                 liverecord.Validate(rec, keys, proof). Plain structs with no toml/json tags. Ship's test decodes
                 into its test-local struct, converts it, and keeps its own per-step proofs and statusless-thread rule.
       covers:   c-8
       depends:  —
       contract: if the extraction changes the ship verdict, TestADOLiveRecordHonest and TestADOLiveRecordStepEvidence
                 still pass unchanged on internal/ship/testdata/ado_live_smoke.toml (posture live), and their
                 fixtures still give reject / accept-as-mock-only / accept-as-live
                 if a posture rule weakens, TestValidatePostures fails unless each of these is rejected: live with
                 one not-run step; partial with every step pass; mock-only carrying a pass; a step key twice or an
                 unknown key; an api_base or evidence naming 127.0.0.1/localhost/[::1]; a pass naming neither an ADO
                 id nor a URL on api_base's host; run_at "yesterday"
                 if a pathfence ledger row becomes necessary, TestEveryPathShapedFieldIsDeclared stays green with
                 no not_paths.txt edit, because liverecord types carry no serialization tags

  t-5  Read ADO branch policies as push protection
       files:    internal/ship/azuredevops_policy.go, internal/ship/azuredevops_policy_test.go,
                 internal/ship/hostguard_test.go
       desc:     ADOBranchProtection(opts, branch) lists <api_base>/<project>/_apis/policy/configurations
                 (paged) and resolves the repo id. It reports Protected when a config that is enabled AND blocking
                 is scoped to this repo (or all repos) with either matchKind Exact == refs/heads/<branch> or a
                 Prefix the ref starts with. Any read failure is Known=false with a reason. GETs only. It has the
                 overridable seam ADOBranchProtectionFunc and exposes the scoped configs for t-9.
       covers:   c-10
       depends:  —
       contract: if scoping is wrong, TestADOBranchProtectionScopes fails unless it reports protected for
                 {enabled, blocking, Exact refs/heads/main, this repo} on main and for {enabled, blocking, Prefix
                 refs/heads/milestone/, repositoryId null} on milestone/v1.8. It must report unprotected when the
                 only config has isBlocking false or isEnabled false, is scoped to another repo id, or is Exact
                 refs/heads/main checked against milestone/v1.8
                 if a failed read reads as unprotected, a 401, a 500 and a non-JSON body each give Known=false with
                 a reason naming the policy read, never Known=true/Protected=false
                 if dross writes a policy, TestADOBranchProtectionReadOnly fails when any request is not a GET
                 if the host check is skipped, the new azure-devops branch-policy row of
                 TestShipRefusesOffAllowlistAPIBase sees fake-host hits or a reason without the host refusal

Wave 2
  t-6  Read ADO process metadata and work items
       files:    internal/forge/azureboards_meta.go, internal/forge/azureboards_meta_test.go,
                 internal/redact/redact_test.go
       desc:     Lazy, cached reads: the requirement type from <project>/_apis/work/backlogconfiguration
                 (requirementBacklog default type), work item type names, each type's states with category in API
                 order, and CheckPaths(area, iteration) via classificationnodes (404 = missing). GetIssue maps
                 id, title, description, System.Tags (dross-vocabulary tags lowercased) and System.State into
                 Issue. WorkflowState is the state name, Resolved is true iff the category is Completed, and State
                 is closed iff Resolved.
       covers:   c-2, c-3, c-9
       depends:  t-2
       contract: if the requirement type is guessed, TestADORequirementTypeFromBacklogConfig fails: Agile, Scrum and
                 Basic fixtures yield User Story, Product Backlog Item and Issue. A 500 is an error naming the backlog
                 configuration, never a "User Story" fallback, and a second call makes no request
                 if categories are misread, TestADOStatesByCategory fails: GET .../workitemtypes/Product%20Backlog%20Item/states
                 (escaped once, never %2520) yields New/Approved Proposed, Committed InProgress, Done Completed,
                 Removed Removed, in API order
                 if close truth comes from the name, TestADOGetIssueResolved fails: a Task in Done (Completed) reads
                 Resolved=true, State closed, WorkflowState Done; Resolved (category Resolved) and Removed read
                 Resolved=false, State open
                 if tag casing breaks identity, System.Tags "Dross; DROSS/phase:p; Bug" reads back as Labels
                 [dross, dross/phase:p, Bug]
                 if a bad key reaches the wire, GetIssue("PROJ-7") errors with zero requests
                 if a missing path reads as present, TestADOCheckPaths fails: area `Fabrikam\Web` GETs
                 .../Fabrikam/_apis/wit/classificationnodes/Areas/Web and a 404 reports it missing; `Fabrikam` alone
                 GETs .../classificationnodes/Areas; iteration `Fabrikam\Sprint 1` GETs .../Iterations/Sprint%201;
                 a 500 is an error, never "missing"
                 if the scrub is removed, the new AzureBoardsClient row of TestEveryForgeClientRedacts fails: a canary
                 token echoed by the server never appears in GetIssue's error

  t-7  Relate and parent ADO work items
       files:    internal/forge/forge.go, internal/forge/azureboards_links.go, internal/forge/azureboards_links_test.go
       desc:     New optional forge.ParentLinker {LinkParent(parent, child string) error} beside IssueLinker.
                 LinkIssues adds System.LinkTypes.Related. LinkParent adds System.LinkTypes.Hierarchy-Reverse on the
                 child, replacing a different existing parent in the same PATCH. Both read relations first and
                 send nothing when the link exists. Compile-time IssueLinker + ParentLinker assertions.
       covers:   c-2, c-6
       depends:  t-2
       contract: if relate is not idempotent, TestADOLinkIssuesRelated fails: item 10 with no relations gets one PATCH
                 to /_apis/wit/workitems/10 adding rel System.LinkTypes.Related with url .../_apis/wit/workItems/20;
                 when GET 10?$expand=relations already lists it, zero PATCH
                 if parenting uses the wrong link, TestADOLinkParent fails: LinkParent("5","9") PATCHes item 9 with rel
                 System.LinkTypes.Hierarchy-Reverse to workItems/5, never Related. If 9 is already under 5, zero PATCH.
                 If it is under 4, one PATCH whose remove of the old relation index precedes the add
                 if GitHub gains a lossy stub, TestGitHubIsNotAParentLinker fails when *GitHubClient satisfies ParentLinker

  t-8  Add Boards live-smoke record and validator
       files:    internal/forge/testdata/ado_boards_live_smoke.toml, internal/forge/ado_boards_live_record_test.go
       desc:     The template's comments are the runbook. Prerequisites: a scratch ADO project (the ado-scratch
                 repo) with dross init; [board] provider azure-devops with base_url, project and auth_env; the
                 process name; a hand-filed Bug before the pull step; `make install` first. Steps create,
                 transition, close, parent-link and pull all start not-run; posture is mock-only. Validated via
                 liverecord.Validate plus per-step proofs.
       covers:   c-8
       depends:  t-4
       contract: if mock can be filed as live, TestADOBoardsLiveRecordHonest fails when posture = live while any step
                 is not-run/fail or run_at, api_version, api_base or process is missing; the committed template
                 validates as mock-only
                 if a hollow leg passes, TestADOBoardsLiveRecordStepEvidence rejects each of these:
                   - create without a .../_workitems/edit/<id> URL on api_base's host and its type
                   - transition without a read-back state that differs from the created state
                   - close without a read-back state with category Completed
                   - parent-link without child id, parent id and rel System.LinkTypes.Hierarchy-Reverse naming the parent
                   - pull without an envelope that has "error":null, lists the hand-filed item and no dross-tagged id
                 if httptest evidence slips in, a step naming 127.0.0.1 or localhost is rejected
                 if the runbook loses its prerequisites, the test fails unless the comments name dross init on a
                 scratch ADO project, provider = "azure-devops", the process, the hand-filed Bug and make install

  t-9  Arm ADO chore auto-complete with merge commit
       files:    internal/ship/automerge.go, internal/ship/azuredevops_automerge.go,
                 internal/ship/azuredevops_automerge_test.go, internal/ship/hostguard_test.go
       desc:     AutoMergePR gains an azure-devops arm that accepts method "merge" only. A target merge-strategy
                 policy that forbids merge commits (allowNoFastForward false, read via t-5) gives
                 ErrAutoMergeUnavailable without a write. Otherwise it PATCHes pullrequests/<n> with
                 autoCompleteSetBy = the connectionData identity and mergeStrategy noFastForward, never
                 bypassPolicy. It reads the outcome back, and a refused PATCH is ErrAutoMergeUnavailable.
       covers:   c-10
       depends:  t-5
       contract: if the payload drifts, TestADOArmChoreAutoComplete fails: exactly one PATCH to .../pullrequests/41
                 whose autoCompleteSetBy.id equals connectionData's authenticatedUser id and whose
                 completionOptions.mergeStrategy = "noFastForward"; bypassPolicy is absent or false, bypassReason absent
                 if a merge-strategy policy is ignored, a blocking "Require a merge strategy" config on the target ref
                 with allowNoFastForward false gives errors.Is(err, ErrAutoMergeUnavailable) and zero PATCH
                 if a refusal is a hard error, a 400 or 403 on the PATCH gives ErrAutoMergeUnavailable; a 500 gives a
                 plain error that is not ErrAutoMergeUnavailable
                 if the outcome is guessed, read-back completed → Merged; active with autoCompleteSetBy → AutoEnabled;
                 active without it → error
                 if the phase-PR rule regresses, TestADOCompletePayload still bans autoCompleteSetBy in the phase
                 completion body
                 if another method is armed, method "squash" on azure-devops errors with zero requests
                 if the host check is skipped, the azure-devops auto-complete row of TestShipRefusesOffAllowlistAPIBase
                 sees zero fake-host hits and ErrRefused

  t-10 Route base chores through ADO chore PRs
       files:    internal/cmd/baseprotect.go, internal/cmd/basebranch.go, internal/cmd/chorepr.go,
                 internal/cmd/basebranch_ado_test.go
       desc:     baseprotect.go is one protection dispatch: github → BranchRulesFunc+RequiresPR; azure-devops →
                 ship.ADOBranchProtectionFunc; others → not checked. routeBaseChores uses it. chorePR learns its
                 provider: on ADO its narration says auto-complete / "complete it by hand", and pullURL builds
                 <url>/pullrequest/<n>.
       covers:   c-10
       depends:  t-5
       contract: if an ADO base is pushed, TestProtectedADOBaseRoutesThroughChorePR fails. With choreBase on an
                 azure-devops remote, a hook rejecting main and the policy seam stubbed protected, routeBaseChores
                 must open one chore PR with dross-chores/main == local main, offer the hook no refs/heads/main,
                 and arm only ["merge"]
                 if an unprotected ADO base is routed, a stub answering unprotected gives Pushed=true,
                 origin main == local main, no chore branch and zero PR opens
                 if an unknown read pushes, a Known=false stub gives an error naming the reason (never "gh auth
                 login") and an empty hook log
                 if code rides a chore PR, a non-.dross commit ahead on a protected ADO main is refused naming the
                 file, with zero PRs opened
                 if narration stays GitHub-shaped, TestChorePRNarrationADO fails: Manual on azure-devops says "complete
                 it by hand" and the URL is .../_git/r/pullrequest/41, never /pull/41
                 if GitHub regresses, every basebranch_chores_test.go and chorepr_test.go case passes unchanged

Wave 3 (depends t-6, t-10)
  t-11 Create, update and list ADO work items
       files:    internal/forge/azureboards_items.go, internal/forge/azureboards_items_test.go,
                 internal/forge/secretscan_test.go
       desc:     CreateIssue POSTs $Task when the labels carry dross/task:, otherwise $<requirement type>. It sends
                 ops for System.Title, System.Description and System.Tags (joined "; "), plus
                 System.AreaPath/IterationPath only when configured.
                 UpdateIssue PATCHes only the fields given: Labels replace the whole tag set, and Milestone is
                 ignored because parents go through ParentLinker.
                 ListIssues runs a WIQL query scoped to @project. Labels are OR'd as tag CONTAINS clauses, plus a
                 [System.WorkItemType] = clause for a label that names an existing type (case-insensitive), with ''
                 quoting. State open drops the Completed and Removed categories, and ids are read in batches of
                 200 or fewer.
                 EnsureMilestoneEpic(version, title, desc) finds the Epic tagged dross/milestone:<version> by WIQL or
                 POSTs $Epic; EnsureMilestone delegates to it.
       covers:   c-2, c-5, c-9
       depends:  t-6
       contract: if type selection breaks, TestADOCreateIssueType fails: [dross, dross/phase:p, dross/task:p/t-1] POSTs
                 .../workitems/$Task; [dross, dross/phase:p] on Scrum POSTs .../workitems/$Product%20Backlog%20Item
                 with Content-Type application/json-patch+json and a System.Tags value "dross; dross/phase:p"
                 if area/iteration leak or drop, TestADOCreateIssueClassification fails: with AreaPath `Fabrikam\Web`
                 and IterationPath `Fabrikam\Sprint 1`, the requirement, Task and Epic creates each carry
                 /fields/System.AreaPath and /fields/System.IterationPath with those values; with both unset none
                 of the three bodies has either op
                 if the Epic duplicates, TestADOEnsureMilestoneEpic fails: WIQL answering id 3 returns "3" with zero
                 POST. An empty WIQL gives one POST .../workitems/$Epic tagged "dross; dross/milestone:v1.8" and
                 titled "v1.8 — <title>", and a later call whose WIQL finds it sends no second POST
                 if pull matching is tag-only or type-only, TestADOListIssuesLabelMatch fails. Labels [bug, enhancement]
                 on Agile types must send WIQL with [System.Tags] CONTAINS 'bug' OR [System.WorkItemType] = 'Bug' OR
                 [System.Tags] CONTAINS 'enhancement' and no type clause for enhancement. Basic types (no Bug) get no
                 type clause at all, and `o'brien` is sent as 'o''brien'
                 if open includes done work, State open omits Done (Completed) and Removed items and keeps Active;
                 State all keeps all three
                 if failure reads as zero, a 500 on WIQL or on the work-item-types read returns an error, never an
                 empty slice with a nil error
                 if large reads break, 250 WIQL ids are fetched in two batch requests of 200 or fewer, and all 250
                 are returned
                 if the payload screen is bypassed, the azure-devops row added to allBackends() fails
                 TestCreateIssueRefusedBeforeRequest / TestUpdateIssueBodyPatchRefused when a token in the body
                 reaches the server

  t-12 Resolve ADO states by category
       files:    internal/forge/azureboards_state.go, internal/forge/azureboards_state_test.go,
                 internal/forge/state_map_test.go, internal/boardsync/board_lifecycle_divergence_test.go
       desc:     defaultADOStateCategory is a map[string]string literal keyed by LifecycleStatuses:
                   - planned → Proposed
                   - in-progress, task-in-progress → InProgress
                   - uat, task-in-review → Resolved (InProgress where the type has none)
                   - shipped, complete, task-complete → Completed
                 The state written is the first state of that category for the type. [board].state_map overrides
                 by name.
                 SetState warns and skips with no PATCH when no state is reachable, or on a 400 rule rejection.
                 CloseIssueAs/CloseIssue write the state, read it back, and error unless Resolved. SetStateRaw
                 writes the literal and verifies WorkflowState. Asserts StateWriter.
       covers:   c-3, c-4, c-6
       depends:  t-6
       contract: if defaults become per-process tables, TestADODefaultStateResolution fails on any of these rows:
                   - Scrum PBI: in-progress→Committed, uat→Committed, planned→New, complete→Done
                   - Scrum Task: task-in-progress→In Progress, task-in-review→In Progress, task-complete→Done
                   - Agile User Story: in-progress→Active, uat→Resolved, complete→Closed
                   - Agile Task: task-in-review→Active
                   - Agile Epic: complete→Closed
                   - Basic Issue: in-progress→Doing, uat→Doing, complete→Done
                 if the override is ignored, state_map {in-progress: Approved} PATCHes System.State Approved on a PBI.
                 An override naming a state the Task type lacks warns on stderr naming the item and the state,
                 sends zero PATCH and returns nil
                 if an unreachable state fails the sync, a type with no Proposed state given "planned" warns and
                 returns nil with zero PATCH. A 400 on the state PATCH warns and returns nil; a 500 returns an error
                 if a close is trusted unread, TestADOCloseIssueAsVerifies fails: read-back Done → nil; read-back
                 Active → error naming work item 42 and the written state; an override blanking complete → error,
                 zero PATCH
                 if undo goes through the map, TestADOSetStateRawLiteral fails: SetStateRaw("42","Resolved") PATCHes
                 Resolved though no dross status maps to it, and a read-back of Closed errors naming 42
                 if a key goes missing or the lifecycle collapses, the new azure-devops rows of
                 TestStateMapsKeyExactlyTheEmittedStatuses, TestStateMapsKeepTheLifecycleStatesDistinct,
                 TestEveryReapTerminalIsMapped and TestLaneTerminalStatesAreDistinctWithinTheirLane fail on
                 defaultADOStateCategory. The last one passes only with one azure-devops Phases complete/shipped
                 exemption that carries its why

  t-13 Route milestone chores; prove ADO ship-to-complete
       files:    internal/cmd/milestone.go, internal/cmd/milestone_protected_ado_test.go,
                 internal/cmd/protected_base_ado_test.go
       desc:     routeProtectedMilestone and waitForMilestoneChores ask baseprotect's dispatch instead of GitHub
                 only, and the waiting error's PR URL is provider-shaped. ADO twins of the protected
                 ship → complete flow.
       covers:   c-10
       depends:  t-10
       contract: if milestone chores push, TestADOMilestoneChoresRouteThroughChorePR fails: a protected milestone/v1.8
                 gets dross-chores/milestone-v1.8, and the hook is never offered refs/heads/milestone/v1.8
                 if the integration PR outruns its chores, TestADOMilestoneCompleteWaitsForOpenChorePR fails: an open
                 ADO chore PR into milestone/v1.8 makes milestone complete error naming .../pullrequest/<n>
                 if ship's safety net or phase complete pushes, TestProtectedADOBaseShipThenComplete fails. On an
                 azure-devops remote with protected main, `dross ship` then `dross phase complete x` must reach
                 origin only via dross-chores/main, the pre-receive log must hold no refs/heads/main, and every
                 armed method must be "merge"
                 if unprotected ADO bases lose the push, TestUnprotectedADOBaseStillPushesDirectly fails unless origin
                 main == local main after ship and no dross-chores/main exists

Wave 4 (depends t-1, t-11, t-12)
  t-14 Dispatch azure-devops boards through NewBoard
       files:    internal/configenum/configenum.go, internal/configenum/configenum_test.go, internal/forge/forge.go,
                 internal/forge/azureboards_dispatch_test.go
       desc:     BoardProviders gains azure-devops; NewBoard routes it to NewAzureBoards. Adds
                 var _ BoardClient = (*AzureBoardsClient)(nil). MilestoneModesFor(azure-devops) stays nil, because
                 milestones are always Epics.
       covers:   c-1
       depends:  t-11, t-12
       contract: if dispatch and the accept-set diverge, TestBoardDispatchMatchesBoardProviders fails
                 if normalisation is skipped, TestNewBoardDispatchesAzureDevOps fails unless
                 NewBoard(Config{Provider: " Azure-DevOps ", ...}) returns a *AzureBoardsClient
                 if the set text drifts, configenum_test's board row must read
                 "forgejo | gitea | gitlab | youtrack | jira | github | azure-devops"
                 if a command path rejects it, TestIssueEnableAcceptsEveryBoardProvider/azure-devops and
                 TestDoctorAcceptsEveryDispatchableBoardProvider/azure-devops fail
                 if milestone_mode starts to apply, MilestoneModesFor("azure-devops") must stay nil

  t-15 Drive ADO states and verified closes
       files:    internal/boardsync/ctx.go, internal/boardsync/phase.go, internal/boardsync/task.go,
                 internal/boardsync/azureboards_state_sync_test.go
       desc:     Config carries api_version, auth_scheme, area_path and iteration_path into forge.Config.
                 SyncPhase and setBoardState gain an AzureBoardsClient arm (SetState with [board].state_map).
                 CloseIssue gains one too (CloseIssueAs, read-back). An adoRepo helper in the new test file (scriptedTracker +
                 NewAzureBoards via Config) carries Agile/Scrum/Basic metadata fixtures.
       covers:   c-3, c-4
       depends:  t-1, t-11, t-12
       contract: if Config drops a field, TestConfigCarriesADOFields fails unless Config(Board{api_version 6.0,
                 auth_scheme bearer, area_path, iteration_path}) carries all four, and the tracker sees api-version=6.0
                 with a Bearer header
                 if a process resolves wrong in sync, TestADOSyncStateByProcess fails. SyncPhase --status in-progress
                 must PATCH System.State Active / Committed / Doing (Agile/Scrum/Basic). SyncTasks --status
                 task-in-progress must PATCH Active / In Progress / Doing on each task item
                 if state_map is not passed, [board].state_map {in-progress: Approved} makes the Scrum phase PATCH
                 carry Approved
                 if an unreachable state fails the sync, a state_map value the type lacks warns on stderr, sends no
                 state PATCH, and SyncPhase returns nil with board.json saved
                 if "(closed)" outruns the tracker, TestADOPhaseCloseReadBack fails: --status complete --close with
                 read-back Active errors naming the work item and ctx.Out has no "(closed)"; read-back Done prints it
                 if one refused task close strands the rest, SyncTasks --status task-complete --close with task 11
                 reading back Active and task 12 Done errors naming t-1 only, and task 12 still got its close PATCH

Wave 5 (depends t-7, t-14, t-15)
  t-16 Join ADO items by Epic and parents
       files:    internal/boardsync/milestone.go, internal/boardsync/phase.go, internal/boardsync/task.go,
                 internal/boardsync/azureboards_hierarchy_test.go
       desc:     EnsureMilestoneLink gains an ADO arm (EnsureMilestoneEpic(version, title, desc)).
                 CheckMilestoneClosable accepts ADO, because Epics are closable. SyncPhase parents the phase item
                 under the milestone Epic via ParentLinker. SyncTasks prefers ParentLinker over IssueLinker for
                 task → phase.
       covers:   c-2, c-4
       depends:  t-7, t-15
       contract: if the hierarchy is wrong, TestADOSyncBuildsHierarchy fails unless:
                   - milestone sync POSTs $Epic
                   - phase sync POSTs $User%20Story / $Product%20Backlog%20Item / $Issue (Agile/Scrum/Basic), then
                     PATCHes Hierarchy-Reverse to the Epic
                   - task sync POSTs $Task per task, each PATCHed Hierarchy-Reverse to the phase item
                   - no task is ever sent a System.LinkTypes.Related relation
                 if a re-run duplicates, TestADOResyncUpdatesInPlace fails. With board.json emptied and WIQL returning
                 the tagged items, the re-run sends PATCH /workitems/<id> only: zero POST to /_apis/wit/workitems/$*,
                 and zero relation PATCH for links already present
                 if the milestone close is refused or unverified, TestADOMilestoneClose fails:
                 CheckMilestoneClosable(ADO) is nil; closing the Epic reads back Closed → nil, Active → error naming
                 it; forgejo is still refused
                 if a linker-only backend loses links, the linkBoard double still receives LinkIssues(parent, task)
                 for every task

  t-17 Prove ADO backlog links, undo, finalize
       files:    internal/boardsync/azureboards_backlog_test.go, internal/boardsync/azureboards_finalize_test.go
       desc:     A proof task over the wired client: routed backlog items relate to their target phase item, reap
                 apply then undo restores the exact prior state, and FinalizePhase closes the tasks, then the
                 phase, and a re-run only reads.
       covers:   c-4, c-6, c-7
       depends:  t-7, t-15
       contract: if backlog links are wrong or repeat, TestADOBacklogRelatesRoutedItem fails: a deferred item routed
                 to phase p (item 20) gets one PATCH adding System.LinkTypes.Related to workItems/20; a second
                 SyncBacklog sends zero relation PATCH
                 if undo is not exact, TestADOReapUndoRestoresPriorState fails: a card swept from Committed is restored
                 by PATCH System.State "Committed" plus its prior tags; a read-back of Done makes Undo return "1 of 1
                 card(s) could not be restored" naming the item
                 if finalize order or terminal states break, TestADOFinalizeTasksThenPhase fails: both task items
                 PATCH to Done before the phase item's PATCH to Done, and each reads back Completed
                 if a re-run writes, TestADOFinalizeRerunOnlyReads fails: the second FinalizePhase sends zero PATCH
                 and zero POST to /_apis/wit/workitems/$*. WIQL POSTs to /_apis/wit/wiql are reads and allowed
                 if a refused task close is hidden, a task reading back In Progress after its close makes FinalizePhase
                 error naming that task while the phase item still closes

  t-18 Validate ADO boards in doctor
       files:    internal/cmd/doctor.go, internal/cmd/doctor_board_ado.go, internal/diag/combinations.go,
                 internal/cmd/doctor_board_ado_test.go
       desc:     The azure-devops arm of the board section:
                   - names each missing base_url / project / auth_env
                   - checks base_url (forge.CheckADOBaseURL) and the host allowlist BEFORE touching $auth_env
                   - validates board.api_version and board.auth_scheme
                   - warns that private-token/basic have no effect, auth_user is ignored and milestone_mode is
                     ignored (Epics)
                   - with area_path/iteration_path set, checks them via the client and flags a missing one as an issue
                 The network call lives in forge; cmd imports no net/http (boundary_test).
       covers:   c-1, c-9
       depends:  t-1, t-6, t-14
       contract: if a missing field is unnamed, TestDoctorADOBoardNamesMissingFields fails: with base_url, project and
                 auth_env unset, doctor prints three ✗ lines naming each and exits non-zero
                 if the token is read first, TestDoctorADORefusedHostSkipsToken fails: base_url https://attacker.example
                 with $auth_env unset prints the host refusal and never "is not set in this shell" for [board].auth_env
                 if a URL form is rejected, TestDoctorADOBoardURLForms fails unless https://dev.azure.com/acme and
                 https://tfs.corp.example/tfs/DefaultCollection (remote.url on that host) both print
                 "✓ [board] is well-formed (azure-devops @ …)"; https://dev.azure.com/acme/Proj is ✗ naming the <org> form
                 if api_version/auth_scheme go unchecked, board.api_version "7.1&x=1" and board.auth_scheme "token" are
                 each ✗. auth_scheme private-token, a set auth_user and milestone_mode agile are ⚠ only, with the
                 exit unaffected
                 if a missing path passes, TestDoctorADOFlagsMissingPath fails:
                   - area_path `Fabrikam\Nope` answered 404 is ✗ naming the path and the project
                   - a 200 is ✓
                   - a 500 is a "could not check" note, not an issue
                   - with both paths unset, no classification-nodes request is sent
                 if other providers change, TestDoctorValidatesBoardBlock and
                 TestDoctorAcceptsEveryDispatchableBoardProvider pass unchanged

Wave 6 (depends t-8, t-14, t-16)
  t-19 Prove ADO board commands end to end
       files:    internal/cmd/ado_board_fake_test.go, internal/cmd/issue_ado_test.go,
                 internal/cmd/phase_complete_board_ado_test.go
       desc:     A stateful httptest Azure Boards fake (backlog config, types, states, WIQL by tag/type, batch read,
                 create, field and relation PATCH) behind an azure-devops [board] on a different host than [remote]
                 (allow_hosts). It drives the real cobra commands.
       covers:   c-1, c-4, c-5, c-7
       depends:  t-14, t-16
       contract: if a command skips the ADO client, TestEveryBoardCommandUsesAzureBoards fails unless each of issue pull,
                 issue phase sync, issue task sync, issue milestone sync, issue backlog sync and issue reap (plan)
                 sends ≥1 request under /<org>/<project>/_apis/ on the fake and none to the [remote] host
                 if pull leaks mirrors or misses types, TestIssuePullADOEnvelope fails:
                 `issue pull --labels bug,enhancement --json` must return {"issues":[7 (type Bug, untagged),
                 9 (tag enhancement)],"error":null}. Item 8 (type Bug, tagged dross) and board.json-linked items never
                 appear
                 if an outage reads as zero, with WIQL answered 503 the same command exits 0 with "issues":[] and a
                 non-null "error" naming HTTP 503
                 if "(closed)" is printed over an open item, TestIssueCloseADOReadBack fails. With state writes
                 refused, `issue phase sync x --status complete --close` and `issue milestone sync v1.8 --close` must
                 exit non-zero naming the work item id, and stdout must never contain "(closed)"
                 if complete leaves items open or re-writes, TestCompleteFinalizesADOBoard fails.
                 `dross phase complete x` must leave both task items and then the phase item in Done, with task
                 PATCHes ahead of the phase PATCH in the fake's log. A second run sends zero PATCH and zero POST to
                 /_apis/wit/workitems/$*, and leaves board.json byte-identical

  t-20 Run live Boards smoke and record it
       files:    internal/forge/testdata/ado_boards_live_smoke.toml
       desc:     After `make install` (r-01), drive five steps with the installed binary in pair-mode against
                 the real org (the /home/rivil/development/ado-scratch project):
                   - create: phase sync
                   - transition: --status in-progress
                   - parent-link: task sync
                   - close: --status complete --close
                   - pull: --labels bug --json against a hand-filed Bug
                 Fill each step's result and evidence, with read-backs from read-only REST GETs. A failing step
                 becomes a /dross-quick fix plus a re-run, never a mock-only pass.
       covers:   c-8
       depends:  t-8, t-14, t-16
       contract: if mock is reported as live, TestADOBoardsLiveRecordHonest (t-8) accepts posture live only with all
                 five steps passing and evidence on the real api_base host; any failed step leaves posture partial
                 if a leg is hollow, TestADOBoardsLiveRecordStepEvidence (t-8) rejects it unless the record shows the
                 created item's _workitems/edit URL and type, a changed transition read-back, a Completed-category close
                 read-back, the Hierarchy-Reverse relation naming the phase item, and a pull envelope with
                 "error":null listing the hand-filed Bug and no dross-tagged item
                 if the run never happens, the committed record stays mock-only and the honesty test rejects any
                 edit that flips it to live without run_at, api_version, api_base and process
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 (dispatch + doctor config) | t-1, t-2, t-3, t-14, t-18, t-19 |
| c-2 (Epic/requirement/Task, parent links, identity-tag re-run) | t-6, t-7, t-11, t-16 |
| c-3 (category state resolution, state_map, warn-and-skip) | t-6, t-12, t-15 |
| c-4 (close verified on read-back, no false "(closed)") | t-12, t-15, t-16, t-17, t-19 |
| c-5 (pull envelope, tag-or-type match, no mirrors, error ≠ zero) | t-11, t-19 |
| c-6 (Related link idempotent, undo exact prior state) | t-7, t-12, t-17 |
| c-7 (phase complete: tasks then phase; re-run only reads) | t-17, t-19 |
| c-8 (live smoke + honesty check) | t-4, t-8, t-20 |
| c-9 (area/iteration on create, defaults when unset, doctor flags) | t-1, t-3, t-6, t-11, t-18 |
| c-10 (ADO policy-protected base → chore PR, unprotected pushes) | t-5, t-9, t-10, t-13 |

All 10 criteria are covered. The deferred item d4c0a4f04bb53856 is absorbed by t-5, t-9, t-10 and t-13.

## Judgment calls

- **Forge-local ADO transport.** Chose a forge-local ADO transport (azureboards.go doRaw mirroring ship's adoRequest). Rejected extracting a shared ADO HTTP package, because that refactors ship's live-proven transport mid-milestone. The secretscan registry and redact rows pin the new one to the same screening instead.
- **Work item type from identity labels.** Chose to pick the type from the forge-owned identity prefixes already in IssueInput.Labels (dross/task: → Task, else the requirement type). Rejected a new IssueInput.Kind field, because it changes the shared struct and every fake for one backend. forge already owns IdentityTaskPrefix.
- **Parents through a new ParentLinker.** Chose a new optional forge.ParentLinker as the single parent path (phase→Epic, task→phase) and kept LinkIssues = Related for c-6. Rejected two alternatives: overloading LinkIssues, which would make backlog links hierarchical; and treating IssueInput.Milestone as the parent, which gives two link paths and no re-run repair.
- **ADO-private milestone tag.** Chose a dross/milestone:<version> tag private to the ADO client for Epic identity. Rejected registering it in forge.IdentityLabelPrefixes, because that drags reap classification for a new lane into scope. An Epic that is not linked surfaces as unclassifiable: reported, never closed.
- **shipped → Completed.** Chose shipped → Completed plus a lane exemption mirroring Jira's. Rejected Resolved, which collides with uat in TestStateMapsKeepTheLifecycleStatesDistinct, and InProgress, which shows a merged phase as in progress.
- **Closed means Completed only.** Chose State "closed" iff the category is Completed. Removed reads open/unresolved and is excluded from open lists. Rejected Removed = closed, because finalizeCard's `Resolved || State == closed` would then skip a Removed card as finished, which is a false c-4/c-7 claim.
- **Shared honesty check.** Chose to extract the honesty core into internal/liverecord, shared by the ship and Boards records. Rejected duplicating the validator in forge, because "the same honesty check" (c-8) is then asserted rather than enforced. Risk: one tiny non-binary package; its types carry no tags, so pathfence needs no ledger rows.
- **Host-before-token in doctor for ADO only.** Chose host-before-token ordering in the azure-devops arm of doctor's board section only. Rejected reordering for every provider, because it changes established doctor output for six providers outside this phase's criteria.
- **No network in the constructor.** Chose lazy, cached metadata (requirement type, types, states) with zero requests at construction. Rejected eager discovery, because doctor and every openBoard caller construct a client even for no-op paths.
- **Requirement type source.** Chose the requirement type from backlogconfiguration's requirementBacklog default (GA at 7.1), which honours the lock's "backlog configuration". Rejected processconfiguration (preview-only) and a hard-coded per-process name table.
- **Tag-or-type match on every ListIssues.** Chose to apply tag-OR-type matching to every ListIssues label, since identity labels never name a type. Rejected a new IssueFilter flag scoped to pull, because it adds surface and the lock states the rule per label.
- **Policy scope matched in Go.** Chose to list policy configurations and match Exact/Prefix/all-repos scope in Go. Rejected server-side refName filtering, because its prefix semantics can't be pinned by a mock.
- **ADO auto-complete arms "merge" only.** Chose an AutoMergePR azure-devops arm that accepts only "merge" (→ noFastForward). Rejected mapping squash/rebase, because chore PRs are the only caller and the lock fixes merge commits.
- **milestone_mode is ignored.** Chose MilestoneModesFor("azure-devops") = nil, with doctor warning when milestone_mode is set. Rejected an epic-only Set, because the lock says milestones are always Epics, so the field is never read.
- **Separate proof tasks.** Chose separate proof tasks t-17 (boardsync) and t-19 (cmd, with a stateful fake). Rejected folding the proofs into the wiring tasks: each would exceed 5 files, and c-5/c-7 name commands (`issue pull`, `phase complete`) whose wiring only an end-to-end test proves.
- **Two-step c-10 wiring.** Chose to land the c-10 cmd routing as a provider dispatch (t-10) first and the milestone path plus the ship→complete end-to-end twin (t-13) after it. Rejected one task, because it spans 7 files. The tests stub ADOBranchProtectionFunc, so t-10 doesn't wait on t-9.
- **Minimal live-smoke dependencies.** t-20 depends only on the record and the features its five steps exercise (t-8, t-14, t-16). Rejected depending on doctor/c-10, which the c-8 steps don't touch.
- **Description sent as-is.** Chose to send the body to System.Description unchanged. Rejected a Markdown→HTML conversion, which no criterion asks for; it is a candidate deferred item if the live run shows unreadable descriptions.
- **board.auth_scheme passthrough for all providers.** Chose to pass board.auth_scheme through boardsync.Config for every provider, so a GitLab board now honours it too. Rejected an ADO-only passthrough, because board_config keeps [board] self-contained and an ignored key that reads as configured is the failure doctor exists to catch.
