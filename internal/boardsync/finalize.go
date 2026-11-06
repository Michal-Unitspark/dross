package boardsync

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Rivil/dross/internal/changes"
	"github.com/Rivil/dross/internal/forge"
	"github.com/Rivil/dross/internal/phase"
)

// FinalizePhase brings a completed phase's board to its terminal state: every
// task card at task-complete and closed, then the phase card at complete and
// closed. It is the one finalizer — `dross phase complete` runs it, `dross
// issue phase finalize` re-runs it, and reap's catch-up creates missing cards
// through it — so the three cannot drift apart.
//
// It refuses unless the phase's changes.json reads complete: a card closed on a
// guess is the false claim the board exists to avoid.
//
// It is idempotent. A card already carrying its terminal status label AND
// reading done is left alone, so a second run makes only reads. A missing card
// is created and closed. The milestone link comes from board.json's cache only
// — never EnsureMilestoneLink, which would open a milestone epic for a
// historical phase. A card that fails is recorded and the rest still finalize;
// the error names every card that did not get there.
func FinalizePhase(ctx *Ctx, phaseID string) error {
	ch, err := changes.Load(changes.FilePath(ctx.Root, phaseID), phaseID)
	if err != nil {
		return fmt.Errorf("finalize %s: %w", phaseID, err)
	}
	if !ch.Complete() {
		status := ch.Status
		if status == "" {
			status = "not started"
		}
		return fmt.Errorf("finalize %s: the phase is %s, not complete — its board is finalized only once changes.json reads complete", phaseID, status)
	}

	dir := phase.Dir(ctx.Root, phaseID)
	spec, err := phase.LoadSpec(filepath.Join(dir, "spec.toml"))
	if err != nil {
		return fmt.Errorf("finalize %s: load spec: %w", phaseID, err)
	}
	plan, _ := phase.LoadPlan(filepath.Join(dir, "plan.toml")) // absent is fine: label-found cards still close

	warn := &runWarnings{}
	var failed []string
	closed := 0

	// The phase card is resolved (or created) first, so task card bodies can
	// name it, and closed last. A failure here is recorded like any other card:
	// the task cards need it only for their body text, so they still finalize.
	title := fmt.Sprintf("%s — %s", phaseID, spec.Phase.Title)
	phaseLabels := []string{LabelMarker, PhaseLabel(phaseID), StatusLabel(StatusComplete)}
	phaseKey, created := "", false
	if key, err := ResolvePhaseIssue(ctx, phaseID, title); err != nil {
		failed = append(failed, fmt.Sprintf("phase card: %v", err))
	} else if key != "" {
		phaseKey = key
	} else if iss, err := ctx.Client.CreateIssue(forge.IssueInput{
		Title:     title,
		Body:      RenderPhaseBody(phaseID, spec, plan),
		Labels:    phaseLabels,
		Milestone: cachedMilestone(ctx, spec.Phase.Milestone),
	}); err != nil {
		failed = append(failed, fmt.Sprintf("phase card: create: %v", Wrap(err)))
	} else {
		phaseKey, created = iss.Key, true
	}
	if phaseKey != "" {
		ctx.Board.SetPhase(phaseID, phaseKey)
	}

	// Plan tasks first, in plan order, then any card the phase's labels say is
	// one of its tasks that the plan no longer lists.
	seen := map[string]bool{}
	if plan != nil {
		for _, t := range plan.Task {
			seen[t.ID] = true
			wrote, err := finalizePlanTask(ctx, phaseID, phaseKey, t, warn)
			if err != nil {
				failed = append(failed, fmt.Sprintf("task %s: %v", t.ID, err))
				continue
			}
			if wrote {
				closed++
			}
		}
	}
	strays, err := labelledTaskCards(ctx, phaseID)
	if err != nil {
		failed = append(failed, fmt.Sprintf("list task cards: %v", err))
	}
	for _, sc := range strays {
		if seen[sc.taskID] {
			continue
		}
		seen[sc.taskID] = true
		labels := []string{LabelMarker, PhaseLabel(phaseID), TaskLabel(phaseID, sc.taskID), StatusLabel(StatusTaskComplete)}
		wrote, err := finalizeCard(ctx, sc.key, labels, StatusTaskComplete, warn)
		if err != nil {
			failed = append(failed, fmt.Sprintf("task %s: %v", sc.taskID, err))
			continue
		}
		ctx.Board.SetTask(phaseID, sc.taskID, sc.key)
		if wrote {
			closed++
		}
	}

	// The phase card last: it closes even when a task card would not, so one
	// stuck card does not hold the phase open too. The narration says what
	// happened to it — never "already complete" for a card that failed.
	phaseState := "NOT closed"
	switch {
	case phaseKey == "":
		// recorded above; there is no card to report on
	case created:
		if err := closeNew(ctx, phaseKey, StatusComplete, warn); err != nil {
			failed = append(failed, fmt.Sprintf("phase %s: %v", phaseKey, err))
		} else {
			phaseState = "created, closed"
		}
	default:
		if wrote, err := finalizeCard(ctx, phaseKey, phaseLabels, StatusComplete, warn); err != nil {
			failed = append(failed, fmt.Sprintf("phase %s: %v", phaseKey, err))
		} else if wrote {
			phaseState = "closed"
		} else {
			phaseState = "already complete"
		}
	}

	// Saved before the verdict, as SyncTasks does: the cards that did finalize
	// keep their links, so a re-run resumes rather than repeats.
	if err := ctx.Board.Save(ctx.BoardPath); err != nil {
		return err
	}
	card := phaseKey
	if card == "" {
		card = "no card"
	}
	fmt.Fprintf(ctx.out(), "phase %s -> board %s (%s); %d task card(s) closed\n", phaseID, card, phaseState, closed)
	if len(failed) > 0 {
		return fmt.Errorf("finalize %s: %d card(s) did not reach their terminal state:\n  %s", phaseID, len(failed), strings.Join(failed, "\n  "))
	}
	return nil
}

