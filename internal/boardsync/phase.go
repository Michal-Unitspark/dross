package boardsync

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Rivil/dross/internal/forge"
	"github.com/Rivil/dross/internal/phase"
)

// resolvePhaseIssue finds the phase's existing board issue, or "" if there is
// none yet. Resolution order:
//
//  1. board.json's cached key, verified against the tracker. A key that no
//     longer resolves is dropped rather than trusted — the file is a cache.
//  2. A tracker query for the phase label. This is the durable mapping: it
//     survives the deleted phase branch that takes board.json with it.
//  3. A legacy issue carrying the marker and the exact `<id> — <title>`
//     summary, from before phase labels existed. Adopting it (the caller
//     back-fills the phase label on update) is what stops one duplicate per
//     pre-existing phase the first time this runs.
//
// The query filters on ONE label. Two would be OR'd now (c-1), which would
// adopt an arbitrary dross issue; and State must be "all", because an
// already-closed phase issue is exactly the one a re-ship must find.
func ResolvePhaseIssue(ctx *Ctx, phaseID, title string) (string, error) {
	if key, ok := ctx.Board.PhaseIssue(phaseID); ok {
		if iss, err := ctx.Client.GetIssue(key); err == nil && iss != nil && HasMarker(*iss) {
			return key, nil
		}
		fmt.Fprintf(os.Stderr, "warning: board.json points %s at %s, which no longer resolves — re-resolving from the tracker\n", phaseID, key)
		ctx.Board.DeletePhase(phaseID)
	}

	byLabel, err := ctx.Client.ListIssues(forge.IssueFilter{State: "all", Labels: []string{PhaseLabel(phaseID)}})
	if err != nil {
		return "", Wrap(err)
	}
	var matches []string
	for _, iss := range byLabel {
		// A task card carries its phase's label too, so it answers the same
		// query. It is never the phase card: taking it would put the phase's
		// status on a task, leave the real phase card unmatched, and — since
		// keys sort as strings, PROJ-100 before PROJ-98 — happen as soon as a
		// board passes its hundredth issue.
		if HasMarker(iss) && !hasTaskLabel(iss.Labels) {
			matches = append(matches, iss.Key)
		}
	}
	if len(matches) > 0 {
		sort.Strings(matches)
		if len(matches) > 1 {
			fmt.Fprintf(os.Stderr, "warning: %d issues carry %s (%s) — updating %s and leaving the rest\n",
				len(matches), PhaseLabel(phaseID), strings.Join(matches, ", "), matches[0])
		}
		return matches[0], nil
	}

	// Legacy adoption: issues synced before phase labels existed are only
	// identifiable by their summary.
	byMarker, err := ctx.Client.ListIssues(forge.IssueFilter{State: "all", Labels: []string{LabelMarker}})
	if err != nil {
		return "", Wrap(err)
	}
	for _, iss := range byMarker {
		if iss.Title == title && HasMarker(iss) {
			return iss.Key, nil
		}
	}
	return "", nil
}

// hasTaskLabel reports whether a card carries any dross/task: identity label.
func hasTaskLabel(labels []string) bool {
	for _, l := range labels {
		if strings.HasPrefix(l, forge.IdentityTaskPrefix) {
			return true
		}
	}
	return false
}

