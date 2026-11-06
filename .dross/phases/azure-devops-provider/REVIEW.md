# Plan Review — azure-devops-provider

Reviewed: 2026-10-28
Plan: 13 tasks across 4 waves

## BLOCKING
(none)

## FLAG
- [antipattern / shared ledger] Only t-4 carries the constraint "Response structs stay function-local (no not_paths rows)". t-3 (ADO error or sign-in bodies), t-5 (thread payload) and especially t-6 also decode or encode ADO JSON. t-6 is the most likely to break it: azureDevOpsPRStatus and adoOpenPRBySource parse the same PR shape (status, targetRefName, sourceRefName, forkSource), so the natural move is one package-level tagged struct. TestEveryPathShapedFieldIsDeclared enumerates every tagged string field outside function scope in internal/ship, so a shared struct turns the guard red at t-6's commit. The fix is to edit internal/cmd/testdata/pathfence_scan/not_paths.txt, which t-7 owns in the same wave 2. That is a parallel-edit collision.
  Suggestion: Copy t-4's function-local-structs constraint into the t-3, t-5 and t-6 descriptions. Alternatively, have whichever task owns a shared struct also own the not_paths rows, and order it against t-7.

- [test-contract] t-1's last contract says "if EscapedPath alters a non-ADO remote, the existing TestDetectRemote and TestDetectRemoteDropsUserinfo rows fail". None of those rows has a percent-encoded path (internal/project/remote_test.go has no `%XX` in any input), so Path and EscapedPath give identical output for all of them. The contract cannot detect the behaviour change it is meant to guard. That change is real: a non-ADO `https://host/o/My%20Repo` used to canonicalize to a raw space and will now keep `%20`.
  Suggestion: Add a non-ADO row with an encoded segment (e.g. a gitlab.com or codeberg remote containing `%20`) that pins the intended post-change URL.

- [test-contract] t-12's only contract checks that the substrings "remote.api_version" and "azure-devops" appear somewhere in options.md. The description promises more: Basic :PAT as the default, bearer for Entra, private-token/basic having no effect, auth_user being ignored, the 7.1 default with the 2019/2020/2022 Server versions, and the refusal of http-only TFS. None of that is pinned, and "azure-devops" would pass on any incidental mention. Compare t-11, which pins AZURE_DEVOPS_EXT_PAT on the auth_env bullet.
  Suggestion: Name at least the auth_scheme guidance (the auth_scheme lock) and the api_version default/Server table in the contract, the way t-11 does for its lock.

- [wave-order] t-12 (wave 4, depends_on t-7 and t-8) needs no output from either. Its test reads options.md text only, and it shares no files with t-7, t-8 or anything in waves 1–3. It could run in wave 1.
  Suggestion: Drop t-12's depends_on and move it to wave 1. If ordering docs after code is deliberate, say so in the description.

