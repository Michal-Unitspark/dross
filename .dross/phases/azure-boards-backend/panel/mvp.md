Phase azure-boards-backend — 7 tasks across 4 waves (lens: mvp)

Wave 1
  t-1  Build the Azure Boards client core
       files:    internal/forge/forge.go, internal/forge/azureboards.go, internal/forge/azureboards_test.go, internal/configenum/configenum.go
       covers:   c-1, c-2, c-5, c-9
       depends:  —
       description:
         AzureBoardsClient behind BoardClient. NewAzureBoards names a missing base_url/project/auth_env, refuses a
         malformed api_version (default 7.1), and runs cfg.Hosts.Check BEFORE os.Getenv. It sends Basic with an empty
         user and the PAT, or Bearer when auth_scheme = bearer. One request helper does secretscan.ScanPayload before
         encoding and redact.Scrub on every body. It follows no redirect and treats a sign-in page as an error.
         Work-item writes use json-patch+json. CreateIssue makes a Task for a dross/task: tag; any other card gets the
         requirement type from work/backlogconfiguration (read once). It writes System.Tags from labels, AreaPath and
         IterationPath when configured, and a Hierarchy-Reverse to IssueInput.Milestone (the Epic id).
         UpdateIssue replaces the tags and adds the parent link only when it is missing. GetIssue fills
         WorkflowState = System.State, Resolved = Completed category (states cached per type) and State open/closed.
         ListIssues runs WIQL that matches each label as a tag OR a work-item type, with ' doubled, then batched GETs.
         EnsureMilestone finds or creates an Epic tagged dross + dross/milestone:<version>. CloseIssue moves the item
         to its type's first Completed-category state and verifies by reading it back. PathExists(kind, path) reads
         classificationnodes. forge.Config gains APIVersion/AreaPath/IterationPath, NewBoard dispatches
         azure-devops, and configenum.BoardProviders gains it in the same commit (enum_divergence guard).
       contract:
         - if the host check moves after the token read, TestAzureBoardsRefusedHostNeverReadsToken fails: base_url https://attacker.example with an UNSET auth_env returns hostallow.ErrRefused, never the "$X is not set" error
         - if a required field goes unnamed, TestNewAzureBoardsNamesMissingField fails: empty base_url, project and auth_env each error naming [board].base_url / [board].project / [board].auth_env; api_version "7.1&x=1" is refused before the token is read
         - if the org/collection base is mangled, TestAzureBoardsBaseShapes fails: base_url <srv>/acme, <srv>/tfs/DefaultCollection and <srv> each POST <base>/My%20Proj/_apis/wit/workitems/$Task with api-version=7.1 (6.0 when configured)
         - if the credential drifts, TestAzureBoardsAuthHeader fails: default sends Basic base64(":"+token); auth_scheme bearer sends "Bearer "+token
         - if a card lands as the wrong type, TestAzureBoardsCreateType fails: a dross/task:p/t-1 card creates $Task; a dross/phase:p card creates the backlogconfiguration default (Product Backlog Item on the Scrum fixture, Issue on Basic), with backlogconfiguration fetched once per client
         - if the phase loses its Epic, TestAzureBoardsCreateParentsUnderEpic fails: IssueInput.Milestone 42 adds exactly one /relations/- op, rel System.LinkTypes.Hierarchy-Reverse to .../workItems/42; UpdateIssue with Milestone 42 on an item already parented to 42 sends no relation op
         - if area/iteration leak or vanish, TestAzureBoardsCreateAreaIteration fails: with AreaPath "Proj\Team A" and IterationPath "Proj\Sprint 3" every create (Epic, requirement, Task) carries /fields/System.AreaPath and /fields/System.IterationPath; unset, neither op appears
         - if pull_match regresses, TestAzureBoardsListLabelMatchesTagOrType fails: Labels [bug, enhancement] give WIQL with ([System.Tags] CONTAINS 'bug' OR [System.WorkItemType] = 'bug') OR (the same for 'enhancement'); a label o'brien is sent as 'o''brien'
         - if an outage reads as empty, TestAzureBoardsListErrorsNotZero fails: a WIQL 503 and a 203 sign-in HTML page each return a non-nil error and a nil slice
         - if a duplicate Epic can be minted, TestAzureBoardsEnsureMilestoneIdempotent fails: EnsureMilestone("v1.8", …) against a server already holding an Epic tagged dross/milestone:v1.8 returns its id and sends zero POSTs to workitems/$Epic
         - if Resolved is inferred from the write, TestAzureBoardsGetIssueReadsCategory fails: an item in "Resolved" (category Resolved) reads Resolved=false/State open; one in "Closed" (Completed) reads Resolved=true, State closed, WorkflowState "Closed"
         - if screening is skipped, TestAzureBoardsScreensAndScrubs fails: a CreateIssue body carrying a PAT-shaped secret errors before any request reaches the server, and a 400 body echoing the token is scrubbed from the returned error
         - if the path probe lies, TestAzureBoardsPathExists fails: classificationnodes/Areas/Team%20A 200 → true, 404 → false with nil error, 500 → error
         - if dispatch is missed, NewBoard(Config{Provider: "azure-devops", …}) does not return *AzureBoardsClient and TestBoardDispatchMatchesBoardProviders (internal/cmd/enum_divergence_test.go) goes red

  t-2  Read ADO branch policies; arm chore auto-complete
       files:    internal/ship/rulesets.go, internal/ship/automerge.go, internal/ship/azuredevops_policy.go, internal/ship/azuredevops_policy_test.go
       covers:   c-10
       depends:  —
       description:
         BranchRules dispatches azure-devops to a GET-only read of the project's policy configurations. When an
         enabled, blocking, non-deleted configuration is scoped to this repo (or to all repos) and to
         refs/heads/<branch>, exactly or by prefix, it returns Known with one synthesized push-refusing rule, so
         protect.RequiresPR answers unchanged. Otherwise it returns Known with no rules. An unreadable answer gives
         Known=false with a reason. AutoMergePR dispatches azure-devops in two cases. If an enabled "Require a merge
         strategy" policy (fa4e907d-c16b-4a4c-9dfa-4916e5d171ab) on the base disallows no-fast-forward, it returns
         ErrAutoMergeUnavailable and writes nothing. Otherwise it PATCHes the PR with autoCompleteSetBy = the caller's
         connectionData id and completionOptions{mergeStrategy: noFastForward, bypassPolicy: false}, then reads the PR
         back. A refused PATCH is ErrAutoMergeUnavailable.
       contract:
         - if scope matching drifts, TestADOBranchRulesScope fails: an enabled blocking policy on exact refs/heads/main makes protect.RequiresPR true for main; prefix refs/heads/milestone/ is true for milestone/v1.8 and false for main; isEnabled false, isBlocking false, isDeleted true or another repositoryId each give Known with no rules
         - if an unread answer reads as unprotected, TestADOBranchRulesUnknown fails: 401, a 203 sign-in page and malformed JSON each give Known=false with a non-empty Reason
         - if dross ever writes a policy, TestADOBranchRulesReadOnly fails: the server records only GET requests during BranchRules
         - if auto-complete bypasses policy or squashes, TestADOAutoComplete fails unless the PATCH body carries autoCompleteSetBy.id = the connectionData user id, completionOptions.mergeStrategy = "noFastForward" and bypassPolicy never true
         - if a forbidden merge commit is armed, TestADOAutoCompleteMergeStrategyForbidden fails: a merge-strategy policy with allowNoFastForward=false returns ErrAutoMergeUnavailable and sends no PATCH; a 400 on the PATCH also returns ErrAutoMergeUnavailable
         - if the outcome is guessed, TestADOAutoCompleteReadBack fails: read back completed → Merged; active with autoCompleteSetBy → AutoEnabled; active without it → error

