package forge

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// --- Azure Boards process metadata and work-item reads ---
//
// Everything a sync needs to know about the project's process is read from
// the project itself, never assumed per process: which type is the
// requirement backlog's (locked hierarchy — User Story on Agile, Product
// Backlog Item on Scrum, Issue on Basic, or whatever an inherited process
// renamed it to), which types exist, and each type's states with their
// categories (locked state_resolution). Each read is made lazily, at most once
// per client, and cached.

// ADO state categories, as the states endpoint names them.
const (
	ADOCategoryProposed   = "Proposed"
	ADOCategoryInProgress = "InProgress"
	ADOCategoryResolved   = "Resolved"
	ADOCategoryCompleted  = "Completed"
	ADOCategoryRemoved    = "Removed"
)

// ADOState is one state of a work-item type and its category.
type ADOState struct {
	Name     string
	Category string // one of the ADOCategory* values
}

// ADOPathKind names a classification tree: areas or iterations.
type ADOPathKind string

const (
	ADOAreaPath      ADOPathKind = "area"
	ADOIterationPath ADOPathKind = "iteration"
)

// adoMetaCache is a client's process metadata. The zero value is empty; each
// field fills on its first successful read. A failed read is not cached, so
// the next call asks again.
type adoMetaCache struct {
	requirementType string
	typeNames       []string              // nil until read
	states          map[string][]ADOState // keyed by lowercase type name
}

// RequirementType is the default work-item type of the project's requirement
// backlog, read from <project>/_apis/work/backlogconfiguration. It is never
// guessed: a backlog configuration without a requirement backlog is an error
// naming it, not a "User Story" fallback that would 400 on a Scrum project.
func (c *AzureBoardsClient) RequirementType() (string, error) {
	if c.meta.requirementType != "" {
		return c.meta.requirementType, nil
	}
	var cfg struct {
		RequirementBacklog *struct {
			DefaultWorkItemType *struct {
				Name string `json:"name"`
			} `json:"defaultWorkItemType"`
			WorkItemTypes []struct {
				Name string `json:"name"`
			} `json:"workItemTypes"`
		} `json:"requirementBacklog"`
	}
	if err := c.do(http.MethodGet, c.projectAPI("/work/backlogconfiguration"), nil, &cfg); err != nil {
		return "", fmt.Errorf("read the backlog configuration: %w", err)
	}
	name := ""
	if rb := cfg.RequirementBacklog; rb != nil {
		if rb.DefaultWorkItemType != nil {
			name = strings.TrimSpace(rb.DefaultWorkItemType.Name)
		}
		if name == "" && len(rb.WorkItemTypes) > 0 {
			name = strings.TrimSpace(rb.WorkItemTypes[0].Name)
		}
	}
	if name == "" {
		return "", errors.New("read the backlog configuration: it names no requirement backlog type")
	}
	c.meta.requirementType = name
	return name, nil
}