- [coverage-depth] t-6 lists c-2 in `covers`, but none of its six contracts uses a Server remote (/tfs/Coll or a host-root collection). Server addressing is asserted for open (t-4, and t-10's full flow), dedupe GET (t-10) and comment (t-10). GetPRStatus against a Server remote is asserted by no task. c-2 says "every ship call addresses the right repo".
  Suggestion: Add a /tfs/DefaultCollection row to t-6's status test, asserting the request path `/tfs/DefaultCollection/P/_apis/git/repositories/R/pullRequests/<n>`. Otherwise drop c-2 from t-6's covers.

- [locked-decision pinning] Two cases that locked decisions spell out have no row:
  (a) The auth_scheme lock says "auth_user is ignored". Every row in t-3's TestAzureDevOpsAuthHeader leaves AuthUser unset. If adoRequest reuses bitbucket's user:token Basic helper, a set auth_user sends `me:PAT`. ADO accepts that, so nothing downstream fails and the lock is silently broken.
  (b) The repo_identity lock names `o.visualstudio.com/_git/r` → repo == project with an empty base. t-1's TestParseAzureLegacyHostCarriesOrg covers `/p/_git/r`, `DefaultCollection` and vs-ssh, but not this form.
  Suggestion: Add an AuthUser="me" row to TestAzureDevOpsAuthHeader asserting base64(':'+tok). Add an `https://o.visualstudio.com/_git/r` row to t-1 asserting project r, repo r and api_base https://o.visualstudio.com.

- [seam t-1/t-8] c-2 names the Server ssh form `ssh://<host>:22/...`, and the canonical_host lock says "Server ssh drops the port". t-8's DetectRemote contract pins only the https Server row (Provider stays empty). With an origin of `ssh://tfs.corp:22/tfs/Coll/P/_git/R`, the generic DetectRemote path writes `https://tfs.corp:22/tfs/Coll/P/_git/R`, which causes two problems:
  - hostallow.Derive then authorizes `tfs.corp:22` and refuses the hand-set `https://tfs.corp/tfs/Coll` api_base on :443.
  - t-9's canonicalizer would call that url a mismatch against its own origin.

  A user who follows c-2 literally and hand-sets [remote].url to the ssh form hits the same refusal.
  Suggestion: In t-8, pin the url DetectRemote writes for a Server ssh origin. Route Server-shaped `_git` remotes through ParseAzureDevOpsRemote for the url while leaving Provider empty, or state the limitation. Either way, have t-11 or t-12 tell the user to hand-write the https url.

- [scope edge, t-9] t-9 strips userinfo from the "does not match" line only. The sibling branch in the same switch, `internal/cmd/doctor.go` line 120 (`✗ git origin = %s but project.toml has no [remote]`), prints the same raw `gitURL`. A PAT embedded in origin (`https://user:PAT@dev.azure.com/...`) is a common ADO setup, and it leaks there.
  Suggestion: Apply the same strip to that line and add a row to TestDoctorMismatchStripsUserinfo, or a sibling test, for the no-[remote] branch.

## NOTE
- [granularity] Three tasks exceed the split thresholds:
  - t-3 touches 6 files across ship and secretscan.
  - t-7 touches 7 files across project, cmd and testdata.
  - t-8 touches 10 files across configenum, project, ship and prompts.

  Each is guard-forced. t-3 is forced by the transport registry walker plus scrubbingHelpers, t-7 by TestNoSettableKeyIsInert plus the not_paths ledger plus json-tag parity, and t-8 by the locked guard_atomicity decision plus TestPromptProviderListsMatchShipProviders. Splitting any of them would leave a guard red between commits. No action needed.
- [granularity] t-2 bundles two unrelated surfaces: the secretscan PAT rule (c-9) and the hostallow SaaS defaults (c-4). The hostallow half alone would be a one-file merge candidate, so the bundle is a reasonable trade.
- [scope] t-13 changes defaults.Apply for every provider, not just ADO. For example, a saved github default will no longer seed GITHUB_TOKEN onto a detected gitlab.com remote. The change is defensible because it stops a token of one forge being sent to another host, but no criterion names it. Call it out in the PR body.
- [rules r-01] Only t-11 mentions `make install`. t-8 and t-12 also edit assets/prompts, and most tasks change Go code. That is not a violation, because every contract reads source in-process. But the verify-time live smoke run (verify_posture) uses the installed `dross`. Without `make install` first, it exercises a binary that does not know azure-devops.
- [ux] After t-8, the deferred surfaces return `unsupported provider "azure-devops" (expected github | … | azure-devops)` through their default arms. The four are ListPRComments, PRHeadOf, AuthenticatedUser and OpenPRsTargeting. TestAzureDeferredSurfacesFailClosed pins only that they error, which is the right fail-closed property. The self-contradictory wording will show up in /dross-respond and in milestone-complete's skip line until azure-devops-ship-loop lands.
- [strength] The plan knows the guards. It names the exact existing tests each change would break (enum_divergence's providerSwitchIn first-switch rule, the transport registry walker, scrubbingHelpers, TestNoGetenvOutsideHostguard, the randomLine panic path) and puts each guard-forced edit in the right task. t-8 is exactly the locked guard_atomicity unit. t-6's FindOpenPRByHead arm deliberately sits outside the scanned dispatch sites, so it can land in wave 2 without reddening anything.
- [strength] The test contracts are concrete and adversarial rather than happy-path. Examples:
  - substring host spoofs (`evil-visualstudio.com`, `visualstudio.com.evil.example`)
  - AZDO at offsets 75 and 77, and 83- and 85-char runs
  - a 203 sign-in page and a 302 redirect read as auth failure
  - `%2520` double-encoding
  - ambiguous two-match identity lookups
  - a mock that ignores the server-side filter
  - "failed lookup is not ErrHeadPRLookupUnsupported"
- [strength] The wave layout has no same-wave file overlap in any wave. Every wave-N+1 dependency except t-12 is a real data or file dependency, and t-11's position is forced by sharing init.md and onboard.md with t-8.

## Summary
The plan covers all nine criteria and conflicts with no locked decision or rule, and its guard awareness is unusually strong. Before execution, fix the shared not_paths ledger risk in wave 2 and pin the Server-ssh and GetPRStatus-on-Server gaps. The remaining flags are contract-tightening.
