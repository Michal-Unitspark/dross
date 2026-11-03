package forge

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/Rivil/dross/internal/secretscan"
)

// --- Azure Boards work items: create, update, list, and the milestone Epic ---
//
// The hierarchy is locked: a milestone is an Epic, a phase is the project's
// requirement backlog type (RequirementType — User Story, Product Backlog
// Item, Issue, or an inherited process's own), and a task is a Task. Labels
// are System.Tags; a tag never contains ';', which is the separator.

// adoTitleMax is System.Title's limit, in characters.
const adoTitleMax = 255

// adoBatchMax is the most ids one workitemsbatch read takes.
const adoBatchMax = 200

// adoTaskLabelPrefix marks a task's identity label: an item carrying it is a
// Task, anything else dross creates is the requirement type.
const adoTaskLabelPrefix = "dross/task:"

// adoMilestoneTagPrefix is the tag that names a milestone's Epic.
const adoMilestoneTagPrefix = "dross/milestone:"

var (
	_ BoardClient = (*AzureBoardsClient)(nil)
)

// adoTitle is title cut to System.Title's limit, by runes so a multi-byte
// character is never split.
func adoTitle(title string) string {
	if r := []rune(title); len(r) > adoTitleMax {
		return string(r[:adoTitleMax])
	}
	return title
}

// adoDescription is body as System.Description's HTML: escaped, with each
// newline a <br> (adoBodyText reads it back).
func adoDescription(body string) string {
	return strings.ReplaceAll(html.EscapeString(body), "\n", "<br>")
}

// adoTags joins labels into System.Tags, refusing a label that carries the
// separator: "a;b" would land as two tags and never match itself again.
func adoTags(labels []string) (string, error) {
	for _, l := range labels {
		if strings.Contains(l, ";") {
			return "", fmt.Errorf("azure boards: label %q contains ';', the tag separator — it cannot be a tag", l)
		}
	}
	return strings.Join(labels, "; "), nil
}

// isDrossLabel reports whether label is in dross's own vocabulary.
func isDrossLabel(label string) bool {
	l := strings.ToLower(strings.TrimSpace(label))
	return l == "dross" || strings.HasPrefix(l, "dross/")
}

// screenFields screens the text fields of a write before anything — a
// metadata read included — leaves: a credential in a body is refused with no
// request made, its location the field it rode in.
func screenFields(method string, fields map[string]string) error {
	return secretscan.ScanPayload(method+" workitems", fields)
}

// classificationOps are the area and iteration ops a create carries — only
// when configured, never an empty string.
func (c *AzureBoardsClient) classificationOps() adoJSONPatch {
	var ops adoJSONPatch
	if c.areaPath != "" {
		ops = append(ops, adoPatchOp{Op: "add", Path: "/fields/System.AreaPath", Value: normalizeADOPath(c.project, c.areaPath)})
	}
	if c.iterationPath != "" {
		ops = append(ops, adoPatchOp{Op: "add", Path: "/fields/System.IterationPath", Value: normalizeADOPath(c.project, c.iterationPath)})
	}
	return ops
}

// createWorkItem POSTs one work item of typeName — every type dross creates,
// Epic included, goes through here — with title, description, tags, the
// configured area and iteration, and a parent link when parent > 0.
func (c *AzureBoardsClient) createWorkItem(typeName, title, body string, labels []string, parent int) (adoWorkItem, error) {
	tags, err := adoTags(labels)
	if err != nil {
		return adoWorkItem{}, err
	}
	ops := adoJSONPatch{
		{Op: "add", Path: "/fields/System.Title", Value: adoTitle(title)},
		{Op: "add", Path: "/fields/System.Description", Value: adoDescription(body)},
	}
	if tags != "" {
		ops = append(ops, adoPatchOp{Op: "add", Path: "/fields/System.Tags", Value: tags})
	}
	ops = append(ops, c.classificationOps()...)
	if parent > 0 {
		ops = append(ops, adoPatchOp{Op: "add", Path: "/relations/-", Value: c.relationValue(adoRelParent, parent)})
	}
	var raw json.RawMessage
	if err := c.do(http.MethodPost, c.projectAPI("/wit/workitems/$"+url.PathEscape(typeName)), ops, &raw); err != nil {
		return adoWorkItem{}, fmt.Errorf("create %s: %w", typeName, err)
	}
	return parseWorkItem(raw)
}

