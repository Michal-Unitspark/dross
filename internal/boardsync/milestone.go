package boardsync

import (
	"fmt"
	"strings"

	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/forge"
	"github.com/Rivil/dross/internal/milestone"
)

// checkMilestoneClosable refuses --close unless the tracker entity this repo's
// milestones map to is an ISSUE. Only YouTrack's epic mode stores an issue's
// idReadable in the milestones slot; a version bundle stores a version name, an
// agile board a board name, and the REST forges and GitHub a numeric milestone
// id — none of which a close verb can address.
//
// The forge case is not merely inert: a numeric milestone id shares an id space
// with those backends' own issue keys, so an ungated close would resolve
// milestone 7 to issue #7 and close a human's issue.
func CheckMilestoneClosable(ctx *Ctx) error {
	mode := configenum.Normalize(ctx.Proj.Board.MilestoneMode)
	if mode == "" {
		mode = "version" // the documented code default
	}
	if _, ok := ctx.Client.(*forge.AzureBoardsClient); ok {
		// Azure Boards: a milestone is always an Epic — a work item, closed
		// like any other — whatever milestone_mode says (it is not read).
		return nil
	}
	if _, ok := ctx.Client.(*forge.YouTrackClient); !ok {
		return fmt.Errorf("milestone-sync --close needs a milestone that is itself an issue; %s maps a milestone to a milestone entity, not an issue — nothing to close",
			ctx.Proj.Board.Provider)
	}
	if mode != "epic" {
		return fmt.Errorf("milestone-sync --close needs a milestone that is itself an issue; [board].milestone_mode is %q, which maps a milestone to a %s entity — nothing to close (set milestone_mode = \"epic\")",
			mode, mode)
	}
	return nil
}

// ensureMilestoneLink returns the board milestone id for a dross milestone
// version, creating the board milestone (and storing the link) if needed.
// Returns 0 (no error) when the milestone toml doesn't exist locally, so
// callers can treat "no milestone" as "skip assignment".
func EnsureMilestoneLink(ctx *Ctx, version string) (string, error) {
	if id, ok := ctx.Board.MilestoneID(version); ok {
		return id, nil
	}
	m, err := milestone.Load(milestone.FilePath(ctx.Root, version))
	if err != nil {
		return "", nil // not found locally — nothing to link
	}
	title := m.Milestone.Title
	if title == "" {
		title = version
	}
	desc := strings.Join(m.Scope.SuccessCriteria, "\n")
	var id string
	switch c := ctx.Client.(type) {
	case *forge.YouTrackClient:
		// YouTrack maps a milestone to an entity per [board].milestone_mode
		// (version bundle / agile board / epic), not a forge-style milestone.
		id, err = c.EnsureMilestoneEntity(ctx.Proj.Board.MilestoneMode, version, desc)
	case *forge.AzureBoardsClient:
		// Azure Boards maps a milestone to an Epic (the locked hierarchy),
		// found by its dross/milestone:<version> tag before one is created,
		// so a re-clone with no board.json reuses the Epic already there.
		id, err = c.EnsureMilestoneEpic(version, title, desc)
	case *forge.JiraClient:
		// Jira maps a milestone to a project VERSION (string id via the concrete
		// path). The returned id is numeric, so the phase-sync int-milestone path
		// still attaches it (as a Fix Version) downstream.
		id, err = c.EnsureMilestoneEntity(ctx.Proj.Board.MilestoneMode, version, desc)
	default:
		id, err = ctx.Client.EnsureMilestone(version, MilestoneBody(title, desc))
	}
	if err != nil {
		return "", Wrap(err)
	}
	if id == "" {
		return "", nil // backend ensured no entity (e.g. youtrack mode dispatch lands later)
	}
	ctx.Board.SetMilestone(version, id)
	return id, nil
}

func MilestoneBody(title, criteria string) string {
	if criteria == "" {
		return title
	}
	return title + "\n\nSuccess criteria:\n" + criteria
}
