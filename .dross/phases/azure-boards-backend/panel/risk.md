# azure-boards-backend — risk-lens draft

Lens: every task owns a named set of failure modes (edge case, concurrency, bad input, partial failure, security, regression) and the test that catches each one. Waves are file-disjoint. No two tasks in the same wave edit the same file.

```
Phase azure-boards-backend — 17 tasks across 7 waves

Wave 1
  t-1  Add ADO fields to the board config
       files:    internal/project/project.go, internal/project/board_test.go, internal/cmd/project.go,
                 internal/cmd/project_test.go, assets/prompts/options.md
       covers:   c-1, c-9
       depends:  —
       desc:     Board gains api_version (project.APIVersion, so a bare TOML number decodes), auth_scheme, area_path and
                 iteration_path, each with toml and json tags. Adds `dross project get/set/unset` arms for all four, with
                 auth_scheme checked against configenum.AuthSchemes. Adds options.md bullets and puts azure-devops in the
                 board.provider list. Nothing is inherited from [remote]. options.md takes effect only after make install (r-01).
       contract: if a hand-written `api_version = 6.0` under [board] breaks Load, TestBoardAPIVersionDecodesBareNumber
                 fails: 6.0 → "6.0", 7 → "7.0", "7.1-preview.1" kept verbatim
                 if [board] inherits from [remote], TestBoardDoesNotInheritRemote fails: [remote] api_version "6.0" and
                 auth_scheme "bearer" with an empty [board] leave Board.APIVersion and Board.AuthScheme ""
                 if a new field is unaddressable, TestBoardFieldsAllAddressable fails; `project set board.auth_scheme nonsense`
                 exits non-zero naming private-token | bearer | basic and leaves project.toml byte-identical
                 if a backslash path is mangled, TestBoardAreaPathRoundTrips fails: set board.area_path 'Scratch project\Team A',
                 then get returns the same string and a Save/Load cycle reproduces it exactly
                 if the docs drift, TestOptionsDocumentsTheADOBoardFields fails unless options.md has `board.api_version`
                 (naming 7.1), `board.auth_scheme` (empty-user PAT / bearer), `board.area_path` and `board.iteration_path`
                 bullets, and lists azure-devops under board.provider

  t-2  Build the Azure Boards transport and constructor
       files:    internal/forge/azureboards.go, internal/forge/azureboards_test.go, internal/forge/forge.go,
                 internal/forge/hostallow_test.go, internal/secretscan/sinks.go
       covers:   c-1
       depends:  —
       desc:     Adds AzureBoardsClient and NewAzureBoards. The constructor checks, in order: base_url, project, auth_env, the
                 api_version shape (exported ADOAPIVersionOK, default 7.1), then hostallow, and only then calls Getenv.
                 doRaw runs ScanPayload before encoding, sets api-version on the query, sends Basic ":"+PAT or Bearer, sets
                 X-TFS-FedAuthRedirect: Suppress, never follows a redirect, treats 203/3xx as a sign-in error and Scrubs every
                 body. Writes go as application/json-patch+json. forge.Config gains APIVersion, AreaPath and IterationPath.
                 doRaw is registered as Screened in secretscan.Transports.
       contract: if a refused host reads the token, TestAzureBoardsRefusesOffAllowlistHost fails: base_url
                 https://attacker.example with the sentinel env set returns hostallow.ErrRefused naming the host, no client,
                 sentinel absent from the error
                 if a config hole reaches the network, a missing base_url, project or auth_env each errors naming
                 [board].<key> while the env is unset (a config error, not "$X is not set"); api_version "7.1&$top=1",
                 "7.1#x" and "../7.1" error naming [board].api_version before any Getenv
                 if the credential shape drifts, TestAzureBoardsAuthHeader fails: default, basic and private-token send Basic
                 base64(":"+PAT) even with AuthUser set; bearer sends "Bearer <PAT>"
                 if a redirect carries the token, TestAzureBoardsNeverFollowsRedirect fails: a 302 to a second httptest server
                 gets 0 requests there and returns a sign-in error; a 203 errors naming $AUTH_ENV; every request carries
                 X-TFS-FedAuthRedirect: Suppress
                 if a body leaks the secret, a 400 echoing the PAT and base64(":"+PAT) yields an error containing neither
                 if URLs double-escape, project "Scratch project" builds /o/Scratch%20project/_apis/... (never %2520); Server
                 base https://tfs.corp.example/tfs/DefaultCollection keeps its collection path; an endpoint already carrying
                 api-version=5.0 is sent with exactly one api-version (the configured one)
                 if the transport escapes the screen, TestEveryOutboundSeamIsRegistered and
                 TestScreenPrecedesRequestInEveryScreenedTransport (internal/cmd) fail unless forge.AzureBoardsClient.doRaw
                 is Screened(ScanPayload) and its scan comes before http.NewRequest

  t-3  Add the Boards live-smoke record and validator
       files:    internal/forge/testdata/ado_boards_live_smoke.toml, internal/forge/ado_boards_live_record_test.go
       covers:   c-8
       depends:  —
       desc:     A record template whose TOML comments are the runbook: make install, [board] on a scratch ADO project,
                 the process recorded, and a Bug filed by hand before the pull step. It has five steps (create, transition,
                 close, parent, pull), all not-run, with posture mock-only. The validator is test-local and mirrors
                 internal/ship/ado_live_record_test.go.
       contract: if mock can be filed as live, TestADOBoardsLiveRecordHonest fails when posture = "live" while any step is
                 not-run or fail, or the record lacks run_at, api_version or process
                 if httptest evidence passes as live, a step naming localhost, 127.x or [::1] is rejected; a URL counts only on
                 api_base's host and in _workitems/edit/<id> form
                 if a step is dropped or duplicated, the test fails unless create, transition, close, parent and pull each
                 appear exactly once
                 if a hollow leg passes, TestADOBoardsLiveRecordStepEvidence rejects: create without three distinct ids typed
                 Epic / requirement / Task; transition without the task's state and category InProgress; close with any
                 category but Completed (Resolved and Removed rejected); parent unless the ids chain task → phase → Epic; pull
                 unless error is null, a non-dross Bug id is listed and none of the three dross ids is
                 if the validator is vacuous, in-test fixtures live-with-gap / mock-only / complete-live give reject /
                 accept-as-mock / accept-as-live, and "partial" with every step passing is rejected

  t-4  Read ADO branch policies as base protection
       files:    internal/ship/azuredevops_policy.go, internal/ship/azuredevops_policy_test.go, internal/ship/protection.go,
                 internal/ship/protection_test.go
       covers:   c-10
       depends:  —
       desc:     adoBranchProtection GETs the repo (id, defaultBranch) and one $top page of _apis/policy/configurations. A
                 branch is protected iff a non-deleted, enabled, blocking config has a scope whose repositoryId is null or
                 this repo, and whose refName is an exact or prefix match (case-insensitive) or whose matchKind is
                 DefaultBranch on the default branch. adoMergeCommitAllowed reads merge-strategy policies. ship.BaseProtection
                 (with a BaseProtectionFunc seam) dispatches github through BranchRulesFunc + protect.RequiresPR, and
                 azure-devops to the reader above. Every other provider gets Supported=false. Read-only.
       contract: if scope matching is loose, TestADOBranchProtection fails: exact refs/heads/main, enabled and blocking →
                 protected; isBlocking false, isEnabled false or isDeleted true → not protected; prefix refs/heads/milestone/
                 protects milestone/v1.8 but not main; matchKind DefaultBranch protects main (the repo's defaultBranch) but
                 not milestone/v1.8; refName refs/heads/Main protects main
                 if another repo's or a repo-wide policy counts, a scope on a different repositoryId leaves main unprotected,
                 and so does a file-size policy scoped by repositoryId with no refName; repositoryId null applies
                 if unreadable reads as unprotected, a 500, a 203 sign-in, non-JSON, a body without value, and a full $top
                 page each give Known=false with a reason, never Known=true with RequiresPR=false
                 if merge-commit policy is misread, a blocking merge-strategy policy (fa4e907d-…) with allowNoFastForward
                 false on main gives allowed=false; allowNoFastForward true, or no such policy, gives true; an unreadable
                 list gives known=false
                 if GitHub routing regresses, TestBaseProtectionGitHubUsesBranchRulesSeam fails unless a stubbed
                 BranchRulesFunc with a pull_request rule yields RequiresPR; provider forgejo gives Supported=false with
                 0 requests
                 if dross writes a policy, the test fails when any request recorded across these tests is not a GET

  t-5  Build a stateful Azure Boards test fake
       files:    internal/boardsync/ado_fake_test.go
       covers:   c-2, c-7
       depends:  —
       desc:     An in-memory ADO server for boardsync tests, built from ADO's documented behaviour rather than from dross's
                 client. It models work items (rev, fields, relations), json-patch create and update, WIQL ([System.TeamProject],
                 Tags CONTAINS, WorkItemType =), workitemsbatch, types, states and backlogconfiguration for Agile, Scrum and
                 Basic, and classificationnodes. It logs every call, with writes() = PATCH + POST .../workitems/$Type, and
                 supports per-path fault injection. It is self-tested.
       contract: if the fake accepts what ADO rejects, TestADOFakeRejects fails unless each of these is refused: a second
                 Hierarchy-Reverse on a child → 400 TF201036, item unchanged; a duplicate Related → 400; a PATCH with a stale
                 test /rev → non-2xx and no field change; a create or update without application/json-patch+json → 415; a
                 GET with both $expand and fields → 400; a workitemsbatch with 201 ids → 400; a WIQL without
                 [System.TeamProject] = @project → 400; a System.State not in the type's states → 400
                 if tag matching drifts, TestADOFakeWIQLTags fails unless CONTAINS 'dross/task:p/t-1' matches that tag as a
                 whole tag, case-insensitively, and does not match dross/task:p/t-10
                 if reads count as writes, TestADOFakeWritesExcludeReads fails unless POST wiql and POST workitemsbatch are
                 absent from writes() while PATCH and POST workitems/$Task are present
                 if a fixture asserts nothing, an unscripted path, or a fault injected on a route nobody hit, fails the test at
                 cleanup (as strictTracker does)

Wave 2
  t-6  Resolve ADO process metadata and state categories
       files:    internal/forge/azureboards_meta.go, internal/forge/azureboards_meta_test.go,
                 internal/boardsync/board_lifecycle_divergence_test.go
       covers:   c-2, c-3, c-9
       depends:  t-2
       desc:     Adds requirementType() (backlogconfiguration requirementBacklog.defaultWorkItemType), workItemTypes() and
                 typeStates(type), all cached per client. defaultADOCategoryMap is a map[string]string over
                 LifecycleStatuses: planned→Proposed; in-progress and task-in-progress→InProgress; shipped, uat and
                 task-in-review→Resolved (InProgress if the type has no Resolved); complete and task-complete→Completed.
                 resolveADOState applies a flat state_map override first, else takes the first state of the category in the
                 type's order, and never picks Removed. Also adds PathExists(areas|iterations, path) and an azure-devops
                 entry in the divergence guard tables.
       contract: if c-3's example breaks, TestADOResolveState fails: Scrum in-progress → PBI "Committed" and Task
                 "In Progress"; Scrum planned → PBI "New" (not "Approved"); Agile task-in-review → User Story "Resolved" but
                 Task "Active"; Basic uat → Issue "Doing"; complete → Agile "Closed", Scrum and Basic "Done"
                 if a default lands in Removed, a table over Agile/Scrum/Basic × every LifecycleStatuses member never resolves
                 to a Removed-category state
                 if an override is trusted blindly, state_map {"complete": "Ready"} on a Task that has no "Ready" returns !ok
                 with a reason naming "Ready" and "Task"; {"uat": ""} returns !ok; {"in-progress": "Doing"} on Basic beats
                 the category default
                 if the requirement type is guessed, a backlogconfiguration default of "Product Backlog Item" gives PBI, a
                 custom "Customer Request" gives that name, and a body without requirementBacklog errors naming backlog
                 configuration (never "User Story")
                 if metadata is refetched or misaddressed, a second typeStates("Product Backlog Item") makes 0 requests and
                 the first requests .../workitemtypes/Product%20Backlog%20Item/states (escaped once)
                 if path lookup misreads, PathExists(areas, `Scratch project\Team A`) and PathExists(areas, `Team A`) both GET
                 .../classificationnodes/Areas/Team%20A; the bare project name GETs .../Areas; 404 → (false, nil); 500 or
                 203 → error, never (false, nil)
                 if the category map drifts from the lifecycle set, TestStateMapsKeyExactlyTheEmittedStatuses/azure-devops
                 fails when a LifecycleStatuses key is dropped from defaultADOCategoryMap; TestEveryReapTerminalIsMapped
                 and TestLaneTerminalStatesAreDistinctWithinTheirLane cover azure-devops too

  t-7  Arm ADO chore-PR auto-complete without bypass
       files:    internal/ship/automerge.go, internal/ship/azuredevops_autocomplete.go,
                 internal/ship/azuredevops_autocomplete_test.go
       covers:   c-10
       depends:  t-4
       desc:     AutoMergePR gains an azure-devops arm (method merge only). It GETs the PR's target, checks
                 adoMergeCommitAllowed, reads the caller's id from connectionData, then PATCHes pullRequests/<n> with
                 autoCompleteSetBy and completionOptions {mergeStrategy noFastForward, bypassPolicy false}. Read-back:
                 autoCompleteSetBy present → AutoEnabled; status completed → Merged. A forbidding or unknown policy, a refused
                 PATCH, or a missing read-back each give ErrAutoMergeUnavailable.
       contract: if policy is bypassed, TestADOArmAutoComplete fails unless the PATCH body has mergeStrategy
                 "noFastForward", bypassPolicy false (present), and autoCompleteSetBy.id equal to the connectionData id, and
                 no recorded body anywhere carries bypassPolicy true
                 if a forbidden merge commit is armed anyway, a blocking merge-strategy policy with allowNoFastForward false
                 on the target, or an unreadable policy list, returns ErrAutoMergeUnavailable with 0 PATCH requests
                 if a refusal reads as armed, a PATCH 403 and a 200 whose body lacks autoCompleteSetBy each return
                 ErrAutoMergeUnavailable, never AutoEnabled
                 if completion state is misread, a PATCH answer with status completed gives Merged true
                 if squash leaks into chores, method "squash" on azure-devops errors with 0 requests; provider github still
                 takes the gh path (existing automerge tests unchanged)

Wave 3
  t-8  Query and read work items via WIQL
       files:    internal/forge/azureboards_query.go, internal/forge/azureboards_query_test.go
       covers:   c-2, c-4, c-5
       depends:  t-6
       desc:     GetIssue does GET workitems/<id>?$expand=relations with no fields param. ListIssues builds a WIQL scoped by
                 [System.TeamProject] = @project with labels OR'd: a non-dross label matches its tag or a same-named type the
                 project has (case-insensitive); dross labels match tags only. ' is doubled. Ids come back through
                 workitemsbatch, ≤200 per call, errorPolicy Omit. A post-filter checks the exact tag or type and the state
                 category (open drops Completed and Removed). toIssue sets Key, the browser URL, Labels with dross tags
                 lowercased, WorkflowState = System.State, and State closed / Resolved only for Completed.
       contract: if WIQL can be injected, TestADOWIQLEscapes fails: label o'brien appears as 'o''brien', and
                 "x' OR [System.Id] > '0" stays one literal
                 if a pull leaks across projects, every WIQL carries [System.TeamProject] = @project and is POSTed to
                 /<org>/<project>/_apis/wit/wiql, never the org root
                 if pull_match breaks, TestADOListTypeOrTag fails: on Agile, labels bug,enhancement return an untagged Bug, a
                 PBI tagged Enhancement and a Task tagged BUG; on Basic (no Bug type) the WIQL has no WorkItemType clause and
                 only tags match; labels "dross" and dross/phase:x never produce a WorkItemType clause
                 if CONTAINS is trusted, an id the WIQL returns for dross/task:p/t-10 is dropped from a dross/task:p/t-1
                 lookup
                 if a partial list escapes, 450 ids make batch calls of 200/200/50 and a 500 on the second returns (nil, err);
                 a 203 or non-JSON WIQL answer returns (nil, err), never ([], nil); a null batch entry (deleted mid-query)
                 is skipped
                 if mirrors leak into the inbox, tags "Dross; DROSS/Phase:p1; Bug" read back as Labels
                 [dross dross/phase:p1 Bug], so HasMarker is true
                 if done-ness is misread, TestADOToIssueState fails: Agile "Resolved" → State open, Resolved false,
                 WorkflowState "Resolved"; "Closed" → closed and Resolved; "Removed" → open, Resolved false, and absent from
                 an open ListIssues; Resolved-category items stay in open
                 if stale keys hit the wire, GetIssue("PROJ-7") errors with 0 requests, a 404 errors (never nil, nil), and the
                 request carries $expand=relations without fields; URL is
                 https://dev.azure.com/o/Scratch%20project/_workitems/edit/42

  t-9  Create and update work items
       files:    internal/forge/azureboards_items.go, internal/forge/azureboards_items_test.go,
                 internal/forge/secretscan_test.go
       covers:   c-2, c-9
       depends:  t-6
       desc:     CreateIssue uses type Task when a dross/task: label is present, else the requirement type. It POSTs
                 workitems/$<type> as json-patch with Title (≤255 runes), an HTML-escaped Description (newlines → <br>),
                 Tags joined by '; ', AreaPath/IterationPath when configured, and a Hierarchy-Reverse op to in.Milestone
                 when > 0. UpdateIssue PATCHes title, description and tags (full replace), never area/iteration; it refuses
                 a State patch and ignores Milestone (parenting belongs to t-12's LinkParent). One createWorkItem helper
                 serves every type.
       contract: if the type is wrong, TestADOCreateType fails: task labels POST .../workitems/$Task; phase labels on Scrum
                 POST .../workitems/$Product%20Backlog%20Item (escaped once) and on Basic $Issue; dross/quick → requirement
                 type
                 if the wire shape drifts, creates and updates carry Content-Type application/json-patch+json and a JSON
                 array of ops
                 if bad input reaches ADO, a 300-rune title (multi-byte at the cut) is sent as exactly 255 runes;
                 "a < b\n- x & y" is sent as "a &lt; b<br>- x &amp; y"; a label containing ';' errors naming it with
                 0 requests
                 if c-9 breaks, TestADOCreateAreaIteration fails: configured area/iteration ride every create through
                 createWorkItem (a table over Epic, PBI and Task); unset sends no AreaPath/IterationPath op (no empty
                 string); UpdateIssue never carries either op
                 if the parent is dropped on create, Milestone 42 adds one relations op with rel
                 System.LinkTypes.Hierarchy-Reverse and a url ending /_apis/wit/workItems/42; Milestone 0 adds none
                 if tags accumulate, UpdateIssue Labels [a] on an item tagged "a; b" sends System.Tags "a" (a re-routed item
                 loses its old dross/target tag); nil Labels sends no tags op; a State patch errors with 0 requests
                 if a secret leaves in a description, the azure-devops row of TestCreateIssueRefusedBeforeRequest refuses
                 CreateIssue on the description op's JSON path with 0 requests

  t-10 Validate azure-devops boards in dross doctor
       files:    internal/cmd/doctor.go, internal/cmd/doctor_board_ado.go, internal/cmd/doctor_board_ado_test.go,
                 internal/diag/combinations.go, internal/diag/diag_test.go
       covers:   c-1, c-9
       depends:  t-1, t-2, t-6
       desc:     doctor's [board] block gains an azure-devops arm (doctor_board_ado.go). It names a missing base_url, project
                 or auth_env, and checks base_url against the derived hostallow policy before reading the env through an
                 injected getenv. It also checks the api_version shape, auth_scheme, and whether area_path / iteration_path
                 exist (via forge PathExists). diag.BoardCombination warns on: an ineffective auth_scheme; an ignored
                 auth_user or milestone_mode; a base_url that names the project; [board].auth_scheme on a non-ADO board.
       contract: if a field goes unnamed, TestDoctorADOBoardMissingFields fails unless a [board] with only
                 provider = azure-devops prints ✗ lines naming [board].base_url, [board].project and [board].auth_env and
                 counts 3 issues
                 if doctor reads the token for a hostile host, TestDoctorADOBoardRefusesBeforeToken fails: base_url
                 https://attacker.example prints a ✗ naming attacker.example, the injected getenv is called 0 times for
                 auth_env, and no HTTP request is made
                 if a deployment is rejected, https://dev.azure.com/org, https://org.visualstudio.com and
                 https://tfs.corp.example/tfs/DefaultCollection (allowed via [remote].url) all pass base_url;
                 https://dev.azure.com/org/Proj with project Proj warns that base_url names the project
                 if config checks are lax, api_version "7.1&x" is ✗ and "6.0" passes; auth_scheme "nonsense" is ✗;
                 private-token or basic warns no effect; auth_user and milestone_mode set warn ignored; [board].auth_scheme
                 on a jira board warns no effect
                 if c-9's doctor half misreports, an area_path answered 404 is ✗ naming the path and the project; a 500 or 203
                 is a ⚠ "could not verify", never ✗ or ✓; unset paths make no request

  t-11 Route ADO base chores through a chore PR
       files:    internal/cmd/basebranch.go, internal/cmd/milestone.go, internal/cmd/chorepr.go,
                 internal/cmd/basebranch_ado_test.go
       covers:   c-10
       depends:  t-4, t-7
       desc:     routeBaseChores, routeProtectedMilestone and waitForMilestoneChores consult ship.BaseProtectionFunc instead
                 of a github-only branch. On azure-devops, protected bases send .dross-only chores through publishChorePR;
                 unknown protection refuses with a provider-specific fix; unprotected bases and other forges keep the direct
                 push. pullURL and chorePR.narrate become provider-aware (ADO /pullrequest/<n>, "auto-complete armed").
       contract: if ADO pushes straight to a policy base, TestADOProtectedMainRoutesChorePR fails: with BaseProtectionFunc
                 stubbed protected for main, a .dross-only commit ahead leaves origin main's SHA unchanged, OpenPRFunc gets
                 head dross-chores/main and base main, and AutoMergePRFunc gets "merge"
                 if the unprotected path regresses, the same fixture stubbed unprotected advances origin main by a direct
                 push and never calls OpenPRFunc
                 if unknown is guessed, Known=false returns an error naming the reason and [remote].auth_env, and pushes and
                 opens nothing
                 if code sneaks through, a non-.dross commit ahead of a protected ADO main is refused naming the file and
                 pushes nothing
                 if the milestone base is missed, routeProtectedMilestone on a protected ADO milestone/v1.8 opens a chore PR
                 into milestone/v1.8, and waitForMilestoneChores refuses while it is open, naming
                 https://dev.azure.com/o/p/_git/r/pullrequest/7
                 if GitHub regresses, basebranch_chores_test.go and milestone_protected_test.go pass unchanged (their
                 BranchRulesFunc stubs still decide, through BaseProtection's github arm)

Wave 4
  t-12 Link work items and ensure milestone Epic
       files:    internal/forge/azureboards_links.go, internal/forge/azureboards_links_test.go, internal/forge/forge.go
       covers:   c-2, c-6, c-9
       depends:  t-8, t-9
       desc:     Adds the ParentLinker interface (forge.go) and AzureBoardsClient.LinkParent: one GET with relations, then
                 nothing if the Hierarchy-Reverse already names the parent, else one PATCH [test /rev, remove the old parent
                 if any, add the new one]. LinkIssues is Related only and idempotent from either side.
                 EnsureMilestone/EnsureEpic(version, title, desc) finds the Epic by its exact tag dross/milestone:<v>, reuses
                 the lowest id and updates a stale title; otherwise it creates the Epic through createWorkItem, tagged with
                 dross and that identity tag. Relation targets are compared by trailing id.
       contract: if a child gets two parents, TestADOLinkParent fails: an existing parent different from the target is
                 replaced in one PATCH whose ops are test /rev, remove /relations/<i>, add Hierarchy-Reverse (never an add
                 alone); a stale-rev rejection errors naming the child, with no second PATCH
                 if re-runs write, a child already under its parent makes 1 GET and 0 PATCH, including when the relation url
                 carries a project-GUID segment (.../<guid>/_apis/wit/workItems/42)
                 if bad ids reach ADO, LinkParent(child, child) and a non-numeric parent error with 0 requests
                 if c-6's link drifts, TestADOLinkIssuesRelated fails: the PATCH op's rel is System.LinkTypes.Related, never
                 Hierarchy; a Related already present from either side makes 0 PATCH
                 if the Epic duplicates, TestADOEnsureEpic fails: an Epic tagged dross/milestone:v1.8 is reused with 0 POSTs;
                 two matches pick id 99 over 100 and warn; none creates one $Epic tagged "dross; dross/milestone:v1.8",
                 with AreaPath/IterationPath when configured; a stale title is PATCHed once, an unchanged one 0 times
                 if the capability surface is wrong, compile-time assertions pin IssueLinker and ParentLinker on
                 AzureBoardsClient, and TestGitHubIsNotAParentLinker fails if GitHubClient satisfies ParentLinker

  t-13 Write ADO states with verified close
       files:    internal/forge/azureboards_state.go, internal/forge/azureboards_state_test.go,
                 internal/forge/state_map_test.go
       covers:   c-3, c-4, c-6
       depends:  t-6, t-8
       desc:     SetState(key, status, override) reads the type and state and resolves the target through t-6. It warns on
                 stderr and skips when the status is unmapped or unreachable, or when a PATCH is rule-rejected, and does
                 nothing when the item is already there. CloseIssueAs refuses before writing if the target is not
                 Completed-category, otherwise PATCHes System.State and reads the category back. CloseIssue is
                 CloseIssueAs("complete"). SetStateRaw writes the literal state and verifies the read-back (EqualFold).
                 StateWriter is asserted.
       contract: if an unreachable status fails the sync, TestADOSetStateSkips fails: override
                 {"task-in-review": "Code Review"} on a Task without that state warns naming the status and id, sends
                 0 PATCH and returns nil; a 400 TF401320 rule error warns and returns nil
                 if c-3's mapping is bypassed, Scrum SetState(in-progress) PATCHes /fields/System.State to "Committed" on a
                 PBI and "In Progress" on a Task; an item already in the target makes 0 PATCH
                 if a non-done close passes, TestADOCloseVerifiesCompleted fails: override {"complete": "Resolved"} on an
                 Agile User Story errors naming the id with 0 PATCH; a PATCH answered 200 whose read-back is "Active" errors
                 naming the id; a read-back GET 500 errors; a "Closed" read-back returns nil
                 if undo is lossy, TestSetStateRawBypassesTheStateMap/azure-devops writes "Resolved" verbatim (not a
                 category), TestSetStateRawVerifiesReadBack/azure-devops errors naming the key when the read-back is
                 "Active", and a 400 on the write errors (unlike SetState)

Wave 5
  t-14 Dispatch azure-devops boards end to end
       files:    internal/forge/forge.go, internal/configenum/configenum.go, internal/boardsync/ctx.go,
                 internal/cmd/enum_divergence_test.go, internal/boardsync/ado_inbound_test.go
       covers:   c-1, c-5
       depends:  t-1, t-5, t-8, t-9, t-12, t-13
       desc:     NewBoard dispatches azure-devops to NewAzureBoards, and configenum.BoardProviders gains it (the enum fixture
                 goes from 3 to 4 cases). boardsync.Config passes APIVersion, AreaPath and IterationPath, plus AuthScheme for
                 azure-devops only. ado_inbound_test.go adds the adoRepo helper (a Ctx through Config + NewBoard against the
                 t-5 fake) that t-15 and t-16 use, and proves pull end to end.
       contract: if dispatch and accept-set diverge, TestBoardDispatchMatchesBoardProviders fails when azure-devops is in only
                 one of NewBoard and BoardProviders
                 if a board command builds the wrong client, adoRepo's ctx.Client is *forge.AzureBoardsClient (not
                 "unsupported provider")
                 if Config drops a field, the fake sees api-version=6.0 for [board].api_version 6.0, a Bearer header for
                 auth_scheme bearer, and AreaPath on a create; [remote].api_version 5.0 with [board] unset sends 7.1; a gitlab
                 board's forge.Config.AuthScheme stays "" even with [board].auth_scheme set
                 if pull lies, TestADOPullEndToEnd fails: CollectInbound(labels bug,enhancement, open) on an Agile fake returns
                 exactly the open untagged Bug and the Enhancement-tagged PBI, not the Dross-cased mirror and not a closed
                 Bug; a WIQL 500 returns an error, and EmitPullEnvelope prints issues [] with a non-null error

Wave 6
  t-15 Route ADO through phase and task sync
       files:    internal/boardsync/phase.go, internal/boardsync/task.go, internal/boardsync/ado_sync_test.go
       covers:   c-2, c-3, c-4
       depends:  t-14
       desc:     SyncPhase and setBoardState gain azure-devops arms (SetState with [board].state_map). CloseIssue gains an arm
                 that calls CloseIssueAs. SyncPhase re-parents an updated phase under its milestone Epic via ParentLinker.
                 SyncTasks prefers ParentLinker over IssueLinker for task → phase and warns once per run if it fails.
       contract: if c-3 is not wired, TestADOSyncStates fails: on Scrum, phase sync --status in-progress leaves the PBI at
                 Committed, and task sync --status task-in-progress leaves each Task at In Progress
                 if an unreachable state fails the run, state_map task-in-review → "Code Review" warns once per task naming
                 its id, and both syncs return nil
                 if '(closed)' lies, TestADOSyncCloseRefused fails: with state_map complete → "Resolved", phase sync --close
                 returns an error naming the PBI id and Ctx.Out has no "(closed)"; task sync --close names each refused Task
                 id while the other Tasks still reach Done
                 if hierarchy is a Related link, TestADOTaskSyncParents fails: after task sync each Task has exactly one
                 Hierarchy-Reverse to the phase id and no Related to it; a re-run makes 0 relation ops; a phase moved to
                 another milestone ends up under the new Epic only
                 if a link failure strands the sync, a LinkParent fault on one Task warns once, the other Tasks are parented,
                 and the next run heals it
                 if other boards regress, the existing YouTrack and Jira boardsync tests pass unchanged

  t-16 Route ADO through milestone, finalize and reap
       files:    internal/boardsync/milestone.go, internal/boardsync/finalize.go, internal/boardsync/ado_finalize_test.go,
                 internal/boardsync/ado_reap_test.go
       covers:   c-2, c-4, c-6, c-7
       depends:  t-14
       desc:     EnsureMilestoneLink gains an azure-devops arm (EnsureEpic with version, title and criteria).
                 CheckMilestoneClosable accepts the ADO client in any mode, since an Epic is an issue. finalizePlanTask
                 parents any task card it creates under the phase card via ParentLinker. Adds ADO finalize (c-7) and reap
                 apply/undo (c-6) tests against the t-5 fake.
       contract: if the Epic duplicates, TestADOMilestoneSyncReusesEpic fails: with an empty board.json and an Epic tagged
                 dross/milestone:v1.8 already on the fake, it reuses that Epic with 0 creates; CheckMilestoneClosable then
                 CloseIssue moves it to a Completed state, and a Resolved read-back makes CloseIssue error naming the Epic id
                 if finalize leaves work open, TestADOFinalize fails unless every Task reaches Done before the phase PBI's
                 state write in the fake's write log and the PBI ends Done; a card whose close the fake bounces is named by
                 id in the error, while the other cards and the phase still close
                 if a re-run writes, TestADOFinalizeRerunReadsOnly fails when a second FinalizePhase makes any fake write
                 (PATCH or POST workitems/$Type); it must narrate "already complete"
                 if finalize-created cards are orphaned, a task card missing from the board is created with exactly one
                 Hierarchy-Reverse to the phase card
                 if undo is lossy, TestADOReapUndoRestoresExactState fails: a User Story swept from Resolved to Closed reads
                 "Resolved" (not "Active") after Undo, with its prior tags; if the fake rejects one restore, Undo errors
                 naming that id and the restored count covers only the others

Wave 7
  t-17 Run the live Boards smoke and record it
       files:    internal/forge/testdata/ado_boards_live_smoke.toml
       covers:   c-8
       depends:  t-3, t-10, t-15, t-16
       desc:     Run make install first (r-01). Configure [board] on the scratch org's project (ado-scratch) and file a Bug by
                 hand. Then, with the installed binary in pair-mode, run dross doctor, milestone/phase/task sync, a
                 transition, a --close, and issue pull --labels bug,enhancement --json, filling in each step's result and
                 evidence. A failed live step becomes a /dross-quick fix and a re-run, never a mock-only pass.
       contract: if mock is reported as live, TestADOBoardsLiveRecordHonest (t-3) passes with posture "live" only when all five
                 steps pass with real-host evidence; any failed step leaves posture "partial"
                 if a leg is hollow, TestADOBoardsLiveRecordStepEvidence (t-3) rejects it unless the evidence shows: three
                 distinct typed ids, the task's InProgress state, a Completed-category close read-back, a task → phase → Epic
                 parent chain, and a pull envelope with error null that lists the hand-filed Bug and none of the dross ids
```

