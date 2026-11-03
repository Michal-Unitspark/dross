// Package boardsync mirrors dross planning artefacts — phases, tasks,
// milestones and the deferred backlog — onto the repo's issue-tracker board,
// and reconciles what the board says back. The cobra verbs that expose it live
// in internal/cmd; this package never knows about a command, and narrates
// through Ctx.Out rather than a package-level writer.
package boardsync

import (
	"fmt"
	"io"
	"os"

	"github.com/Rivil/dross/internal/board"
	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/forge"
	"github.com/Rivil/dross/internal/hostallow"
	"github.com/Rivil/dross/internal/project"
)

// Board label vocabulary. A single marker label identifies dross-managed
// issues; one status label tracks lifecycle stage. Keeping it to two labels
// avoids cluttering the board's label list.
//
// Every status literal here is a configenum.LifecycleStatuses member — the same
// set both forge state maps key on. StatusPlanned reads "planned", not
// "planning": the maps have always keyed "planned", it is what state.json's
// current_phase_status carries once plan.toml locks, and "planning" wrongly
// implies work in flight. Issues already synced re-label on the next
// phase-sync, which replaces the label set wholesale.
const (
	LabelMarker = "dross"
	LabelQuick  = "dross/quick"

	StatusPlanned    = "planned"
	StatusInProgress = "in-progress"
	StatusUAT        = "uat"

	// The task-level pair. Named here rather than left as bare literals in the
	// prompts because board-task-inbound has to READ them back off an issue's
	// dross/status label and convert to a plan status — see task_lifecycle.go.
	StatusTaskInProgress = "task-in-progress"
	StatusTaskInReview   = "task-in-review"

	// The phase lane's terminal state, written by FinalizePhase. The task
	// lane's own terminal, StatusTaskComplete, lives in lifecycle.go — two
	// lanes, two dross/status: labels.
	StatusComplete = "complete"
)

func StatusLabel(s string) string { return "dross/status:" + s }

// phaseLabel is the durable identity of a phase's board issue. It is what
// makes the TRACKER the source of truth for the phase→issue mapping: board.json
// is git-tracked, so a phase-sync writes it on phase/<id>, which `phase
// complete` then deletes — the mapping never reaches the base branch, and the
// next ship mints a duplicate. A label on the issue survives that, and survives
// a second machine or CI that never had the file.
func PhaseLabel(phaseID string) string { return "dross/phase:" + phaseID }

// taskLabel marks an issue as mirroring one plan task. The pair is in the label
// because task ids are unique only within a phase.
func TaskLabel(phaseID, taskID string) string { return "dross/task:" + phaseID + "/" + taskID }

// Ctx bundles everything a board operation needs.
type Ctx struct {
	Client    forge.BoardClient
	Board     *board.Board
	Proj      *project.Project
	Root      string
	BoardPath string
	// Out is where a sync narrates its result. Nil means os.Stdout, resolved
	// at call time by out() — never at init, because a test that swaps
	// os.Stdout for a pipe does so after this package is initialised.
	Out io.Writer
}

// out returns the writer a sync narrates to. Nil-safe on both the Ctx and
// its Out so a zero Ctx still prints somewhere sensible.
func (c *Ctx) out() io.Writer {
	if c != nil && c.Out != nil {
		return c.Out
	}
	return os.Stdout
}

// boardConfig maps a [board] block onto a forge.Config. base_url is the API
// base; project is the tracker-native identifier — a "owner/repo" path for
// forge backends, the numeric/path project ref for GitLab, the short-name for
// YouTrack. For the forge backends a synthetic URL carries owner/repo to the
// client (the real host is irrelevant — every call targets base_url).
// remoteURL is the derivation source for the API host allowlist, and it is
// [remote].url — NOT the synthetic "https://board.local/<project>" URL below.
// The synthetic URL exists only to carry owner/repo to the forge backends;
// deriving the allowlist from it would authorize a host nobody configured and
// make the policy self-satisfying.
func Config(b project.Board, remoteURL string, extra []string) forge.Config {
	cfg := forge.Config{
		Provider: b.Provider,
		APIBase:  b.BaseURL,
		AuthEnv:  b.AuthEnv,
		AuthUser: b.AuthUser,
		Project:  b.Project,
		BoardID:  b.GitHubProject,
		URL:      "https://board.local/" + b.Project,
		Hosts:    hostallow.Derive(remoteURL, extra),
		Fields: forge.Fields{
			State:       b.Fields.State,
			Type:        b.Fields.Type,
			FixVersions: b.Fields.FixVersions,
		},
		// Azure Boards only reads these; every other backend ignores them.
		// Never from [remote]: a repo's Azure Repos api_version says nothing
		// about the Boards server it tracks on.
		APIVersion:    string(b.APIVersion),
		AreaPath:      b.AreaPath,
		IterationPath: b.IterationPath,
	}
	switch configenum.Normalize(b.Provider) {
	case "gitlab":
		cfg.ProjectID = b.Project
	case "azure-devops":
		// [board].auth_scheme picks PAT-over-Basic or a bearer token for
		// Azure Boards alone. forge.New reads AuthScheme for GitLab's REST
		// header too, so passing it to any other board would let a key that
		// only documents azure-devops change how a GitLab board signs in.
		cfg.AuthScheme = b.AuthScheme
	}
	return cfg
}

// wrapBoard tags operational forge errors so telemetry buckets them as
// "board" instead of generic network/other.
func Wrap(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("board: %w", err)
}

// deferredBacklogKey is the board-link key for a deferred item. It keys on the
// item's stable id, never its position: a positional key re-points at whatever
// item slides into the index when an earlier sibling is removed, which silently
// re-titles a live issue to a different finding's text (c-8).
func DeferredBacklogKey(id string) string { return "someday:id:" + id }

// deferredLabel is a deferred item's durable identity on the tracker, and
// targetLabel carries its routed destination. Both are labels rather than
// board.json entries for the reason phaseLabel is: mirrorDeferredAdd mints the
// issue on phase/<id>, whose board.json dies with the branch — so the mapping
// has to live somewhere the tracker keeps.
func DeferredLabel(id string) string { return "dross/deferred:" + id }

func TargetLabel(slug string) string { return "dross/target:" + slug }

// hasMarker reports whether an issue carries the dross marker label. Every
// adoption path post-filters on it: the tracker query is by phase label alone
// (see resolvePhaseIssue), and a hand-made issue that happens to carry that
// label must not be silently taken over.
func HasMarker(iss forge.Issue) bool {
	for _, l := range iss.Labels {
		if l == LabelMarker {
			return true
		}
	}
	return false
}