// TypeNames lists the project's work-item type names, in API order.
func (c *AzureBoardsClient) TypeNames() ([]string, error) {
	if c.meta.typeNames != nil {
		return append([]string(nil), c.meta.typeNames...), nil
	}
	var page struct {
		Value *[]struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	if err := c.do(http.MethodGet, c.projectAPI("/wit/workitemtypes"), nil, &page); err != nil {
		return nil, fmt.Errorf("read the work item types: %w", err)
	}
	if page.Value == nil {
		return nil, errors.New("read the work item types: the answer is not the list it promises")
	}
	names := make([]string, 0, len(*page.Value))
	for _, v := range *page.Value {
		if n := strings.TrimSpace(v.Name); n != "" {
			names = append(names, n)
		}
	}
	c.meta.typeNames = names
	return append([]string(nil), names...), nil
}

// HasType reports whether the project has a work-item type named name,
// case-insensitively, and returns its own spelling.
func (c *AzureBoardsClient) HasType(name string) (string, bool, error) {
	names, err := c.TypeNames()
	if err != nil {
		return "", false, err
	}
	for _, n := range names {
		if strings.EqualFold(n, strings.TrimSpace(name)) {
			return n, true, nil
		}
	}
	return "", false, nil
}

// States lists typeName's states with their categories, in API order (the
// order the process declares them, which is the board's column order).
func (c *AzureBoardsClient) States(typeName string) ([]ADOState, error) {
	key := strings.ToLower(strings.TrimSpace(typeName))
	if key == "" {
		return nil, errors.New("read work item states: no work item type")
	}
	if cached, ok := c.meta.states[key]; ok {
		return append([]ADOState(nil), cached...), nil
	}
	var page struct {
		Value *[]struct {
			Name     string `json:"name"`
			Category string `json:"category"`
		} `json:"value"`
	}
	endpoint := c.projectAPI("/wit/workitemtypes/" + url.PathEscape(strings.TrimSpace(typeName)) + "/states")
	if err := c.do(http.MethodGet, endpoint, nil, &page); err != nil {
		return nil, fmt.Errorf("read the %s states: %w", typeName, err)
	}
	if page.Value == nil {
		return nil, fmt.Errorf("read the %s states: the answer is not the list it promises", typeName)
	}
	states := make([]ADOState, 0, len(*page.Value))
	for _, v := range *page.Value {
		states = append(states, ADOState{Name: v.Name, Category: v.Category})
	}
	if c.meta.states == nil {
		c.meta.states = map[string][]ADOState{}
	}
	c.meta.states[key] = states
	return append([]ADOState(nil), states...), nil
}

// stateCategory is the category of state on typeName, or "" when the type has
// no such state.
func (c *AzureBoardsClient) stateCategory(typeName, state string) (string, error) {
	states, err := c.States(typeName)
	if err != nil {
		return "", err
	}
	for _, s := range states {
		if strings.EqualFold(s.Name, state) {
			return s.Category, nil
		}
	}
	return "", nil
}

// PathExists reports whether an area or iteration path exists in the project,
// via its classification node. The path is made absolute in the project first
// (normalizeADOPath), so `Web` and `Fabrikam\Web` probe the same node. A 404
// is "missing"; any other failure — a 500, a sign-in page — is an error, never
// "missing".
func (c *AzureBoardsClient) PathExists(kind ADOPathKind, path string) (bool, error) {
	tree := ""
	switch kind {
	case ADOAreaPath:
		tree = "Areas"
	case ADOIterationPath:
		tree = "Iterations"
	default:
		return false, fmt.Errorf("unknown classification kind %q (want area or iteration)", kind)
	}
	full := normalizeADOPath(c.project, path)
	if full == "" {
		return false, fmt.Errorf("no %s path to check", kind)
	}
	suffix := "/wit/classificationnodes/" + tree
	if rel := full[len(c.project):]; rel != "" {
		for _, seg := range strings.Split(strings.Trim(rel, `\`), `\`) {
			seg = strings.TrimSpace(seg)
			switch seg {
			case "":
				continue
			case ".", "..":
				// A dot segment would address the parent node once the server
				// normalizes the URL, so a bad path would read as present.
				return false, fmt.Errorf("%s path %s has a %q segment", kind, full, seg)
			}
			suffix += "/" + url.PathEscape(seg)
		}
	}
	err := c.do(http.MethodGet, c.projectAPI(suffix), nil, nil)
	var status *adoStatusError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &status) && status.Status == http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("check %s path %s: %w", kind, full, err)
}

// adoWorkItem is a work item as dross reads it.
type adoWorkItem struct {
	id          int
	rev         int
	typeName    string
	title       string
	description string
	tags        string
	state       string
	relations   []adoRelation
}

// adoRelation is one entry of a work item's relations.
type adoRelation struct {
	rel string // e.g. System.LinkTypes.Hierarchy-Reverse
	url string // the related work item's API URL
}

// parseWorkItem decodes one work item from the workitems API.
func parseWorkItem(raw []byte) (adoWorkItem, error) {
	var w struct {
		ID     int `json:"id"`
		Rev    int `json:"rev"`
		Fields struct {
			Type        string `json:"System.WorkItemType"`
			Title       string `json:"System.Title"`
			Description string `json:"System.Description"`
			Tags        string `json:"System.Tags"`
			State       string `json:"System.State"`
		} `json:"fields"`
		Relations []struct {
			Rel string `json:"rel"`
			URL string `json:"url"`
		} `json:"relations"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return adoWorkItem{}, fmt.Errorf("decode work item: %w", err)
	}
	if w.ID <= 0 {
		return adoWorkItem{}, errors.New("decode work item: it carries no id")
	}
	out := adoWorkItem{
		id: w.ID, rev: w.Rev, typeName: w.Fields.Type, title: w.Fields.Title,
		description: w.Fields.Description, tags: w.Fields.Tags, state: w.Fields.State,
	}
	for _, r := range w.Relations {
		out.relations = append(out.relations, adoRelation{rel: r.Rel, url: r.URL})
	}
	return out, nil
}

// adoWorkItemID parses a work-item key. Azure Boards ids are positive
// integers; anything else — a YouTrack-style "PROJ-7", a path — is refused
// before it can reach a URL.
func adoWorkItemID(key string) (int, error) {
	id, err := strconv.Atoi(strings.TrimSpace(key))
	if err != nil || id <= 0 || strconv.Itoa(id) != strings.TrimSpace(key) {
		return 0, fmt.Errorf("azure boards: %q is not a work item id", key)
	}
	return id, nil
}

// getWorkItem GETs one work item with its relations. It asks for every field
// (no fields param — that cannot be combined with $expand).
func (c *AzureBoardsClient) getWorkItem(key string) (adoWorkItem, error) {
	id, err := adoWorkItemID(key)
	if err != nil {
		return adoWorkItem{}, err
	}
	var raw json.RawMessage
	endpoint := c.projectAPI("/wit/workitems/"+strconv.Itoa(id)) + "?$expand=relations"
	if err := c.do(http.MethodGet, endpoint, nil, &raw); err != nil {
		return adoWorkItem{}, fmt.Errorf("read work item %d: %w", id, err)
	}
	return parseWorkItem(raw)
}

// GetIssue reads one work item. State is "closed" and Resolved true exactly
// when its state's category is Completed — read from the type's states, never
// from the name — so a Removed item, or one parked in a Resolved-category
// state, reads open and unresolved. WorkflowState is the state's own name.
func (c *AzureBoardsClient) GetIssue(key string) (*Issue, error) {
	w, err := c.getWorkItem(key)
	if err != nil {
		return nil, err
	}
	return c.toIssue(w)
}

// toIssue maps a work item onto the shared Issue shape.
func (c *AzureBoardsClient) toIssue(w adoWorkItem) (*Issue, error) {
	category, err := c.stateCategory(w.typeName, w.state)
	if err != nil {
		return nil, fmt.Errorf("read work item %d: %w", w.id, err)
	}
	resolved := category == ADOCategoryCompleted
	state := "open"
	if resolved {
		state = "closed"
	}
	return &Issue{
		Number:        w.id,
		Key:           strconv.Itoa(w.id),
		Title:         w.title,
		Body:          adoBodyText(w.description),
		State:         state,
		Labels:        adoTagLabels(w.tags),
		URL:           c.workItemURL(w.id),
		WorkflowState: w.state,
		Resolved:      resolved,
	}, nil
}

// workItemURL is the work item's browser URL.
func (c *AzureBoardsClient) workItemURL(id int) string {
	return c.baseURL + "/" + url.PathEscape(c.project) + "/_workitems/edit/" + strconv.Itoa(id)
}

// adoTagLabels splits System.Tags ("a; b; c") into labels. Azure Boards keeps
// a tag's casing as first typed and matches tags case-insensitively, so a tag
// in dross's own vocabulary — "dross" and anything under "dross/" — is
// lowercased: identity lookups (HasMarker, the dross/phase: tag) compare
// exactly, and "Dross" must still be the marker. Every other tag keeps its
// spelling.
func adoTagLabels(tags string) []string {
	var out []string
	for _, t := range strings.Split(tags, ";") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if lower := strings.ToLower(t); lower == "dross" || strings.HasPrefix(lower, "dross/") {
			t = lower
		}
		out = append(out, t)
	}
	return out
}

// adoBodyText turns a System.Description back into text: the line breaks
// dross writes as <br> become newlines and HTML entities are unescaped.
func adoBodyText(desc string) string {
	r := strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n")
	return html.UnescapeString(r.Replace(desc))
}