// finalizePlanTask resolves or creates one plan task's card and brings it to
// task-complete, closed. It reports whether it wrote anything.
func finalizePlanTask(ctx *Ctx, phaseID, parent string, t phase.Task, warn *runWarnings) (bool, error) {
	labels := []string{LabelMarker, PhaseLabel(phaseID), TaskLabel(phaseID, t.ID), StatusLabel(StatusTaskComplete)}
	key, err := resolveTaskIssue(ctx, phaseID, t.ID)
	if err != nil {
		return false, err
	}
	if key == "" {
		iss, err := ctx.Client.CreateIssue(forge.IssueInput{
			Title:  fmt.Sprintf("%s/%s — %s", phaseID, t.ID, t.Title),
			Body:   RenderTaskBody(phaseID, parent, t),
			Labels: labels,
		})
		if err != nil {
			return false, Wrap(err)
		}
		// The link is recorded before the close, so a refused close does not
		// cost the next run a label lookup.
		ctx.Board.SetTask(phaseID, t.ID, iss.Key)
		// A tracker with a real parent link (Azure Boards' Hierarchy-Reverse)
		// gets the card under its phase card, as SyncTasks would have put it:
		// a card created only at finalize is otherwise an orphan in every
		// hierarchy view. A failure warns once and does not stop the close.
		if pl, ok := ctx.Client.(forge.ParentLinker); ok && parent != "" {
			if err := pl.LinkParent(parent, iss.Key); err != nil {
				warn.once(&warn.noLink, "could not put task cards under %s (%v) — they carry the phase label", parent, err)
			}
		}
		if err := closeNew(ctx, iss.Key, StatusTaskComplete, warn); err != nil {
			return false, err
		}
		ctx.Board.SetTaskSynced(phaseID, t.ID, iss.Key, t.Status, StatusTaskComplete)
		return true, nil
	}
	ctx.Board.SetTask(phaseID, t.ID, key)
	wrote, err := finalizeCard(ctx, key, labels, StatusTaskComplete, warn)
	if err != nil {
		return false, err
	}
	ctx.Board.SetTaskSynced(phaseID, t.ID, key, t.Status, StatusTaskComplete)
	return wrote, nil
}

