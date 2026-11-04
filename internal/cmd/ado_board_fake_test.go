package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/BurntSushi/toml"
)

// A stateful Azure Boards fake for the command-level proofs
// (azure-boards-backend t-20). It answers only the REST surface dross's client
// uses, under /<org>/<project>/_apis/, and it refuses what Azure DevOps
// refuses — a second parent, a work-item body that is not a JSON Patch, a
// state the type lacks, a batch over 200 ids — so a command that passes
// against it is not just agreeing with the client's own assumptions. It also
// keeps what the service keeps for itself: the reverse end of every link,
// the revision, a normalized tag set. Where it is stricter than the service it
// says so.

const (
	adoFakeOrg         = "acme"
	adoFakeProject     = "Fabrikam"
	adoFakeProjectGUID = "6ce954b1-ce1f-45d1-b94d-e6bf2464ba2c"
	adoFakeBatchMax    = 200
	adoFakeRequirement = "Product Backlog Item"
	adoFakePAT         = "secret" // what pointBoardAtADO puts in MOCK_TOKEN

	adoRelParent  = "System.LinkTypes.Hierarchy-Reverse"
	adoRelChild   = "System.LinkTypes.Hierarchy-Forward"
	adoRelRelated = "System.LinkTypes.Related"
)

type adoFakeState struct{ name, category string }

// adoFakeScrum is the Scrum process: each type's states in its own order.
var adoFakeScrum = map[string][]adoFakeState{
	"Epic":                 {{"New", "Proposed"}, {"In Progress", "InProgress"}, {"Done", "Completed"}, {"Removed", "Removed"}},
	"Product Backlog Item": {{"New", "Proposed"}, {"Approved", "Proposed"}, {"Committed", "InProgress"}, {"Done", "Completed"}, {"Removed", "Removed"}},
	"Bug":                  {{"New", "Proposed"}, {"Approved", "Proposed"}, {"Committed", "InProgress"}, {"Done", "Completed"}, {"Removed", "Removed"}},
	"Task":                 {{"To Do", "Proposed"}, {"In Progress", "InProgress"}, {"Done", "Completed"}, {"Removed", "Removed"}},
}

// adoFakeType is the process's spelling of a type name; Azure DevOps matches
// type names without regard to case.
func adoFakeType(name string) (string, bool) {
	for n := range adoFakeScrum {
		if strings.EqualFold(n, strings.TrimSpace(name)) {
			return n, true
		}
	}
	return "", false
}

type adoFakeRel struct {
	rel    string
	target int
}

type adoFakeItem struct {
	typ, state, title, desc, tags, area, iteration string
	rev                                            int
	rels                                           []adoFakeRel
}

func (it *adoFakeItem) clone() *adoFakeItem {
	cp := *it
	cp.rels = append([]adoFakeRel(nil), it.rels...)
	return &cp
}

// adoFakeReq is one request as the fake saw it, and the status it answered.
type adoFakeReq struct {
	method, path string
	body         []byte
	status       int
}

type adoFake struct {
	t   *testing.T
	srv *httptest.Server

	mu          sync.Mutex
	items       map[int]*adoFakeItem
	next        int
	log         []adoFakeReq
	refuseState bool // every System.State write is a rule error (TF401320)
	dropState   bool // every System.State write is accepted (200) and not recorded
	wiqlStatus  int  // when set, every WIQL is answered with this status
}

func newADOFake(t *testing.T) *adoFake {
	t.Helper()
	f := &adoFake{t: t, items: map[int]*adoFakeItem{}, next: 100}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// baseURL is the [board].base_url naming the fake's organization.
func (f *adoFake) baseURL() string { return f.srv.URL + "/" + adoFakeOrg }

// projectAPI is the path every request must sit under.
func (f *adoFake) projectAPI() string { return "/" + adoFakeOrg + "/" + adoFakeProject + "/_apis" }

// seed files a work item the way a person would, outside dross.
func (f *adoFake) seed(id int, typ, state, tags string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[id] = &adoFakeItem{typ: typ, state: state, title: fmt.Sprintf("%s %d", typ, id), tags: adoFakeTags(tags), rev: 1}
}

func (f *adoFake) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	status, body := f.answer(r, raw)
	f.log = append(f.log, adoFakeReq{method: r.Method, path: r.URL.EscapedPath(), body: raw, status: status})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	b, _ := json.Marshal(body)
	_, _ = w.Write(b)
}

func adoFakeRefuse(status int, msg string) (int, any) {
	return status, map[string]any{"message": msg}
}

func mediaType(r *http.Request) string {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return mt
}