func SyncPhase(ctx *Ctx, phaseID, status string, doClose bool) error {
	dir := phase.Dir(ctx.Root, phaseID)
	spec, err := phase.LoadSpec(filepath.Join(dir, "spec.toml"))
	if err != nil {
		return fmt.Errorf("load spec for %s: %w", phaseID, err)
	}
	// plan.toml may not exist yet (sync at spec time) — tolerate it.
	plan, _ := phase.LoadPlan(filepath.Join(dir, "plan.toml"))

	if status == "" {
		status = DerivePhaseStatus(plan)
	}

	title := fmt.Sprintf("%s — %s", phaseID, spec.Phase.Title)
	body := RenderPhaseBody(phaseID, spec, plan)
	labels := []string{LabelMarker, PhaseLabel(phaseID), StatusLabel(status)}

	// Assign to the milestone if the phase declares one and it's syncable.
	// IssueInput.Milestone is the forge int id; the board stores it as a string.
	milestoneID := 0
	if spec.Phase.Milestone != "" {
		id, err := EnsureMilestoneLink(ctx, spec.Phase.Milestone)
		if err != nil {
			return err
		}
		milestoneID, _ = strconv.Atoi(id) // "" / non-numeric (youtrack entity) → 0, unassigned
	}

	key, err := ResolvePhaseIssue(ctx, phaseID, title)
	if err != nil {
		return err
	}
	created := key == ""
	if created {
		iss, err := ctx.Client.CreateIssue(forge.IssueInput{
			Title:     title,
			Body:      body,
			Labels:    labels,
			Milestone: milestoneID,
		})
		if err != nil {
			return Wrap(err)
		}
		ctx.Board.SetPhase(phaseID, iss.Key)
		key = iss.Key
	} else {
		patch := forge.IssuePatch{Title: &title, Body: &body, Labels: &labels}
		if milestoneID > 0 {
			patch.Milestone = &milestoneID
		}
		// The close is NOT folded into the patch: it goes through
		// closeBoardIssue below, so the created and updated edges take one
		// path. Splitting them is what let the close-on-create branch regress
		// unnoticed.
		if _, err := ctx.Client.UpdateIssue(key, patch); err != nil {
			return Wrap(err)
		}
		// Re-record: the key may have come from the tracker rather than the
		// cache (an adopted or re-resolved issue), and board.json has to catch
		// up or the next run pays for the lookup again.
		ctx.Board.SetPhase(phaseID, key)
	}
	// YouTrack tracks lifecycle on the State custom field (not a status label),
	// mapped via the default map overridden by [board].state_map. An unmapped
	// state warns and skips inside SetState without failing the sync.
	if yt, ok := ctx.Client.(*forge.YouTrackClient); ok && status != "" {
		if err := yt.SetState(key, status, ctx.Proj.Board.StateMap); err != nil {
			return Wrap(err)
		}
	}
	// Jira tracks lifecycle by moving the issue through a workflow transition,
	// mapped via the default map overridden by [board].state_map. An unmapped
	// state (or a target with no available transition) warns and skips inside
	// SetState without failing the sync.
	if jr, ok := ctx.Client.(*forge.JiraClient); ok && status != "" {
		if err := jr.SetState(key, status, ctx.Proj.Board.StateMap); err != nil {
			return Wrap(err)
		}
	}
	// Azure Boards moves System.State to the first state of the status's
	// category in the item type's own order, [board].state_map overriding by
	// name. A state the type lacks, or a rule rejection, warns and skips
	// inside SetState without failing the sync.
	if ado, ok := ctx.Client.(*forge.AzureBoardsClient); ok && status != "" {
		if err := ado.SetState(key, status, ctx.Proj.Board.StateMap); err != nil {
			return Wrap(err)
		}
	}
	// One close path for both edges — created-then-closed and updated-then-
	// closed. It errors rather than warns, so nothing prints "(closed)" for an
	// issue that is still open.
	if doClose {
		if err := CloseIssue(ctx, key, status); err != nil {
			return err
		}
	}
	if err := ctx.Board.Save(ctx.BoardPath); err != nil {
		return err
	}

	state := status
	if doClose {
		state = "closed"
	}
	fmt.Fprintf(ctx.out(), "phase %s -> board %s (%s)\n", phaseID, key, state)
	return nil
}