// CreateIssue creates a work item: a Task when the labels carry a
// dross/task: identity, else the project's requirement backlog type. A
// Milestone > 0 is the milestone's Epic id, linked as the item's parent.
func (c *AzureBoardsClient) CreateIssue(in IssueInput) (*Issue, error) {
	if _, err := adoTags(in.Labels); err != nil {
		return nil, err
	}
	if err := screenFields("POST", map[string]string{"System.Title": in.Title, "System.Description": in.Body}); err != nil {
		return nil, err
	}
	typeName := "Task"
	isTask := false
	for _, l := range in.Labels {
		isTask = isTask || strings.HasPrefix(strings.ToLower(l), adoTaskLabelPrefix)
	}
	if !isTask {
		rt, err := c.RequirementType()
		if err != nil {
			return nil, err
		}
		typeName = rt
	}
	w, err := c.createWorkItem(typeName, in.Title, in.Body, in.Labels, in.Milestone)
	if err != nil {
		return nil, err
	}
	// A new item is open in its type's first state; no states read is needed
	// to say so, and none can then fail a create that already happened.
	return &Issue{
		Number: w.id, Key: strconv.Itoa(w.id), Title: w.title, Body: adoBodyText(w.description),
		State: "open", Labels: adoTagLabels(w.tags), URL: c.workItemURL(w.id), WorkflowState: w.state,
	}, nil
}

// UpdateIssue PATCHes only the fields patch sets. Labels replace the whole tag
// set. Area and iteration are never touched after create, Milestone is
// ignored (the parent link is LinkParent's), and a State patch is refused:
// states move through SetState and CloseIssueAs.
func (c *AzureBoardsClient) UpdateIssue(key string, patch IssuePatch) (*Issue, error) {
	id, err := adoWorkItemID(key)
	if err != nil {
		return nil, err
	}
	if patch.State != nil {
		return nil, fmt.Errorf("azure boards: update work item %d: a State patch is not supported — states move through SetState / CloseIssueAs", id)
	}
	fields := map[string]string{}
	var ops adoJSONPatch
	if patch.Title != nil {
		fields["System.Title"] = *patch.Title
		ops = append(ops, adoPatchOp{Op: "add", Path: "/fields/System.Title", Value: adoTitle(*patch.Title)})
	}
	if patch.Body != nil {
		fields["System.Description"] = *patch.Body
		ops = append(ops, adoPatchOp{Op: "add", Path: "/fields/System.Description", Value: adoDescription(*patch.Body)})
	}
	if patch.Labels != nil {
		tags, err := adoTags(*patch.Labels)
		if err != nil {
			return nil, err
		}
		ops = append(ops, adoPatchOp{Op: "add", Path: "/fields/System.Tags", Value: tags})
	}
	if err := screenFields("PATCH", fields); err != nil {
		return nil, err
	}
	if len(ops) == 0 {
		return c.GetIssue(key)
	}
	var raw json.RawMessage
	if err := c.do(http.MethodPatch, c.projectAPI("/wit/workitems/"+strconv.Itoa(id)), ops, &raw); err != nil {
		return nil, fmt.Errorf("update work item %d: %w", id, err)
	}
	w, err := parseWorkItem(raw)
	if err != nil {
		return nil, fmt.Errorf("update work item %d: %w", id, err)
	}
	return c.toIssue(w)
}

