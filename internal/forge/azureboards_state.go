package forge

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// --- Azure Boards states, resolved by category ---
//
// A process names its states however it likes — Scrum's Committed, Agile's
// Active, Basic's Doing are all "being worked" — but every state carries one
// of five categories, and those are fixed. So dross maps each lifecycle status
// to a CATEGORY, never to a state name, and writes the first state of that
// category in the type's own order (locked state_resolution): no per-process
// tables, and an inherited process's renamed states resolve unchanged.
// [board].state_map still overrides by state name.

// defaultADOStateCategory maps each dross lifecycle status to the state
// category it lands in. A plain literal keyed by configenum.LifecycleStatuses,
// so the lifecycle guards in internal/boardsync read it like the Jira and
// YouTrack maps.
//
// uat and task-in-review are Resolved — distinct from in-progress, as c-3
// asks — on a type that has a Resolved state (Agile's User Story); a type
// without one (Scrum, Basic, Agile's Task) falls back to InProgress
// (adoCategoryFallback). Nothing resolves to Removed.
var defaultADOStateCategory = map[string]string{
	"planned":          "Proposed",
	"in-progress":      "InProgress",
	"task-in-progress": "InProgress",
	"uat":              "Resolved",
	"task-in-review":   "Resolved",
	"shipped":          "Completed",
	"complete":         "Completed",
	"task-complete":    "Completed",
}

// adoCategoryFallback is the category written when a type has no state of
// the one a status asks for.
var adoCategoryFallback = map[string]string{ADOCategoryResolved: ADOCategoryInProgress}

var _ StateWriter = (*AzureBoardsClient)(nil)

// resolveADOState picks the state status lands in among states (one type's,
// in API order). An override names a state, which must exist on the type; a
// blank override is unmapped. Otherwise the first state of the status's
// category — or its fallback — wins. Removed is never written by default.
func resolveADOState(status string, states []ADOState, override map[string]string) (ADOState, error) {
	if v, ok := override[status]; ok {
		name := strings.TrimSpace(v)
		if name == "" {
			return ADOState{}, fmt.Errorf("[board].state_map.%s is blank", status)
		}
		for _, s := range states {
			if strings.EqualFold(s.Name, name) {
				return s, nil
			}
		}
		return ADOState{}, fmt.Errorf("[board].state_map.%s = %q names no state the type has", status, name)
	}
	category, ok := defaultADOStateCategory[status]
	if !ok {
		return ADOState{}, fmt.Errorf("dross state %q has no Azure Boards mapping", status)
	}
	for _, want := range []string{category, adoCategoryFallback[category]} {
		if want == "" || want == ADOCategoryRemoved {
			continue
		}
		for _, s := range states {
			if s.Category == want {
				return s, nil
			}
		}
	}
	return ADOState{}, fmt.Errorf("the type has no %s-category state for %q", category, status)
}

// itemWithStates reads work item key and its type's states. Any failure here
// is a read that failed, never a mapping gap.
func (c *AzureBoardsClient) itemWithStates(key string) (adoWorkItem, []ADOState, error) {
	w, err := c.getWorkItem(key)
	if err != nil {
		return adoWorkItem{}, nil, err
	}
	states, err := c.States(w.typeName)
	if err != nil {
		return adoWorkItem{}, nil, fmt.Errorf("work item %d: %w", w.id, err)
	}
	return w, states, nil
}

// writeState PATCHes System.State on work item id, verbatim.
func (c *AzureBoardsClient) writeState(id int, state string) error {
	patch := adoJSONPatch{{Op: "add", Path: "/fields/System.State", Value: state}}
	return c.do(http.MethodPatch, c.projectAPI("/wit/workitems/"+strconv.Itoa(id)), patch, nil)
}

// adoRuleError is the code Azure Boards refuses a write with when the
// process's rules forbid it: a required field left empty, a transition the
// type does not allow.
const adoRuleError = "TF401320"

// ruleRejection reports whether err is Azure Boards refusing a write on a
// process rule — an HTTP 400 whose body carries TF401320. That is the one 400
// that means "no reachable state"; a 400 for anything else (a malformed patch,
// an unknown field) is a fault in the request.
func ruleRejection(err error) bool {
	var se *adoStatusError
	return errors.As(err, &se) && se.Status == http.StatusBadRequest && strings.Contains(se.body, adoRuleError)
}