## Coverage

| Criterion | Tasks |
|---|---|
| c-1 | t-1, t-2, t-10, t-14 |
| c-2 | t-5, t-6, t-8, t-9, t-12, t-15, t-16 |
| c-3 | t-6, t-13, t-15 |
| c-4 | t-8, t-13, t-15, t-16 |
| c-5 | t-8, t-14 |
| c-6 | t-12, t-13, t-16 |
| c-7 | t-5, t-16 |
| c-8 | t-3, t-17 |
| c-9 | t-1, t-6, t-9, t-10, t-12 |
| c-10 | t-4, t-7, t-11 |

All 10 criteria covered.

## Risk register (each risk → its one owner)

- **Edge cases:**
  - t-6: Scrum New vs Approved; no default ever picks Removed; the Resolved→InProgress fallback.
  - t-8: Removed reads open but is excluded from pulls; Basic has no Bug type; ADO returns tags in another casing; a t-1 tag lookup must not match t-10.
  - t-4: the DefaultBranch matchKind; repo-wide push policies with no refName.
  - t-12: relation URLs that carry a project-GUID segment.
  - t-16: task cards created during finalize are left without a parent.
- **Concurrency:**
  - t-12: a stale /rev when re-parenting; picking among duplicate Epics (lowest numeric id).
  - Duplicate phase and task cards keep the existing lowest-key rule, unchanged.
