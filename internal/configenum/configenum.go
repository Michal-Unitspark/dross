// Package configenum is the single home for dross's enumerated config values.
//
// Before this package, every validator carried its own literal list and every
// consumer carried its own dispatch switch, so the two drifted: doctor rejected
// board providers the CLI could dispatch, and ship's error message named a set
// that no longer matched its arms. Both sides now resolve through the Sets
// below, and internal/cmd/enum_divergence_test.go fails the build when a
// dispatch switch and its Set diverge.
//
// This is a leaf package: it imports nothing from internal/, so validators,
// dispatchers and the schema can all depend on it without a cycle.
package configenum

import "strings"

// Normalize is the one normalisation applied to every enumerated config value
// in this repo. Validators and dispatch both call it, so no site can be more or
// less forgiving than another — doctor cannot bless a value that a consumer's
// switch then silently skips.
func Normalize(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

// Set is an ordered set of accepted values for one config field, plus the value
// an empty field falls back to in code.
//
// The empty-value policy is per-field and deliberately not uniform: an empty
// [remote].auth_scheme means private-token, an empty [board].milestone_mode
// means version, but an empty [board].provider means nothing at all and stays
// invalid. Has reads def to tell those apart — a Set with no default rejects
// the empty string.
type Set struct {
	values []string
	def    string
}

func newSet(def string, values ...string) Set {
	return Set{values: values, def: def}
}

// Values returns the accepted values in declaration order. The copy is
// defensive: Sets are package-level vars and must not be mutable by a caller.
func (s Set) Values() []string {
	out := make([]string, len(s.values))
	copy(out, s.values)
	return out
}

// Has reports whether v — after Normalize — is an accepted value. An empty v is
// accepted only when the field has a code default to fall back to.
func (s Set) Has(v string) bool {
	n := Normalize(v)
	if n == "" {
		return s.def != ""
	}
	for _, want := range s.values {
		if n == want {
			return true
		}
	}
	return false
}

// List renders the set as the pipe-joined literal that doctor, issue and forge
// interpolate into their "expected ..." messages, so those messages cannot
// name a set the code no longer accepts.
func (s Set) List() string {
	return strings.Join(s.values, " | ")
}

// BoardProviders is the set of trackers internal/forge can build a board client
// for (forge.NewBoard, falling through to forge.New for the REST forges).
//
// Empty is rejected: an unset [board].provider dispatches nowhere.
var BoardProviders = newSet("", "forgejo", "gitea", "gitlab", "youtrack", "jira", "github")

// ForgeRESTProviders is the subset of BoardProviders served by forge.New's
// generic REST client. It is exported so forge.New's expected-provider message
// derives from it rather than re-deriving the same three names by hand.
var ForgeRESTProviders = newSet("", "forgejo", "gitea", "gitlab")

// ShipProviders is the set of [remote].provider values internal/ship can
// dispatch — open a PR, post a review comment and report merge status.
//
// "none" is a sentinel meaning "this repo has no remote", handled at the call
// sites, and is deliberately not a member: it names the absence of a backend,
// not one of them.
var ShipProviders = newSet("", "github", "forgejo", "gitea", "gitlab", "bitbucket", "azure-devops")

// AuthSchemes is the set of [remote].auth_scheme credential selectors. Empty
// defaults to private-token in code, so an unset scheme is valid.
var AuthSchemes = newSet("private-token", "private-token", "bearer", "basic")

// MilestoneStatuses is the set of [milestone].status values. It is pinned by
// the on-disk corpus rather than by any completion path: `dross milestone
// create` writes "planning", the generic setter writes the rest, and `dross
// milestone complete` writes no status at all — so what the milestone tomls
// actually carry (complete, and active on the one in flight) is the truth.
//
// Empty is rejected: a blank status says nothing about where a milestone is,
// and blanking the field through `milestone set` is a mistake, not a default.
var MilestoneStatuses = newSet("", "planning", "active", "complete")

// LifecycleStatuses is the set of dross phase lifecycle statuses that reach an
// issue board: the value in the `dross/status:<v>` label, and the key both forge
// state maps (defaultJiraStateMap, defaultYouTrackStateMap) resolve a board
// state from.
//
// It is the producer side and the map side of one vocabulary, which is exactly
// where they drifted: issue.go emitted "planning" while both maps keyed
// "planned", so a phase whose plan was locked but unstarted warned and skipped
// its transition. internal/cmd/board_lifecycle_divergence_test.go now fails the
// build when either side leaves the other behind.
//
// Empty is rejected: a blank status maps to no board state, and it is never a
// request to derive one — syncPhase already derives from the plan when
// --status is absent.
// "verifying" was retired by board-task-mirror: "uat" replaced it at its only
// emitting call site, and a member nothing emits is a state-map key nothing
// will ever reach — the precise thing the divergence guard forbids. A repo
// whose [board].state_map still overrides "verifying" gets a stale-key issue
// from `dross doctor`, which is the surface that exists for exactly this.
//
// The last three are TASK- and verdict-level states (board-task-mirror). They
// extend this same set rather than starting a second vocabulary: there is one
// override point ([board].state_map) and one build-failing divergence guard,
// and a second mechanism would let the two drift — which is the exact bug this
// set was introduced to close.
// "task-complete" is the TASK lane's terminal state, and it is separate from
// "complete" on purpose: the two lanes each carry their own `dross/status:`
// label, so reusing the phase lane's value would put the phase issue's own
// label onto every task card and make task_lifecycle.go's board->plan reading
// ambiguous.
var LifecycleStatuses = newSet("", "planned", "in-progress", "shipped", "complete",
	"task-in-progress", "task-in-review", "task-complete", "uat")

// RuntimeModes is the set of [runtime].mode values.
//
// docker | native, and deliberately NOT hybrid. hybrid was accepted and
// documented for as long as the field existed, and its only consumer was
// `p.Runtime.Mode != "docker"` (internal/cmd/verify.go) — so hybrid and native
// compiled to the same program and the string's entire effect was passing
// validation. A per-service split is a fact [runtime.services] already carries;
// it was never a third whole-project mode.
//
// Empty is rejected: an unset mode has no code default, and validate has always
// reported it as a problem in its own right.
var RuntimeModes = newSet("", "docker", "native")

// RepoLayouts is the set of [repo].layout values. Empty falls back to single,
// which is what the scaffold writes and what every single-module repo means.
var RepoLayouts = newSet("single", "single", "monorepo")

// CommitConventions is the set of [repo].commit_convention values. Empty falls
// back to conventional, which is what init writes; freeform is the opt-out for
// a repo whose history does not use type(scope): subjects.
var CommitConventions = newSet("conventional", "conventional", "freeform")

// MilestoneModes is the union of [board].milestone_mode values across the
// trackers. Empty defaults to version in code. Not every provider accepts every
// mode — see MilestoneModesFor.
var MilestoneModes = newSet("version", "version", "agile", "epic")

// MilestoneModesFor returns the modes a given board provider actually accepts,
// or nil when the provider maps milestones by some other means (the REST forges
// and github use their own milestone objects and never read milestone_mode).
//
// A nil result means "milestone_mode does not apply here", not "no mode is
// valid" — callers checking a combination should skip the check when it is nil.
func MilestoneModesFor(provider string) *Set {
	switch Normalize(provider) {
	case "youtrack":
		s := MilestoneModes
		return &s
	case "jira":
		// jira.go maps a milestone to a fixVersion and errors on anything else.
		s := newSet("version", "version")
		return &s
	default:
		return nil
	}
}

// BoardRequiresBaseURL reports whether [board].base_url must be set for a
// provider. Only github has a usable default (https://api.github.com); every
// other backend is self-hosted or per-tenant and has no address to guess.
func BoardRequiresBaseURL(provider string) bool {
	return Normalize(provider) != "github"
}

// SelectorStyles is the set of [[runtime.test_lane]] `selector` values: the
// shape a lane's matched paths take when `dross test --files` appends them to
// that lane's command.
//
// The members name selector SHAPES, not runners — `path` emits each matched
// path, `dir` its parent directory, `go-package` the ./<dir>/... form Go's
// tooling takes. A runner is configured by pointing its lane at whichever shape
// it accepts, so dross never grows a per-framework translation engine.
//
// Empty is rejected by Has, and that is deliberate rather than an oversight: an
// omitted selector means "append nothing", which is the pre-selector behaviour
// every existing lane already has. Validators guard the empty case themselves
// before consulting the Set, so a lane that declares no selector is valid while
// a lane that declares a nonsense one is named and refused.
var SelectorStyles = newSet("", "path", "dir", "go-package")