// SetState moves a work item to the state a dross lifecycle status resolves
// to. A state board sync cannot reach is a cosmetic loss, not a failed sync:
// a type with no state for the status, an override naming a state the type
// lacks, or a 400 rule rejection (TF401320 — a required field, a disallowed
// transition) warns on stderr and returns nil, as Jira and YouTrack do. An
// item already in the target is left alone; any other failure — another 400
// included — is an error naming the item.
func (c *AzureBoardsClient) SetState(key, status string, override map[string]string) error {
	w, states, err := c.itemWithStates(key)
	if err != nil {
		return err
	}
	target, err := resolveADOState(status, states, override)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v — not moving work item %d (%s, %s) for %q\n", err, w.id, w.typeName, w.state, status)
		return nil
	}
	if strings.EqualFold(w.state, target.Name) {
		return nil
	}
	if err := c.writeState(w.id, target.Name); err != nil {
		if ruleRejection(err) {
			fmt.Fprintf(os.Stderr, "warning: Azure Boards refused moving work item %d to %q for %q — skipping: %v\n", w.id, target.Name, status, err)
			return nil
		}
		return fmt.Errorf("set work item %d to %q: %w", w.id, target.Name, err)
	}
	return nil
}

// CloseIssue closes a work item as "complete".
func (c *AzureBoardsClient) CloseIssue(key string) error {
	return c.CloseIssueAs(key, "complete", nil)
}

// CloseIssueAs closes a work item at the state status resolves to, and
// verifies it. A close must land in a Completed-category state — that is what
// GetIssue reads as closed — so a target in any other category (an override to
// a Resolved state, a blank override) is refused before anything is written.
// After the write the item is read again, and anything but a Completed state
// is an error naming the item: a write the process silently declined must not
// read as a close.
func (c *AzureBoardsClient) CloseIssueAs(key, status string, override map[string]string) error {
	w, states, err := c.itemWithStates(key)
	if err != nil {
		return err
	}
	target, err := resolveADOState(status, states, override)
	if err != nil {
		return fmt.Errorf("close work item %d as %q: %w", w.id, status, err)
	}
	if target.Category != ADOCategoryCompleted {
		return fmt.Errorf("close work item %d as %q: %q is a %s-category state, not Completed — it would read as still open; fix [board].state_map.%s",
			w.id, status, target.Name, target.Category, status)
	}
	if !strings.EqualFold(w.state, target.Name) {
		if err := c.writeState(w.id, target.Name); err != nil {
			return fmt.Errorf("close work item %d at %q: %w", w.id, target.Name, err)
		}
	}
	now, err := c.getWorkItem(strconv.Itoa(w.id))
	if err != nil {
		return fmt.Errorf("close work item %d: read back: %w", w.id, err)
	}
	category, err := c.stateCategory(now.typeName, now.state)
	if err != nil {
		return fmt.Errorf("close work item %d: read back: %w", w.id, err)
	}
	if category != ADOCategoryCompleted {
		return fmt.Errorf("close work item %d: wrote %q but it reads %q (%s), not a Completed state", w.id, target.Name, now.state, orUnknown(category))
	}
	return nil
}

// SetStateRaw writes state verbatim — no state map, no category — and
// verifies the read-back. Stricter than SetState: an undo that wrote nothing
// and reported success would leave the board as the sweep left it, so a 400
// is an error here, never a warning.
func (c *AzureBoardsClient) SetStateRaw(key, state string) error {
	id, err := adoWorkItemID(key)
	if err != nil {
		return err
	}
	if err := c.writeState(id, state); err != nil {
		return fmt.Errorf("set state on work item %d to %q: %w", id, state, err)
	}
	now, err := c.getWorkItem(strconv.Itoa(id))
	if err != nil {
		return fmt.Errorf("set state on work item %d: read back: %w", id, err)
	}
	if !strings.EqualFold(now.state, state) {
		return fmt.Errorf("set state on work item %d: wrote %q but it still reads %q", id, state, now.state)
	}
	return nil
}

// orUnknown names an empty category.
func orUnknown(category string) string {
	if category == "" {
		return "no known category"
	}
	return category
}