Wave 2 (t-3, t-4 depend t-1; t-5 depends t-2)
  t-3  Carry [board] ADO config; doctor validates it
       files:    internal/project/project.go, internal/boardsync/ctx.go, internal/cmd/doctor.go, internal/cmd/doctor_test.go, internal/cmd/friction_window_test.go
       covers:   c-1, c-9
       depends:  t-1
       description:
         project.Board gains auth_scheme, api_version (project.APIVersion), area_path and iteration_path, and
         boardsync.Config maps them into forge.Config. For azure-devops, doctor's [board] block names each missing
         base_url/project/auth_env. It flags (✗) a malformed api_version and an auth_scheme outside AuthSchemes, and
         warns that private-token/basic have no effect. It runs hostallow (remote url + local allow_hosts) on base_url
         BEFORE the $auth_env lookup and skips that lookup when the host is refused. It accepts Services and Server URLs.
         When the block is valid and area_path/iteration_path are set, doctor builds the client and flags a path that
         PathExists reports absent. An unreachable board is only advisory. The doctor tests driven by BoardProviders
         get an ADO-reachable base_url for azure-devops.
       contract:
         - if a missing field goes unnamed, TestDoctorADOBoardNamesMissingFields fails: dropping base_url, project and auth_env each prints a ✗ line naming exactly that key
         - if the token is consulted for a refused host, TestDoctorADOBoardRefusedHostSkipsToken fails: base_url https://attacker.example prints a ✗ naming the refused host and never the "$X is not set" line, with $X unset
         - if Server URLs are refused, TestDoctorADOBoardAcceptsServicesAndServer fails: https://dev.azure.com/acme, https://acme.visualstudio.com and https://tfs.corp.example/tfs/DefaultCollection (host in local allow_hosts) each print "✓ [board] is well-formed"
         - if api_version/auth_scheme go unchecked, TestDoctorADOBoardAPIVersionAuthScheme fails: api_version "7.1&x" and auth_scheme "oauth" are each a ✗; bearer and empty pass
         - if a missing path passes, TestDoctorADOPathProbe fails: area_path "Proj\Nope" answered 404 by the classification-node stub prints a ✗ naming [board].area_path; an existing iteration_path prints none; the probe request carries api-version=6.0 from [board].api_version (proves boardsync.Config maps it)
         - if the BoardProviders loop regresses, TestDoctorAcceptsEveryDispatchableBoardProvider and TestFrictionWindow_DoctorAcceptsWorkingProviders go red for azure-devops

  t-4  Map statuses to ADO states; verify closes, links
       files:    internal/forge/azureboards_state.go, internal/forge/azureboards_state_test.go, internal/forge/state_map_test.go, internal/boardsync/board_lifecycle_divergence_test.go
       covers:   c-2, c-3, c-4, c-6
       depends:  t-1
       description:
         defaultAzureBoardsCategoryMap (map[string]string): planned→Proposed; in-progress, task-in-progress→InProgress;
         uat, task-in-review→Resolved (InProgress where the type has no Resolved state); shipped, complete,
         task-complete→Completed. SetState takes a [board].state_map literal first, else the type's first state in
         that category, and PATCHes System.State. If no state is reachable, or an override names a state the type
         lacks, it warns on stderr, writes nothing and returns nil. CloseIssueAs resolves the same way, PATCHes,
         re-GETs, and errors naming the id unless the state read back is Completed-category. SetStateRaw writes a
         literal and verifies it on read-back (StateWriter). LinkIssues adds System.LinkTypes.Related and LinkParent
         adds Hierarchy-Reverse on the child. Both read relations first, so linking again is a no-op. The map
         is registered in both lifecycle guards.
       contract:
         - if per-process resolution breaks, TestAzureBoardsDefaultStatesPerProcess fails: Scrum PBI in-progress → Committed and Scrum Task task-in-progress → In Progress; Agile User Story uat → Resolved but Agile Task task-in-review → Active; Basic Issue uat → Doing; complete → Done / Closed / Done per process
         - if overrides are ignored, TestAzureBoardsStateMapOverride fails: state_map in-progress = "Approved" on a Scrum PBI PATCHes System.State "Approved"
         - if an unreachable status fails the sync, TestAzureBoardsUnreachableStateWarnsAndSkips fails: an override naming a state the type lacks, and planned on a type with no Proposed state, each return nil, print a warning naming the status and the id, and send no PATCH
         - if a close is trusted on the write, TestAzureBoardsCloseVerifiesCompleted fails: override complete = "Resolved" on Agile User Story 7 PATCHes, reads back "Resolved" and errors naming 7; a server that ignores the PATCH also errors
         - if undo is lossy, TestAzureBoardsSetStateRawVerifies fails: SetStateRaw(7, "Resolved") PATCHes that literal and errors naming 7 when the read-back says "Active"; var _ StateWriter = (*AzureBoardsClient)(nil) compiles
         - if links duplicate or use the wrong type, TestAzureBoardsLinks fails: LinkIssues(5, 9) adds one System.LinkTypes.Related to .../workItems/9; LinkParent(3, 8) adds one Hierarchy-Reverse on 8 to 3; a second call of each sends no PATCH
         - if the map drifts from the lifecycle vocabulary, TestStateMapsKeyExactlyTheEmittedStatuses and TestStateMapsKeepTheLifecycleStatesDistinct fail for azure-devops (uat and shipped must not share a category)

  t-5  Route ADO .dross chores through a chore PR
       files:    internal/cmd/basebranch.go, internal/cmd/chorepr.go, internal/cmd/basebranch_chores_test.go
       covers:   c-10
       depends:  t-2
       description:
         routeBaseChores asks BranchRules on azure-devops exactly as on github. That covers phase complete,
         finalizeBoard, and ship's and ship-recover's safety nets, because all of them route through it. On ADO, the
         unknown-answer error names the PAT/[remote].auth_env fix instead of `gh auth login`. chorePR records the provider.
         On azure-devops the narration says auto-complete is armed, or that the PR must be completed by hand. pullURL
         gives <remote>/pullrequest/<n> for an Azure Repos url.
       contract:
         - if ADO still pushes to a protected base, TestADOProtectedBaseRoutesThroughChorePR fails: on an azure-devops remote with BranchRules stubbed protected, routeBaseChores opens one chore PR dross-chores/main → main armed with method "merge", and the pre-receive log shows no refs/heads/main update
         - if the milestone base is missed, the same test on milestone/v1.8 opens dross-chores/milestone-v1.8 → milestone/v1.8 and the log shows no refs/heads/milestone/ update
         - if unprotected ADO stops pushing, TestADOUnprotectedBasePushesDirectly fails: Known with no rules pushes main straight to origin and opens no PR
         - if an unread policy answer pushes, TestADOUnknownProbeNeverPushes fails: Known=false errors naming the reason and the auth_env/PAT fix (not gh auth login) and the hook sees no update
         - if "complete it by hand" is lost, TestADOChoreNarration fails: ErrAutoMergeUnavailable narrates "complete it by hand" with the PR URL; an armed PR narrates auto-complete; pullURL on https://dev.azure.com/o/p/_git/r gives .../_git/r/pullrequest/41