// closeBoardIssue resolves an issue on the board and reports failure rather
// than assuming success.
//
// The lenient warn-and-continue that SetState uses is right for a status label
// — a cosmetic loss — and wrong here. `--close` is the caller asserting the
// work is done; if the tracker did not record that, printing "(closed)" turns
// a failed write into a false claim about the state of the work, which is what
// c-5 exists to stop. So an unmapped status is an error, not a warning.
// The verdict a close is verified against differs by backend, and the branch
// below is by EXCLUSION — not YouTrack, not Jira, not Azure Boards — never a
// three-name allowlist of the flat boards. GitHub is a fourth flat board:
// github.go's toIssue
// populates State and never Resolved, so an allowlist would either demand
// Resolved of it and strand every card, or refuse it by name.
func CloseIssue(ctx *Ctx, key, status string) error {
	if status == "" {
		status = "complete"
	}
	if yt, ok := ctx.Client.(*forge.YouTrackClient); ok {
		// CloseIssueAs writes the mapped state and verifies the read-back.
		return Wrap(yt.CloseIssueAs(key, status, ctx.Proj.Board.StateMap))
	}
	if jr, ok := ctx.Client.(*forge.JiraClient); ok {
		// Jira closes by workflow transition, so the mapped write goes through
		// its existing SetState. SetState is deliberately lenient — an unmapped
		// status or an unavailable transition warns and returns nil — which is
		// right for a status label and wrong for a close, so the read-back
		// below is what makes this path honest.
		if err := jr.SetState(key, status, ctx.Proj.Board.StateMap); err != nil {
			return Wrap(err)
		}
		return verifyClosed(ctx, key, status, func(iss *forge.Issue) bool { return iss.Resolved })
	}
	if ado, ok := ctx.Client.(*forge.AzureBoardsClient); ok {
		// CloseIssueAs refuses a status whose state is not Completed-category
		// before its own write (a --status sync may already have moved the
		// item through SetState), then re-reads the item and errors naming it
		// unless it reads Completed. The generic close below would ignore the
		// state_map and close every type the same way.
		return Wrap(ado.CloseIssueAs(key, status, ctx.Proj.Board.StateMap))
	}
	// Every other provider: a plain close, verified on the open/closed state
	// their toIssue populates. Requiring Resolved here would strand every flat
	// board's cards, which the flat_board_close decision forbids; refusing them
	// by name would strand them just as thoroughly.
	if err := ctx.Client.CloseIssue(key); err != nil {
		return Wrap(err)
	}
	return verifyClosed(ctx, key, status, func(iss *forge.Issue) bool { return iss.State == "closed" })
}

// verifyClosed re-reads an issue and fails unless the tracker's own verdict
// agrees it is done. Without it a write the workflow silently refused is
// indistinguishable from a close, and dross prints "(closed)" over an issue the
// tracker still holds open.
func verifyClosed(ctx *Ctx, key, status string, done func(*forge.Issue) bool) error {
	iss, err := ctx.Client.GetIssue(key)
	if err != nil {
		return Wrap(fmt.Errorf("close %s: read back: %w", key, err))
	}
	if iss == nil || !done(iss) {
		return fmt.Errorf("close %s: wrote the close for status %q but the issue still reads unresolved — the workflow may not allow that transition", key, status)
	}
	return nil
}

// derivePhaseStatus maps plan progress onto a lifecycle label. Its return
// values are the emitted half of configenum.LifecycleStatuses — the other half
// comes from the `--status <literal>` calls in assets/prompts/*.md.
func DerivePhaseStatus(plan *phase.Plan) string {
	if plan == nil || len(plan.Task) == 0 {
		return StatusPlanned
	}
	_, inProgress, done, failed := plan.Summary()
	if done == 0 && inProgress == 0 && failed == 0 {
		return StatusPlanned
	}
	return StatusInProgress
}

// renderPhaseBody builds the issue body: a header, then a task checklist
// mirroring plan.toml task statuses, then a "managed by dross" footer.
func RenderPhaseBody(phaseID string, spec *phase.Spec, plan *phase.Plan) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("**Phase:** `%s`", phaseID))
	if spec.Phase.Milestone != "" {
		b.WriteString(fmt.Sprintf("  ·  **Milestone:** %s", spec.Phase.Milestone))
	}
	b.WriteString("\n")

	if len(spec.Criteria) > 0 {
		b.WriteString("\n### Acceptance criteria\n")
		for _, cr := range spec.Criteria {
			b.WriteString(fmt.Sprintf("- %s\n", cr.Text))
		}
	}

	if plan != nil && len(plan.Task) > 0 {
		b.WriteString("\n### Tasks\n")
		for _, t := range plan.Task {
			box := " "
			if t.Status == phase.StatusDone {
				box = "x"
			}
			b.WriteString(fmt.Sprintf("- [%s] %s — %s\n", box, t.ID, t.Title))
		}
	}

	b.WriteString("\n---\n_Tracked by dross. Edit the phase artefacts under ")
	b.WriteString(fmt.Sprintf("`.dross/phases/%s/`, not this issue body._\n", phaseID))
	return b.String()
}