- **Bad input:**
  - t-2: api_version injection; a hostile base_url.
  - t-8: quotes injected into the WIQL; a stale non-numeric key.
  - t-9: a ';' in a label; a title over 255 characters; HTML in a body.
  - t-1 / t-6: backslash area paths.
  - t-10: a base_url that names the project; a hostile host at doctor time.
- **Partial failure:**
  - t-8: one batch chunk fails.
  - t-15: the create succeeds but the parent link fails.
  - t-13: the close write succeeds but the read-back fails.
  - t-16: one finalize card is refused; one restore write is rejected.
  - t-7: the chore PR opens but auto-complete is refused.
  - t-4: the policy list is unreadable or truncated.
  - t-8 / t-14: an unreachable board on pull.
  - t-10: the doctor path check can't reach the board.
- **Security:**
  - t-2: a redirect carrying the token; body scrubbing; registration in the transport walker.
  - t-9: the secretscan table row.
  - t-7: bypassPolicy is never set.
  - t-8: a WIQL that crosses projects.
- **Regression:**
  - t-4 / t-11: GitHub chore routing.
  - t-15: the YouTrack and Jira arms.
  - t-14: gitlab boards' auth_scheme.
  - t-6 / t-14: the divergence guards.
  - t-3 / t-17: mock reported as live.