Wave 3 (depends t-3, t-4)
  t-6  Wire ADO into board sync, close, finalize
       files:    internal/boardsync/milestone.go, internal/boardsync/phase.go, internal/boardsync/task.go, internal/boardsync/azureboards_sync_test.go
       covers:   c-2, c-3, c-4, c-5, c-6, c-7, c-9
       depends:  t-3, t-4
       description:
         Add an *forge.AzureBoardsClient case next to Jira's: SyncPhase and setBoardState drive it through SetState,
         and CloseIssue closes it through CloseIssueAs, which verifies the close. SyncTasks links each task card to the
         phase card with LinkParent (Hierarchy), because LinkIssues is Related on ADO. CheckMilestoneClosable accepts
         an ADO Epic. The new test file adds a stateful httptest ADO fake: Agile/Scrum/Basic states,
         backlogconfiguration, WIQL on tags/type, json-patch, relations and a request log. It builds Ctx through
         boardsync.Config + forge.NewBoard.
       contract:
         - if hierarchy or dedupe breaks, TestADOSyncBuildsHierarchy fails: milestone+phase+task sync on Scrum creates one Epic, one PBI and N Tasks; the PBI has a Hierarchy-Reverse to the Epic, each Task one to the PBI and no Related; a re-run with board.json deleted creates nothing, adds no relation and PATCHes the same ids
         - if the state never reaches the item, TestADOSyncMovesState fails: phase sync --status in-progress leaves the Scrum PBI "Committed" and task sync --status task-in-progress leaves its Tasks "In Progress"; state_map in-progress = "Approved" leaves the PBI "Approved"; an unreachable status prints a warning and the sync returns nil
         - if a refused close is reported, TestADOCloseRefusedNamesItem fails: with state_map complete/task-complete = "Resolved" on Agile, phase sync --close, task close and FinalizePhase each return an error naming the work item id, and the output never shows "(closed)" for it
         - if pull leaks mirrors or hides outages, TestADOCollectInbound fails: labels bug,enhancement return a native Bug and an enhancement-tagged User Story but not a dross-tagged Task; a WIQL 503 makes CollectInbound return an error, and EmitPullEnvelope prints issues [] with a non-null error
         - if backlog links duplicate or undo is lossy, TestADOBacklogRelatedAndReapUndo fails: a routed deferred item is Related to its target PBI exactly once across two backlog syncs; reap apply then undo returns a card from Closed to exactly its prior "Resolved", and the undo errors naming the id when the fake refuses the write
         - if finalize writes on a re-run, TestADOFinalizeTwiceReadsOnly fails: the first FinalizePhase moves every Task to a Completed state before the PBI's PATCH; the second sends no PATCH and no POST other than /_apis/wit/wiql
         - if area/iteration are dropped in sync, TestADOSyncCarriesAreaIteration fails: with [board].area_path/iteration_path set, the Epic, PBI and Tasks are created with those values; unset, the fake records no AreaPath/IterationPath op