func (f *adoFake) answer(r *http.Request, raw []byte) (int, any) {
	p, ok := strings.CutPrefix(r.URL.EscapedPath(), f.projectAPI())
	if !ok {
		f.t.Errorf("azure boards fake: a request outside %s: %s %s", f.projectAPI(), r.Method, r.URL.Path)
		return adoFakeRefuse(http.StatusNotFound, "TF200016: The following project does not exist")
	}
	// A PAT goes as Basic with an empty user; anything else is a 401 (the
	// client asks for one rather than a sign-in redirect).
	if r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte(":"+adoFakePAT)) {
		return adoFakeRefuse(http.StatusUnauthorized, "TF400813: The user is not authorized to access this resource.")
	}
	if r.URL.Query().Get("api-version") == "" {
		return adoFakeRefuse(http.StatusBadRequest, "No api-version was supplied for the request.")
	}
	// Relations come back only when asked for, on a read and on a write.
	expand := strings.EqualFold(r.URL.Query().Get("$expand"), "relations") || strings.EqualFold(r.URL.Query().Get("$expand"), "all")
	switch {
	case r.Method == http.MethodGet && p == "/work/backlogconfiguration":
		req := map[string]string{"name": adoFakeRequirement}
		return http.StatusOK, map[string]any{"requirementBacklog": map[string]any{"workItemTypes": []any{req}, "defaultWorkItemType": req}}

	case r.Method == http.MethodGet && p == "/wit/workitemtypes":
		names := make([]string, 0, len(adoFakeScrum))
		for n := range adoFakeScrum {
			names = append(names, n)
		}
		sort.Strings(names)
		vals := []map[string]string{}
		for _, n := range names {
			vals = append(vals, map[string]string{"name": n})
		}
		return http.StatusOK, map[string]any{"count": len(vals), "value": vals}

	case r.Method == http.MethodGet && strings.HasPrefix(p, "/wit/workitemtypes/") && strings.HasSuffix(p, "/states"):
		name, _ := url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(p, "/wit/workitemtypes/"), "/states"))
		typ, ok := adoFakeType(name)
		if !ok {
			return adoFakeRefuse(http.StatusNotFound, "VS402323: Work item type "+name+" does not exist.")
		}
		vals := []map[string]string{}
		for _, s := range adoFakeScrum[typ] {
			vals = append(vals, map[string]string{"name": s.name, "category": s.category})
		}
		return http.StatusOK, map[string]any{"count": len(vals), "value": vals}

	case r.Method == http.MethodPost && strings.HasPrefix(p, "/wit/workitems/$"):
		if mediaType(r) != "application/json-patch+json" {
			return adoFakeRefuse(http.StatusUnsupportedMediaType, "The request indicated a Content-Type of \""+r.Header.Get("Content-Type")+"\"; work item writes take application/json-patch+json.")
		}
		name, _ := url.PathUnescape(strings.TrimPrefix(p, "/wit/workitems/$"))
		typ, ok := adoFakeType(name)
		if !ok {
			return adoFakeRefuse(http.StatusNotFound, "VS402323: Work item type "+name+" does not exist.")
		}
		ops, err := adoFakeOps(raw)
		if err != nil {
			return adoFakeRefuse(http.StatusBadRequest, err.Error())
		}
		it := &adoFakeItem{typ: typ, state: adoFakeScrum[typ][0].name, rev: 1}
		if status, msg := f.apply(0, it, ops); status != 0 {
			return adoFakeRefuse(status, msg)
		}
		if strings.TrimSpace(it.title) == "" {
			return adoFakeRefuse(http.StatusBadRequest, "TF401320: Rule Error for field Title. Error code: Required, InvalidEmpty.")
		}
		f.next++
		f.items[f.next] = it
		f.mirror(f.next, nil, it.rels)
		return http.StatusOK, f.itemJSON(f.next, it, expand)

	case strings.HasPrefix(p, "/wit/workitems/") && (r.Method == http.MethodGet || r.Method == http.MethodPatch):
		id, err := strconv.Atoi(strings.TrimPrefix(p, "/wit/workitems/"))
		it, ok := f.items[id]
		if err != nil || !ok {
			return adoFakeRefuse(http.StatusNotFound, "TF401232: Work item "+strings.TrimPrefix(p, "/wit/workitems/")+" does not exist, or you do not have permissions to read it.")
		}
		if r.Method == http.MethodGet {
			return http.StatusOK, f.itemJSON(id, it, expand)
		}
		if mediaType(r) != "application/json-patch+json" {
			return adoFakeRefuse(http.StatusUnsupportedMediaType, "The request indicated a Content-Type of \""+r.Header.Get("Content-Type")+"\"; work item writes take application/json-patch+json.")
		}
		ops, err := adoFakeOps(raw)
		if err != nil {
			return adoFakeRefuse(http.StatusBadRequest, err.Error())
		}
		// Applied to a copy: a refused patch leaves the item exactly as it
		// was, as the service does — no op of it lands.
		cp := it.clone()
		if status, msg := f.apply(id, cp, ops); status != 0 {
			return adoFakeRefuse(status, msg)
		}
		cp.rev++
		f.items[id] = cp
		f.mirror(id, it.rels, cp.rels)
		return http.StatusOK, f.itemJSON(id, cp, expand)

	case r.Method == http.MethodPost && p == "/wit/wiql":
		if mediaType(r) != "application/json" {
			return adoFakeRefuse(http.StatusUnsupportedMediaType, "a WIQL query is application/json")
		}
		if f.wiqlStatus != 0 {
			return adoFakeRefuse(f.wiqlStatus, http.StatusText(f.wiqlStatus))
		}
		var body struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return adoFakeRefuse(http.StatusBadRequest, "VS402336: the query is missing")
		}
		ids, err := f.query(body.Query)
		if err != nil {
			return adoFakeRefuse(http.StatusBadRequest, err.Error())
		}
		refs := []map[string]any{}
		for _, id := range ids {
			refs = append(refs, map[string]any{"id": id, "url": f.itemAPIURL(id)})
		}
		return http.StatusOK, map[string]any{"queryType": "flat", "workItems": refs}

	case r.Method == http.MethodPost && p == "/wit/workitemsbatch":
		if mediaType(r) != "application/json" {
			return adoFakeRefuse(http.StatusUnsupportedMediaType, "a batch read is application/json")
		}
		var body struct {
			IDs         []int  `json:"ids"`
			Expand      string `json:"$expand"`
			ErrorPolicy string `json:"errorPolicy"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || len(body.IDs) == 0 {
			return adoFakeRefuse(http.StatusBadRequest, "VS402337: a batch names at least one work item")
		}
		if len(body.IDs) > adoFakeBatchMax {
			return adoFakeRefuse(http.StatusBadRequest, fmt.Sprintf("VS402337: a batch reads at most %d work items; this one names %d", adoFakeBatchMax, len(body.IDs)))
		}
		vals := []any{}
		for _, id := range body.IDs {
			it, ok := f.items[id]
			switch {
			case ok:
				vals = append(vals, f.itemJSON(id, it, strings.EqualFold(body.Expand, "relations") || strings.EqualFold(body.Expand, "all")))
			case strings.EqualFold(body.ErrorPolicy, "omit"):
				vals = append(vals, nil)
			default:
				return adoFakeRefuse(http.StatusNotFound, fmt.Sprintf("TF401232: Work item %d does not exist", id))
			}
		}
		return http.StatusOK, map[string]any{"count": len(vals), "value": vals}
	}
	f.t.Errorf("azure boards fake: unexpected %s %s", r.Method, r.URL.Path)
	return adoFakeRefuse(http.StatusNotFound, "no such endpoint")
}

type adoFakeOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

func adoFakeOps(raw []byte) ([]adoFakeOp, error) {
	var ops []adoFakeOp
	if err := json.Unmarshal(raw, &ops); err != nil || len(ops) == 0 {
		return nil, fmt.Errorf("VS403068: the body is not a JSON Patch document")
	}
	return ops, nil
}

// apply runs ops against it (a copy, for an update) and answers the refusal,
// if any, as a status and message; 0 is success.
func (f *adoFake) apply(id int, it *adoFakeItem, ops []adoFakeOp) (int, string) {
	for _, op := range ops {
		switch {
		case op.Op == "test" && op.Path == "/rev":
			// A stale revision fails the whole patch with 412, as the live
			// smoke (t-21) read it.
			if n, ok := op.Value.(float64); !ok || int(n) != it.rev {
				return http.StatusPreconditionFailed, fmt.Sprintf("VS403351: Test Operation for path /rev failed, value %d was not equal to test value %v.", it.rev, op.Value)
			}
		case (op.Op == "add" || op.Op == "replace") && strings.HasPrefix(op.Path, "/fields/"):
			field := strings.TrimPrefix(op.Path, "/fields/")
			v, ok := op.Value.(string)
			if !ok {
				return http.StatusBadRequest, "TF401320: Rule Error for field " + field + ". Error code: InvalidType."
			}
			switch field {
			case "System.Title":
				it.title = v
			case "System.Description":
				it.desc = v
			case "System.Tags":
				// As the live smoke (t-21) found: add merges into the set the
				// item has, replace replaces it — "" clears it.
				if op.Op == "add" {
					v = it.tags + ";" + v
				}
				it.tags = adoFakeTags(v)
			case "System.AreaPath":
				it.area = v
			case "System.IterationPath":
				it.iteration = v
			case "System.State":
				if f.refuseState {
					return http.StatusBadRequest, "TF401320: Rule Error for field State. Error code: Required, HasValues, LimitedToValues, AllowsOldValue, InvalidEmpty."
				}
				name, ok := adoFakeStateOf(it.typ, v)
				if !ok {
					return http.StatusBadRequest, fmt.Sprintf("TF401320: Rule Error for field State. Error code: LimitedToValues. %q is not a state of %s.", v, it.typ)
				}
				if !f.dropState {
					it.state = name
				}
			default:
				return http.StatusBadRequest, "TF51535: Cannot find field " + field + "."
			}
		case op.Op == "add" && op.Path == "/relations/-":
			val, _ := op.Value.(map[string]any)
			rel, _ := val["rel"].(string)
			raw, _ := val["url"].(string)
			target := adoFakeTarget(raw)
			if _, ok := f.items[target]; !ok {
				return http.StatusBadRequest, fmt.Sprintf("TF201063: the link target %q is not a work item", raw)
			}
			if target == id {
				return http.StatusBadRequest, "TF201036: a work item cannot be linked to itself"
			}
			switch {
			case strings.EqualFold(rel, adoRelParent):
				rel = adoRelParent
			case strings.EqualFold(rel, adoRelRelated):
				rel = adoRelRelated
			default:
				// The service takes other link types; dross writes none, so
				// one arriving here is a fault worth seeing.
				f.t.Errorf("azure boards fake: a %q link, which dross never writes", rel)
				return http.StatusBadRequest, "the fake does not model link type " + rel
			}
			for _, have := range it.rels {
				if have.rel == rel && have.target == target {
					return http.StatusBadRequest, "TF201035: Relation already exists."
				}
			}
			it.rels = append(it.rels, adoFakeRel{rel, target})
		case op.Op == "remove" && strings.HasPrefix(op.Path, "/relations/"):
			i, err := strconv.Atoi(strings.TrimPrefix(op.Path, "/relations/"))
			if err != nil || i < 0 || i >= len(it.rels) {
				return http.StatusBadRequest, "VS403069: no relation at " + op.Path
			}
			it.rels = append(it.rels[:i], it.rels[i+1:]...)
		default:
			return http.StatusBadRequest, fmt.Sprintf("VS403068: the fake does not model op %q on %s", op.Op, op.Path)
		}
	}
	parents := 0
	for _, r := range it.rels {
		if r.rel == adoRelParent {
			parents++
		}
	}
	if parents > 1 {
		return http.StatusBadRequest, "TF201036: You cannot add a Parent link to this work item because a work item can have only one Parent link."
	}
	return 0, ""
}

// mirror keeps the far end of every link the way the service does: a parent
// gets a Hierarchy-Forward to its child, and Related is symmetric.
func (f *adoFake) mirror(id int, before, after []adoFakeRel) {
	in := func(rels []adoFakeRel, r adoFakeRel) bool {
		for _, x := range rels {
			if x == r {
				return true
			}
		}
		return false
	}
	reverse := func(r adoFakeRel) adoFakeRel {
		back := adoFakeRel{rel: adoRelRelated, target: id}
		if r.rel == adoRelParent {
			back.rel = adoRelChild
		}
		return back
	}
	for _, r := range after {
		if !in(before, r) {
			if other := f.items[r.target]; other != nil && !in(other.rels, reverse(r)) {
				other.rels = append(other.rels, reverse(r))
				other.rev++
			}
		}
	}
	for _, r := range before {
		if !in(after, r) {
			if other := f.items[r.target]; other != nil {
				for i, x := range other.rels {
					if x == reverse(r) {
						other.rels = append(other.rels[:i], other.rels[i+1:]...)
						other.rev++
						break
					}
				}
			}
		}
	}
}

// adoFakeStateOf is typ's spelling of state, if typ has it.
func adoFakeStateOf(typ, state string) (string, bool) {
	for _, s := range adoFakeScrum[typ] {
		if strings.EqualFold(s.name, strings.TrimSpace(state)) {
			return s.name, true
		}
	}
	return "", false
}

// adoFakeTags is the tag set as the service stores it: trimmed, de-duplicated
// without regard to case, sorted, "; "-joined.
func adoFakeTags(raw string) string {
	seen := map[string]bool{}
	var tags []string
	for _, t := range strings.Split(raw, ";") {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		tags = append(tags, t)
	}
	sort.Slice(tags, func(i, j int) bool { return strings.ToLower(tags[i]) < strings.ToLower(tags[j]) })
	return strings.Join(tags, "; ")
}

// adoFakeTarget is the work-item id a relation url ends in, or 0.
func adoFakeTarget(raw string) int {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	p := strings.TrimRight(u.Path, "/")
	id, _ := strconv.Atoi(p[strings.LastIndex(p, "/")+1:])
	return id
}

// itemAPIURL is a work item's url in the form the service hands back: under
// the project's GUID, not its name — never the form dross wrote.
func (f *adoFake) itemAPIURL(id int) string {
	return f.srv.URL + "/" + adoFakeOrg + "/" + adoFakeProjectGUID + "/_apis/wit/workItems/" + strconv.Itoa(id)
}

// itemJSON is the item as the service answers it; relations only when the
// request asked for them with $expand.
func (f *adoFake) itemJSON(id int, it *adoFakeItem, withRelations bool) map[string]any {
	fields := map[string]any{
		"System.Id": id, "System.TeamProject": adoFakeProject, "System.WorkItemType": it.typ,
		"System.State": it.state, "System.Title": it.title,
		"System.AreaPath": adoFakeOr(it.area, adoFakeProject), "System.IterationPath": adoFakeOr(it.iteration, adoFakeProject),
	}
	// The service leaves an empty field out of the answer altogether.
	if it.tags != "" {
		fields["System.Tags"] = it.tags
	}
	if it.desc != "" {
		fields["System.Description"] = it.desc
	}
	out := map[string]any{"id": id, "rev": it.rev, "fields": fields, "url": f.itemAPIURL(id)}
	if withRelations && len(it.rels) > 0 {
		rels := []map[string]any{}
		for _, r := range it.rels {
			rels = append(rels, map[string]any{"rel": r.rel, "url": f.itemAPIURL(r.target), "attributes": map[string]any{"isLocked": false}})
		}
		out["relations"] = rels
	}
	return out
}

func adoFakeOr(v, dflt string) string {
	if v == "" {
		return dflt
	}
	return v
}

// --- WIQL: the subset dross sends, evaluated, and nothing else ---

// query answers a flat WIQL query. It understands SELECT [System.Id] FROM
// WorkItems WHERE … ORDER BY …, with AND, OR, parentheses, and three
// conditions: [System.TeamProject] = @project, [System.WorkItemType] = '…'
// and [System.Tags] CONTAINS '…' — which matches a whole tag, never part of
// one. Anything else is refused, not guessed at.
//
// Stricter than the service: a query not ANDed with [System.TeamProject] =
// @project is refused. Azure DevOps would answer it across the whole
// collection; refusing makes a client that forgot the scope fail loudly here.
func (f *adoFake) query(q string) ([]int, error) {
	const head = "SELECT [System.Id] FROM WorkItems WHERE "
	if len(q) < len(head) || !strings.EqualFold(q[:len(head)], head) {
		return nil, fmt.Errorf("VS402337: the fake answers only %q queries: %q", head+"…", q)
	}
	where := q[len(head):]
	if i := strings.Index(strings.ToUpper(where), " ORDER BY "); i >= 0 {
		where = where[:i]
	}
	toks, err := wiqlLex(where)
	if err != nil {
		return nil, err
	}
	p := &wiqlParser{toks: toks}
	root, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.i != len(p.toks) {
		return nil, fmt.Errorf("TF51006: unexpected %q in the WHERE clause", p.toks[p.i].text)
	}
	if !wiqlScoped(root) {
		return nil, fmt.Errorf("the query is not scoped by [System.TeamProject] = @project: %q", q)
	}
	ids := make([]int, 0, len(f.items))
	for id := range f.items {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	out := []int{}
	for _, id := range ids {
		if root.eval(f.items[id]) {
			out = append(out, id)
		}
	}
	return out, nil
}

type wiqlTok struct {
	kind byte // '(' ')' '=' 'f' field, 's' string literal, 'w' word
	text string
}

func wiqlLex(s string) ([]wiqlTok, error) {
	var out []wiqlTok
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == ' ':
			i++
		case c == '(' || c == ')' || c == '=':
			out = append(out, wiqlTok{c, string(c)})
			i++
		case c == '[':
			j := strings.IndexByte(s[i:], ']')
			if j < 0 {
				return nil, fmt.Errorf("TF51006: unclosed field reference in %q", s)
			}
			out = append(out, wiqlTok{'f', s[i+1 : i+j]})
			i += j + 1
		case c == '\'':
			var b strings.Builder
			j := i + 1
			for ; ; j++ {
				if j >= len(s) {
					return nil, fmt.Errorf("TF51006: unclosed string in %q", s)
				}
				if s[j] == '\'' {
					if j+1 < len(s) && s[j+1] == '\'' {
						b.WriteByte('\'')
						j++
						continue
					}
					break
				}
				b.WriteByte(s[j])
			}
			out = append(out, wiqlTok{'s', b.String()})
			i = j + 1
		default:
			j := i
			for j < len(s) && !strings.ContainsRune(" ()='[", rune(s[j])) {
				j++
			}
			out = append(out, wiqlTok{'w', s[i:j]})
			i = j
		}
	}
	return out, nil
}

type wiqlNode struct {
	op    string // "AND", "OR", or "" for a condition
	kids  []*wiqlNode
	field string
	cmp   string
	value wiqlTok
}

type wiqlParser struct {
	toks []wiqlTok
	i    int
}

func (p *wiqlParser) word(w string) bool {
	if p.i < len(p.toks) && p.toks[p.i].kind == 'w' && strings.EqualFold(p.toks[p.i].text, w) {
		p.i++
		return true
	}
	return false
}

func (p *wiqlParser) or() (*wiqlNode, error) {
	n, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.word("OR") {
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		n = &wiqlNode{op: "OR", kids: []*wiqlNode{n, r}}
	}
	return n, nil
}

func (p *wiqlParser) and() (*wiqlNode, error) {
	n, err := p.cond()
	if err != nil {
		return nil, err
	}
	for p.word("AND") {
		r, err := p.cond()
		if err != nil {
			return nil, err
		}
		n = &wiqlNode{op: "AND", kids: []*wiqlNode{n, r}}
	}
	return n, nil
}

func (p *wiqlParser) cond() (*wiqlNode, error) {
	if p.i < len(p.toks) && p.toks[p.i].kind == '(' {
		p.i++
		n, err := p.or()
		if err != nil {
			return nil, err
		}
		if p.i >= len(p.toks) || p.toks[p.i].kind != ')' {
			return nil, fmt.Errorf("TF51006: unbalanced parentheses")
		}
		p.i++
		return n, nil
	}
	if p.i+3 > len(p.toks) {
		return nil, fmt.Errorf("TF51006: an incomplete condition")
	}
	field, cmp, value := p.toks[p.i], p.toks[p.i+1], p.toks[p.i+2]
	p.i += 3
	if field.kind != 'f' {
		return nil, fmt.Errorf("TF51006: expected a field reference, got %q", field.text)
	}
	n := &wiqlNode{field: field.text, cmp: strings.ToUpper(cmp.text), value: value}
	switch {
	case strings.EqualFold(n.field, "System.TeamProject") && n.cmp == "=" && value.kind == 'w' && strings.EqualFold(value.text, "@project"):
	case strings.EqualFold(n.field, "System.WorkItemType") && n.cmp == "=" && value.kind == 's':
	case strings.EqualFold(n.field, "System.Tags") && n.cmp == "CONTAINS" && value.kind == 's':
	default:
		return nil, fmt.Errorf("TF51005: the fake does not model [%s] %s %s", n.field, cmp.text, value.text)
	}
	return n, nil
}

func (n *wiqlNode) eval(it *adoFakeItem) bool {
	switch n.op {
	case "AND":
		return n.kids[0].eval(it) && n.kids[1].eval(it)
	case "OR":
		return n.kids[0].eval(it) || n.kids[1].eval(it)
	}
	switch {
	case strings.EqualFold(n.field, "System.TeamProject"):
		return true // the fake holds one project
	case strings.EqualFold(n.field, "System.WorkItemType"):
		return strings.EqualFold(it.typ, n.value.text)
	default: // System.Tags CONTAINS: a whole tag, without regard to case
		for _, tag := range strings.Split(it.tags, ";") {
			if strings.EqualFold(strings.TrimSpace(tag), n.value.text) {
				return true
			}
		}
		return false
	}
}

// wiqlScoped reports whether root ANDs [System.TeamProject] = @project at its
// top level.
func wiqlScoped(root *wiqlNode) bool {
	if root.op == "AND" {
		return wiqlScoped(root.kids[0]) || wiqlScoped(root.kids[1])
	}
	return root.op == "" && strings.EqualFold(root.field, "System.TeamProject")
}

// --- reading the fake back ---

// projectRequests counts the requests that reached the project API.
func (f *adoFake) projectRequests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.log {
		if strings.HasPrefix(r.path, f.projectAPI()+"/") {
			n++
		}
	}
	return n
}

// idsOf is every work item of typ, in id order.
func (f *adoFake) idsOf(typ string) []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int
	for id, it := range f.items {
		if it.typ == typ {
			out = append(out, id)
		}
	}
	sort.Ints(out)
	return out
}

func (f *adoFake) stateOf(id int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if it := f.items[id]; it != nil {
		return it.state
	}
	return ""
}

// snapshot is everything about id a refused patch must leave alone.
func (f *adoFake) snapshot(id int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	it := f.items[id]
	return fmt.Sprintf("rev %d state %q tags %q rels %v", it.rev, it.state, it.tags, it.rels)
}

// stateWrites is the work items an accepted PATCH moved to state, in the
// order the PATCHes landed.
func (f *adoFake) stateWrites(state string) []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int
	for _, r := range f.log {
		if r.method != http.MethodPatch || r.status != http.StatusOK {
			continue
		}
		var ops []adoFakeOp
		_ = json.Unmarshal(r.body, &ops)
		for _, op := range ops {
			if op.Path == "/fields/System.State" && op.Value == state {
				id, _ := strconv.Atoi(r.path[strings.LastIndex(r.path, "/")+1:])
				out = append(out, id)
			}
		}
	}
	return out
}

// writesSince is every PATCH, and every create POST to /wit/workitems/$…,
// logged from mark on.
func (f *adoFake) writesSince(mark int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.log[mark:] {
		if r.method == http.MethodPatch || (r.method == http.MethodPost && strings.Contains(r.path, "/_apis/wit/workitems/$")) {
			out = append(out, r.method+" "+r.path)
		}
	}
	return out
}

func (f *adoFake) logLen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.log)
}

// --- the repo around it ---

// allowADOHost adds the fake's address to this machine's allow_hosts, next to
// whatever is there already.
func allowADOHost(t *testing.T, dir string, f *adoFake) {
	t.Helper()
	var local struct {
		AllowHosts string `toml:"allow_hosts"`
	}
	if _, err := toml.DecodeFile(filepath.Join(dir, ".dross", LocalFile), &local); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	hosts := f.srv.Listener.Addr().String()
	if local.AllowHosts != "" {
		hosts = local.AllowHosts + "," + hosts
	}
	if err := runCmd(t, Local(), "set", "allow_hosts", hosts); err != nil {
		t.Fatal(err)
	}
}

// pointBoardAtADO makes [board] an enabled azure-devops board on f, project
// Fabrikam, its PAT in MOCK_TOKEN.
func pointBoardAtADO(t *testing.T, dir string, f *adoFake) {
	t.Helper()
	t.Setenv("MOCK_TOKEN", "secret")
	allowADOHost(t, dir, f)
	for _, kv := range [][2]string{
		{"board.provider", "azure-devops"},
		{"board.base_url", f.baseURL()},
		{"board.auth_env", "MOCK_TOKEN"},
		{"board.project", adoFakeProject},
		{"board.enabled", "true"},
	} {
		mustRunSet(t, kv[0], kv[1])
	}
}

// adoBoardRepo is a dross repo whose [board] is f and whose [remote] is
// another host: a recorder that fails the test on any request. It returns the
// repo and a count of what reached the remote.
func adoBoardRepo(t *testing.T, f *adoFake) (string, func() int) {
	t.Helper()
	var mu sync.Mutex
	hits := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		t.Errorf("a board command reached the [remote] host: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(remote.Close)
	dir := t.TempDir()
	gitInit(t, dir, remote.URL+"/me/p.git")
	chdir(t, dir)
	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("init: %v", err)
	}
	mustRunSet(t, "remote.url", remote.URL+"/me/p.git")
	pointBoardAtADO(t, dir, f)
	return dir, func() int {
		mu.Lock()
		defer mu.Unlock()
		return hits
	}
}

// --- the fake's own proof ---

// send is one raw request to the fake's project API.
func (f *adoFake) send(t *testing.T, method, path, contentType string, body any) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, f.srv.URL+f.projectAPI()+path+"?api-version=7.1", rdr)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(":"+adoFakePAT)))
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// TestADOFakeRefusesWhatADORefuses: the fake turns away each request Azure
// DevOps turns away, so the command tests on it cannot pass by its leniency.
func TestADOFakeRefusesWhatADORefuses(t *testing.T) {
	f := newADOFake(t)
	const patchCT, jsonCT = "application/json-patch+json", "application/json"
	title := func(s string) map[string]any {
		return map[string]any{"op": "add", "path": "/fields/System.Title", "value": s}
	}
	parentOf := func(id int) map[string]any {
		return map[string]any{"op": "add", "path": "/relations/-", "value": map[string]any{
			"rel": adoRelParent, "url": f.baseURL() + "/_apis/wit/workItems/" + strconv.Itoa(id)}}
	}
	create := func(typ string, ops ...map[string]any) int {
		t.Helper()
		st, body := f.send(t, http.MethodPost, "/wit/workitems/$"+url.PathEscape(typ), patchCT, append([]map[string]any{title(typ)}, ops...))
		if st != http.StatusOK {
			t.Fatalf("create %s = %d %s", typ, st, body)
		}
		var w struct {
			ID int `json:"id"`
		}
		_ = json.Unmarshal([]byte(body), &w)
		return w.ID
	}
	epicA, epicB := create("Epic"), create("Epic")
	child := create("Task", parentOf(epicA))
	item := "/wit/workitems/" + strconv.Itoa(child)

	t.Run("a second parent", func(t *testing.T) {
		before := f.snapshot(child)
		st, body := f.send(t, http.MethodPatch, item, patchCT, []any{parentOf(epicB)})
		if st != http.StatusBadRequest || !strings.Contains(body, "TF201036") {
			t.Errorf("a second Hierarchy-Reverse = %d %s; want 400 TF201036", st, body)
		}
		if after := f.snapshot(child); after != before {
			t.Errorf("the refused patch changed the item: %s → %s", before, after)
		}
		if got := f.snapshot(epicA); !strings.Contains(got, fmt.Sprintf("{%s %d}", adoRelChild, child)) {
			t.Errorf("the parent does not hold the far end of the link: %s", got)
		}
	})
	t.Run("a body that is not a JSON Patch", func(t *testing.T) {
		ops := []any{title("x")}
		if st, _ := f.send(t, http.MethodPost, "/wit/workitems/$Task", jsonCT, ops); st != http.StatusUnsupportedMediaType {
			t.Errorf("a create sent as application/json = %d, want 415", st)
		}
		if st, _ := f.send(t, http.MethodPatch, item, jsonCT, ops); st != http.StatusUnsupportedMediaType {
			t.Errorf("an update sent as application/json = %d, want 415", st)
		}
	})
	t.Run("a WIQL not scoped to the project", func(t *testing.T) {
		const q = "SELECT [System.Id] FROM WorkItems WHERE [System.WorkItemType] = 'Task'"
		if st, body := f.send(t, http.MethodPost, "/wit/wiql", jsonCT, map[string]string{"query": q}); st != http.StatusBadRequest {
			t.Errorf("an unscoped WIQL = %d %s, want 400", st, body)
		}
		scoped := strings.Replace(q, "WHERE ", "WHERE [System.TeamProject] = @project AND ", 1)
		if st, body := f.send(t, http.MethodPost, "/wit/wiql", jsonCT, map[string]string{"query": scoped}); st != http.StatusOK {
			t.Errorf("the scoped WIQL = %d %s, want 200", st, body)
		}
	})
	t.Run("a state the type lacks", func(t *testing.T) {
		before := f.snapshot(child)
		state := func(s string) []any {
			return []any{map[string]any{"op": "add", "path": "/fields/System.State", "value": s}}
		}
		if st, body := f.send(t, http.MethodPatch, item, patchCT, state("Closed")); st != http.StatusBadRequest {
			t.Errorf("a Scrum Task moved to Closed = %d %s, want 400", st, body)
		}
		if after := f.snapshot(child); after != before {
			t.Errorf("the refused state changed the item: %s → %s", before, after)
		}
		if st, body := f.send(t, http.MethodPatch, item, patchCT, state("In Progress")); st != http.StatusOK {
			t.Errorf("a Scrum Task moved to In Progress = %d %s, want 200", st, body)
		}
	})
	t.Run("a batch over 200", func(t *testing.T) {
		ids := make([]int, adoFakeBatchMax+1)
		for i := range ids {
			ids[i] = child
		}
		if st, _ := f.send(t, http.MethodPost, "/wit/workitemsbatch", jsonCT, map[string]any{"ids": ids}); st != http.StatusBadRequest {
			t.Errorf("a batch of 201 ids = %d, want 400", st)
		}
		if st, _ := f.send(t, http.MethodPost, "/wit/workitemsbatch", jsonCT, map[string]any{"ids": ids[:adoFakeBatchMax]}); st != http.StatusOK {
			t.Errorf("a batch of 200 ids = %d, want 200", st)
		}
	})
	t.Run("no PAT", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, f.srv.URL+f.projectAPI()+item+"?api-version=7.1", nil)
		resp, err := f.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("a read with no Authorization = %d, want 401", resp.StatusCode)
		}
	})
	t.Run("relations only when expanded", func(t *testing.T) {
		if _, body := f.send(t, http.MethodGet, item, "", nil); strings.Contains(body, `"relations"`) {
			t.Errorf("a read without $expand carried relations: %s", body)
		}
		req, _ := http.NewRequest(http.MethodGet, f.srv.URL+f.projectAPI()+item+"?api-version=7.1&$expand=relations", nil)
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(":"+adoFakePAT)))
		resp, err := f.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(raw), adoRelParent) {
			t.Errorf("a read with $expand=relations lacks the parent link: %s", raw)
		}
	})
	t.Run("CONTAINS matches a whole tag", func(t *testing.T) {
		tags := func(s string) map[string]any {
			return map[string]any{"op": "add", "path": "/fields/System.Tags", "value": s}
		}
		one := create("Task", tags("dross; dross/task:p/t-1"))
		create("Task", tags("dross; dross/task:p/t-10"))
		q := "SELECT [System.Id] FROM WorkItems WHERE [System.TeamProject] = @project AND [System.Tags] CONTAINS 'dross/task:p/t-1' ORDER BY [System.Id]"
		st, body := f.send(t, http.MethodPost, "/wit/wiql", jsonCT, map[string]string{"query": q})
		var res struct {
			WorkItems []struct{ ID int } `json:"workItems"`
		}
		_ = json.Unmarshal([]byte(body), &res)
		if st != http.StatusOK || len(res.WorkItems) != 1 || res.WorkItems[0].ID != one {
			t.Errorf("CONTAINS 'dross/task:p/t-1' = %d %s; want only work item %d", st, body, one)
		}
	})
}