// wiqlLiteral quotes s as a WIQL string literal: ' is doubled, so a value can
// never close its literal and become query.
func wiqlLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// queryIDs runs a WIQL query scoped to the project and returns the ids it
// answers, in its order.
func (c *AzureBoardsClient) queryIDs(where string) ([]int, error) {
	q := "SELECT [System.Id] FROM WorkItems WHERE [System.TeamProject] = @project"
	if where != "" {
		q += " AND " + where
	}
	q += " ORDER BY [System.Id]"
	var res struct {
		WorkItems *[]struct {
			ID int `json:"id"`
		} `json:"workItems"`
	}
	if err := c.do(http.MethodPost, c.projectAPI("/wit/wiql"), map[string]string{"query": q}, &res); err != nil {
		return nil, fmt.Errorf("query work items: %w", err)
	}
	if res.WorkItems == nil {
		return nil, errors.New("query work items: the answer is not the list it promises")
	}
	ids := make([]int, 0, len(*res.WorkItems))
	for _, w := range *res.WorkItems {
		if w.ID > 0 {
			ids = append(ids, w.ID)
		}
	}
	return ids, nil
}

// readItems reads ids in batches of adoBatchMax, with relations. An id
// deleted between the query and the read comes back null and is skipped; any
// failed batch fails the whole read.
func (c *AzureBoardsClient) readItems(ids []int) ([]adoWorkItem, error) {
	var out []adoWorkItem
	for start := 0; start < len(ids); start += adoBatchMax {
		end := min(start+adoBatchMax, len(ids))
		body := map[string]any{"ids": ids[start:end], "$expand": "relations", "errorPolicy": "omit"}
		var page struct {
			Value *[]json.RawMessage `json:"value"`
		}
		if err := c.do(http.MethodPost, c.projectAPI("/wit/workitemsbatch"), body, &page); err != nil {
			return nil, fmt.Errorf("read work items: %w", err)
		}
		if page.Value == nil {
			return nil, errors.New("read work items: the answer is not the list it promises")
		}
		for _, raw := range *page.Value {
			if strings.TrimSpace(string(raw)) == "null" {
				continue
			}
			w, err := parseWorkItem(raw)
			if err != nil {
				return nil, fmt.Errorf("read work items: %w", err)
			}
			out = append(out, w)
		}
	}
	return out, nil
}

// labelClause is the WIQL condition for labels: each one OR'd as a tag
// CONTAINS, and — for a label that is not dross's own and names a type the
// project has — as that work-item type too (locked pull_match: a hand-filed
// Bug matches the bug label whether or not anyone tagged it).
func (c *AzureBoardsClient) labelClause(labels []string) (string, map[string]string, error) {
	var parts []string
	types := map[string]string{} // lowercase label -> the project's type spelling
	for _, l := range labels {
		parts = append(parts, "[System.Tags] CONTAINS "+wiqlLiteral(l))
		if isDrossLabel(l) {
			continue
		}
		spelling, ok, err := c.HasType(l)
		if err != nil {
			return "", nil, err
		}
		if ok {
			types[strings.ToLower(strings.TrimSpace(l))] = spelling
			parts = append(parts, "[System.WorkItemType] = "+wiqlLiteral(spelling))
		}
	}
	return "(" + strings.Join(parts, " OR ") + ")", types, nil
}

// matchesLabels is the exact post-filter CONTAINS needs: CONTAINS is a
// substring test, so dross/task:p/t-1 would otherwise match dross/task:p/t-10.
func matchesLabels(w adoWorkItem, labels []string, types map[string]string) bool {
	tags := adoTagLabels(w.tags)
	for _, l := range labels {
		for _, tag := range tags {
			if strings.EqualFold(tag, l) {
				return true
			}
		}
		if spelling, ok := types[strings.ToLower(strings.TrimSpace(l))]; ok && strings.EqualFold(w.typeName, spelling) {
			return true
		}
	}
	return false
}