// finalizeCard brings an existing card to status and closes it, unless it is
// already there: its terminal status label present AND the tracker reading it
// done. Both are required — a card a human closed by hand still lacks the label
// the lifecycle reads, and a labelled card still open is not finished. Labels
// that are not dross/status: are kept. It reports whether it wrote anything.
func finalizeCard(ctx *Ctx, key string, want []string, status string, warn *runWarnings) (bool, error) {
	iss, err := ctx.Client.GetIssue(key)
	if err != nil {
		return false, Wrap(err)
	}
	if iss == nil {
		return false, fmt.Errorf("%s does not resolve", key)
	}
	if hasLabel(iss.Labels, StatusLabel(status)) && (iss.Resolved || iss.State == "closed") {
		return false, nil
	}
	labels := mergeLabels(iss.Labels, want)
	if _, err := ctx.Client.UpdateIssue(key, forge.IssuePatch{Labels: &labels}); err != nil {
		return false, Wrap(err)
	}
	if err := closeNew(ctx, key, status, warn); err != nil {
		return false, err
	}
	return true, nil
}

// closeNew drives the workflow field to status and closes the card, verified.
func closeNew(ctx *Ctx, key, status string, warn *runWarnings) error {
	if err := setBoardState(ctx, key, status, warn); err != nil {
		return err
	}
	return CloseIssue(ctx, key, status)
}

// mergeLabels keeps every label of have that is not a dross/status: label,
// then adds each of want not already present.
func mergeLabels(have, want []string) []string {
	out := []string{}
	for _, l := range have {
		if !strings.HasPrefix(l, StatusLabel("")) {
			out = append(out, l)
		}
	}
	for _, l := range want {
		if !hasLabel(out, l) {
			out = append(out, l)
		}
	}
	return out
}

// strayTask is a card the phase's labels name as one of its tasks.
type strayTask struct{ taskID, key string }

// labelledTaskCards lists the dross cards carrying dross/phase:<id> and a
// dross/task:<id>/ label — the phase's task cards as the tracker knows them,
// whether or not a plan still lists them. A card with the phase label and no
// task label is the phase card (or a duplicate of it) and is not returned.
func labelledTaskCards(ctx *Ctx, phaseID string) ([]strayTask, error) {
	found, err := ctx.Client.ListIssues(forge.IssueFilter{State: "all", Labels: []string{PhaseLabel(phaseID)}})
	if err != nil {
		return nil, Wrap(err)
	}
	prefix := TaskLabel(phaseID, "")
	byTask := map[string]string{}
	for _, iss := range found {
		if !HasMarker(iss) {
			continue
		}
		for _, l := range iss.Labels {
			if id, ok := strings.CutPrefix(l, prefix); ok && id != "" {
				if cur, seen := byTask[id]; !seen || iss.Key < cur {
					byTask[id] = iss.Key
				}
			}
		}
	}
	out := make([]strayTask, 0, len(byTask))
	for id, key := range byTask {
		out = append(out, strayTask{taskID: id, key: key})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].taskID < out[j].taskID })
	return out, nil
}

// cachedMilestone is the forge milestone id board.json already holds for
// version, or 0. It never creates one: finalizing a historical phase must not
// open an epic for a milestone that has long since closed.
func cachedMilestone(ctx *Ctx, version string) int {
	if version == "" {
		return 0
	}
	id, ok := ctx.Board.MilestoneID(version)
	if !ok {
		return 0
	}
	n, _ := strconv.Atoi(id) // a non-numeric id (a YouTrack epic) is not a forge milestone
	return n
}