Wave 4 (depends t-3, t-6)
  t-7  Run and record the live Azure Boards smoke
       files:    internal/forge/testdata/ado_boards_live_smoke.toml, internal/forge/ado_boards_live_record_test.go
       covers:   c-8
       depends:  t-3, t-6
       description:
         The record's TOML comments are the runbook. Prerequisites: a scratch ADO project, [board] provider =
         azure-devops with a PAT in auth_env, and a Bug filed by hand. Steps: create, transition, close, parent-link,
         pull. Posture is mock-only | partial | live. A test-local validator mirrors the honesty check in
         internal/ship/ado_live_record_test.go. Then the steps run live from /home/rivil/development/ado-scratch, and
         run_at, base_url, api_version and each step's evidence are filled in.
       contract:
         - if mock passes as live, TestADOBoardsLiveRecordHonest fails when posture = "live" while any step is not-run or fail, or the record lacks run_at, base_url or api_version
         - if httptest evidence is passed off as live, any step whose evidence names 127.0.0.1, localhost or an httptest port is rejected
         - if a step is dropped, the test fails unless create, transition, close, parent-link and pull each appear exactly once
         - if a leg is hollow, TestADOBoardsLiveRecordStepEvidence rejects a pass lacking: create = Epic, requirement and Task ids on the real host; transition = the state name read back; close = a Completed-category state read back; parent-link = a child id with Hierarchy-Reverse to its parent id; pull = the hand-filed Bug's id listed and no dross-tagged id
         - if the validator is vacuous, in-test fixtures live-with-gap, mock-only and complete-live give reject, accept-as-mock and accept-as-live
         - if a failed run reads as live, any step "fail" forces posture "partial"; "partial" with every step passing is rejected