// ListIssues lists the project's work items matching f. Labels match a tag
// exactly or, for a non-dross label naming a type, that type. State open
// drops Completed- and Removed-category items; closed keeps Completed only;
// all keeps everything. A failed query or read is an error, never an empty
// list.
func (c *AzureBoardsClient) ListIssues(f IssueFilter) ([]Issue, error) {
	where := ""
	var types map[string]string
	if len(f.Labels) > 0 {
		clause, t, err := c.labelClause(f.Labels)
		if err != nil {
			return nil, err
		}
		where, types = clause, t
	}
	ids, err := c.queryIDs(where)
	if err != nil {
		return nil, err
	}
	items, err := c.readItems(ids)
	if err != nil {
		return nil, err
	}
	out := []Issue{}
	for _, w := range items {
		if len(f.Labels) > 0 && !matchesLabels(w, f.Labels, types) {
			continue
		}
		category, err := c.stateCategory(w.typeName, w.state)
		if err != nil {
			return nil, err
		}
		if !adoStateWanted(f.State, category) {
			continue
		}
		iss, err := c.toIssue(w)
		if err != nil {
			return nil, err
		}
		out = append(out, *iss)
	}
	return out, nil
}

// adoStateWanted applies an IssueFilter state to a category.
func adoStateWanted(state, category string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "all":
		return true
	case "closed":
		return category == ADOCategoryCompleted
	default: // "open", and an unset state
		return category != ADOCategoryCompleted && category != ADOCategoryRemoved
	}
}

// EnsureMilestone is EnsureMilestoneEpic for the generic board path, which
// passes the version as the title.
func (c *AzureBoardsClient) EnsureMilestone(title, description string) (string, error) {
	return c.EnsureMilestoneEpic(title, "", description)
}

// EnsureMilestoneEpic finds the Epic tagged dross/milestone:<version>, or
// creates one titled "<version> — <title>" and tagged "dross;
// dross/milestone:<version>". Of several, the lowest id wins and the rest are
// named on stderr; a stale title is updated, an unchanged one left alone.
func (c *AzureBoardsClient) EnsureMilestoneEpic(version, title, description string) (string, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return "", errors.New("azure boards: a milestone Epic needs a version")
	}
	tag := adoMilestoneTagPrefix + version
	want := version
	if t := strings.TrimSpace(title); t != "" && t != version {
		want = version + " — " + t
	}
	if err := screenFields("POST", map[string]string{"System.Title": want, "System.Description": description}); err != nil {
		return "", err
	}
	ids, err := c.queryIDs("[System.WorkItemType] = 'Epic' AND [System.Tags] CONTAINS " + wiqlLiteral(tag))
	if err != nil {
		return "", fmt.Errorf("find the %s Epic: %w", version, err)
	}
	var found []adoWorkItem
	if len(ids) > 0 {
		items, err := c.readItems(ids)
		if err != nil {
			return "", fmt.Errorf("find the %s Epic: %w", version, err)
		}
		for _, w := range items {
			if strings.EqualFold(w.typeName, "Epic") && matchesLabels(w, []string{tag}, nil) {
				found = append(found, w)
			}
		}
	}
	if len(found) == 0 {
		w, err := c.createWorkItem("Epic", want, description, []string{"dross", tag}, 0)
		if err != nil {
			return "", err
		}
		return strconv.Itoa(w.id), nil
	}
	sort.Slice(found, func(i, j int) bool { return found[i].id < found[j].id })
	epic := found[0]
	if len(found) > 1 {
		var rest []string
		for _, w := range found[1:] {
			rest = append(rest, strconv.Itoa(w.id))
		}
		fmt.Fprintf(os.Stderr, "warning: %d Epics are tagged %s — using %d; %s duplicate it\n", len(found), tag, epic.id, strings.Join(rest, ", "))
	}
	if epic.title != adoTitle(want) {
		patch := adoJSONPatch{{Op: "add", Path: "/fields/System.Title", Value: adoTitle(want)}}
		if err := c.do(http.MethodPatch, c.projectAPI("/wit/workitems/"+strconv.Itoa(epic.id)), patch, nil); err != nil {
			return "", fmt.Errorf("retitle the %s Epic (work item %d): %w", version, epic.id, err)
		}
	}
	return strconv.Itoa(epic.id), nil
}