## Judgment calls

- **Transport location.** Chose a forge-local AzureBoardsClient.doRaw, registered in secretscan.Transports. Rejected extracting ship's adoRequest into a shared package, because the transport walker covers only ship and forge, and extraction would move shipped code under a new root. t-2's contracts restate ship's invariants instead.
- **Parenting.** Chose an explicit ParentLinker interface that boardsync prefers over IssueLinker. Rejected overloading LinkIssues by work-item type: LinkIssues must stay Related for backlog (c-6), and type-sniffing would quietly turn any future link to a Task into a re-parent.
- **Update and parents.** On ADO, UpdateIssue ignores patch.Milestone (documented), and the create carries the parent op atomically. Rejected re-parenting inside UpdateIssue, because it would chain links before items and add a wave.
- **Work item type.** Inferred from identity labels (dross/task: → Task, else the requirement type). Rejected a new IssueInput.Kind, which would touch every CreateIssue call site across providers. The labels are already dross's identity contract.
- **Epic identity tag.** dross/milestone:<v> stays ADO-local, not in forge.identityLabelPrefixes. Adding it there would require a new reap lane. Reap reports a marker card with no identity as unclassifiable and never closes it.
- **Epic marker.** The Epic carries the `dross` marker so pull never lists it (c-5). Accepted cost: if board.json loses the milestone link, reap reports the Epic as unclassifiable.
- **Removed items.** A Removed-category item reads State "open" and Resolved false, so finalize re-closes it, but it is filtered out of open pulls. Rejected State "closed" for Removed: finalizeCard's `Resolved || State=="closed"` skip would leave a Removed card that never reaches Done.
- **Close verification.** CloseIssueAs refuses before writing when the target is not Completed-category, and verifies after writing. Rejected verify-only, which leaves a Resolved state written on the board while the command errors.
- **Category map shape.** defaultADOCategoryMap is a plain map[string]string literal, so the AST-parsing divergence guards can read it. A typed-constant map would make them vacuous.
- **Mapping "shipped".** shipped, uat and task-in-review map to Resolved (falling back to InProgress), treating "shipped" as in-review. Rejected mapping shipped to Completed as Jira does: that would make the phase lane's working and terminal states the same.
- **Policy pagination.** One $top page, with a full page treated as unknown (the codebase's existing truncation rule). Rejected following the continuation-token header, because adoRequest exposes no headers and widening it touches every ship caller.
- **Unknown merge-strategy policy.** It gives Manual (ErrAutoMergeUnavailable). Auto-complete is never armed blind.
- **Test fake.** One stateful ADO fake in boardsync, built in wave 1 from ADO's documented rejections and independent of the client. Rejected per-test scripted handlers: they encode the client's own assumptions and cannot express the one-parent rule or rev tests.
- **File-disjoint waves.** No two tasks in a wave edit the same file. forge.go is edited by t-2 (W1), t-12 (W4) and t-14 (W5). boardsync is split into phase/task (t-15) and milestone/finalize (t-16). This costs about one wave of depth.
- **Config passthrough.** boardsync.Config passes [board].auth_scheme only for azure-devops. Rejected passing it for every provider, which would silently change gitlab board auth.
- **Doctor path check.** A 404 is ✗ and any other failure is ⚠ "could not verify". An unreachable board is never reported as a missing path.
- **Pull type-matching.** Applies only to non-dross labels. Identity and marker lookups stay tag-only, so an identity lookup can never match a work-item type.
- **Long titles.** Truncated to 255 runes rather than refused. The identity tag, not the title, is the lookup key, so truncation never breaks dedupe.
- **Description format.** HTML-escaped with newlines → <br>. Rejected the Markdown multilineFieldsFormat op, which older Server versions may not support.
- **c-10 verification.** c-10 is mock-verified only, per verify_posture. Rejected adding a policy leg to the c-8 live record, which c-8's text does not cover.
- **API assumptions the mocks encode.** These are first exercised for real in t-17: backlogconfiguration at project scope with no team segment, Tags CONTAINS as a whole-tag match, a System.Tags write replacing the whole set, and the status a rev-test failure returns. A mismatch there gets a /dross-quick fix to both the client and the t-5 fake.