## Coverage

- c-1  → t-1 (constructor, host-before-token, NewBoard + BoardProviders), t-3 (config mapping, doctor)
- c-2  → t-1 (typed create, tag identity, Epic, phase→Epic parent), t-4 (LinkParent), t-6 (sync hierarchy + re-run dedupe)
- c-3  → t-4 (category resolution per process, override, warn-skip), t-6 (sync drives it)
- c-4  → t-4 (CloseIssueAs read-back), t-6 (phase --close / task close / finalize error, no "(closed)")
- c-5  → t-1 (WIQL tag-or-type, error not zero), t-6 (CollectInbound + envelope)
- c-6  → t-4 (Related link, SetStateRaw), t-6 (backlog link once, reap undo)
- c-7  → t-6 (finalize order, second run reads only)
- c-8  → t-7
- c-9  → t-1 (area/iteration on create, PathExists), t-3 (doctor flags missing path), t-6 (sync carries them)
- c-10 → t-2 (ADO policy read, auto-complete), t-5 (routeBaseChores gate, narration)

10/10 criteria covered.

## Judgment calls

- The phase→Epic parent rides IssueInput.Milestone: Epic ids are numeric, so SyncPhase, finalize and backlog already pass it. I rejected a new Parent field on IssueInput: this link needs no boardsync change.
- The task→phase link is a concrete *AzureBoardsClient.LinkParent call in task.go. I rejected a new optional ParentLinker interface, because boardsync already type-switches per tracker and one implementer does not earn an interface. LinkIssues stays Related for c-6.
- The type is picked inside CreateIssue from the identity tag (dross/task: → Task, else the requirement type). I rejected widening IssueInput with a Type field that every backend would ignore.
- The milestone Epic uses the generic EnsureMilestone(version, body) path (titled by version, tagged dross/milestone:<version>). I rejected an ADO arm in EnsureMilestoneLink for a prettier title, because no criterion needs it.
- shipped → Completed category, as Jira's "Done", so uat (Resolved) and shipped stay distinct under the existing distinctness guard. I rejected shipped → Resolved, which collides with uat.
- The ADO board transport is written inside forge (json-patch content type, [board] config). I rejected extracting ship's adoConn into a shared package: it is a refactor no criterion asks for.
- c-10 is fixed only at routeBaseChores, which covers phase complete, finalizeBoard, ship and ship-recover. routeProtectedMilestone and waitForMilestoneChores (milestone complete) stay GitHub-only. c-10 names only phase complete and ship's safety net, although the deferred item mentions milestone.go.
- ADO BranchRules returns a synthesized push-refusing LiveRule, so protect.RequiresPR and every caller work unchanged. I rejected a parallel ADO-only protection API.
- The live-smoke template, validator and run are one final task. I rejected a wave-1 template plus a separate run task: one task fewer, and the validator only matters once the run exists.
- c-5, c-6 and c-7 are proved at boardsync level against one stateful httptest fake, folded into t-6. I rejected separate cmd-level pull tests: the pull/envelope path is provider-agnostic and already tested.
- t-3 is 5 files (two are test fixtures). The config mapping and doctor stay together because doctor's path probe needs that mapping. Splitting them would add a wave.
- doctor's host-before-token check is scoped to azure-devops. I rejected applying it to every board provider, which would change behaviour outside this phase.
- configenum.BoardProviders lands in t-1 with the NewBoard case, not in the config task. Otherwise TestBoardDispatchMatchesBoardProviders is red between two wave-1 commits.
- Left out because no criterion asks: `dross project set` / options.md / README wiring for the new [board] keys (hand-edited TOML works), ADO task pull (ProviderHasWorkflowState), and a shared forge/ship ADO transport.
