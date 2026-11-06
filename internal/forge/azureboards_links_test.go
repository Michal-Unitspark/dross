package forge

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// adoLinkServer serves work items by id and records every PATCH body.
type adoLinkServer struct {
	*httptest.Server
	mu         sync.Mutex
	items      map[string]string // id -> work item JSON
	patchCode  int
	gets       int
	patches    []adoLinkPatch
	unexpected []string
}

type adoLinkPatch struct {
	id          string
	contentType string
	ops         []map[string]any
}

func newADOLinkServer(t *testing.T, items map[string]string) *adoLinkServer {
	t.Helper()
	s := &adoLinkServer{items: items, patchCode: http.StatusOK}
	s.Server, _ = adoBoardsServer(t, func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		id, ok := strings.CutPrefix(r.URL.Path, adoProjectAPI+"/wit/workitems/")
		if !ok {
			s.unexpected = append(s.unexpected, r.Method+" "+r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodGet:
			s.gets++
			body, ok := s.items[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(body))
		case http.MethodPatch:
			b, _ := io.ReadAll(r.Body)
			var ops []map[string]any
			_ = json.Unmarshal(b, &ops)
			s.patches = append(s.patches, adoLinkPatch{id: id, contentType: r.Header.Get("Content-Type"), ops: ops})
			w.WriteHeader(s.patchCode)
			_, _ = w.Write([]byte(`{"id":` + id + `}`))
		default:
			s.unexpected = append(s.unexpected, r.Method+" "+r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	t.Cleanup(func() {
		if len(s.unexpected) > 0 {
			t.Errorf("unexpected requests: %v", s.unexpected)
		}
	})
	return s
}

// linkItem is a work item answer with rev and relations ([rel, url] pairs).
func linkItem(id string, rev int, rels ...[2]string) string {
	r := []map[string]string{}
	for _, x := range rels {
		r = append(r, map[string]string{"rel": x[0], "url": x[1]})
	}
	b, _ := json.Marshal(map[string]any{"id": json.Number(id), "rev": rev, "fields": map[string]string{"System.WorkItemType": "Task"}, "relations": r})
	return string(b)
}

// TestADOLinkIssuesRelated: relate is a Related link, added once, and a no-op
// when either side already carries it.
func TestADOLinkIssuesRelated(t *testing.T) {
	t.Run("adds one Related link", func(t *testing.T) {
		srv := newADOLinkServer(t, map[string]string{"10": linkItem("10", 1), "20": linkItem("20", 1)})
		c := newTestAzureBoards(t, srv.Server, nil)
		if err := c.LinkIssues("10", "20"); err != nil {
			t.Fatal(err)
		}
		if len(srv.patches) != 1 {
			t.Fatalf("PATCHes = %d, want 1", len(srv.patches))
		}
		p := srv.patches[0]
		if p.id != "10" || p.contentType != "application/json-patch+json" || len(p.ops) != 1 {
			t.Fatalf("patch = %+v", p)
		}
		op := p.ops[0]
		val, _ := op["value"].(map[string]any)
		if op["op"] != "add" || op["path"] != "/relations/-" || val["rel"] != "System.LinkTypes.Related" {
			t.Errorf("op = %v, want an add of System.LinkTypes.Related", op)
		}
		if u, _ := val["url"].(string); !strings.HasSuffix(u, "/acme/_apis/wit/workItems/20") {
			t.Errorf("url = %q, want .../_apis/wit/workItems/20", u)
		}
		if strings.Contains(val["rel"].(string), "Hierarchy") {
			t.Error("relate used a hierarchy link")
		}
	})
	for name, items := range map[string]map[string]string{
		"present on from": {
			"10": linkItem("10", 1, [2]string{"System.LinkTypes.Related", "https://dev.azure.com/acme/_apis/wit/workItems/20"}),
			"20": linkItem("20", 1),
		},
		"present on to only": {
			"10": linkItem("10", 1),
			"20": linkItem("20", 1, [2]string{"System.LinkTypes.Related", "https://dev.azure.com/acme/_apis/wit/workItems/10"}),
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newADOLinkServer(t, items)
			if err := newTestAzureBoards(t, srv.Server, nil).LinkIssues("10", "20"); err != nil {
				t.Fatal(err)
			}
			if len(srv.patches) != 0 {
				t.Errorf("an existing Related link was re-added: %+v", srv.patches)
			}
		})
	}
	t.Run("a parent link is not a relation", func(t *testing.T) {
		srv := newADOLinkServer(t, map[string]string{
			"10": linkItem("10", 1, [2]string{"System.LinkTypes.Hierarchy-Reverse", "https://dev.azure.com/acme/_apis/wit/workItems/20"}),
			"20": linkItem("20", 1),
		})
		if err := newTestAzureBoards(t, srv.Server, nil).LinkIssues("10", "20"); err != nil {
			t.Fatal(err)
		}
		if len(srv.patches) != 1 {
			t.Errorf("PATCHes = %d, want 1 (a hierarchy link does not count as Related)", len(srv.patches))
		}
	})
}

// TestADOLinkParent: parenting is a Hierarchy-Reverse link on the child,
// skipped when already in place, and a move is one revision-tested patch.
func TestADOLinkParent(t *testing.T) {
	t.Run("no parent yet", func(t *testing.T) {
		srv := newADOLinkServer(t, map[string]string{"9": linkItem("9", 4)})
		if err := newTestAzureBoards(t, srv.Server, nil).LinkParent("5", "9"); err != nil {
			t.Fatal(err)
		}
		if len(srv.patches) != 1 || srv.patches[0].id != "9" {
			t.Fatalf("patches = %+v, want one PATCH of 9", srv.patches)
		}
		ops := srv.patches[0].ops
		last := ops[len(ops)-1]
		val, _ := last["value"].(map[string]any)
		if last["op"] != "add" || val["rel"] != "System.LinkTypes.Hierarchy-Reverse" ||
			!strings.HasSuffix(val["url"].(string), "/_apis/wit/workItems/5") {
			t.Errorf("last op = %v, want an add of Hierarchy-Reverse to workItems/5", last)
		}
		if ops[0]["op"] != "test" || ops[0]["path"] != "/rev" || ops[0]["value"] != float64(4) {
			t.Errorf("first op = %v, want test /rev 4", ops[0])
		}
	})
	for name, url := range map[string]string{
		"already under 5":                   "https://dev.azure.com/acme/_apis/wit/workItems/5",
		"already under 5, project GUID url": "https://dev.azure.com/acme/9b3f0c1e-0000-4000-8000-000000000001/_apis/wit/workItems/5",
	} {
		t.Run(name, func(t *testing.T) {
			srv := newADOLinkServer(t, map[string]string{"9": linkItem("9", 4, [2]string{"System.LinkTypes.Hierarchy-Reverse", url})})
			if err := newTestAzureBoards(t, srv.Server, nil).LinkParent("5", "9"); err != nil {
				t.Fatal(err)
			}
			if srv.gets != 1 || len(srv.patches) != 0 {
				t.Errorf("GETs %d PATCHes %d, want 1 and 0", srv.gets, len(srv.patches))
			}
		})
	}
	t.Run("under 4 moves to 5 in one patch", func(t *testing.T) {
		srv := newADOLinkServer(t, map[string]string{"9": linkItem("9", 7,
			[2]string{"System.LinkTypes.Related", "https://dev.azure.com/acme/_apis/wit/workItems/30"},
			[2]string{"System.LinkTypes.Hierarchy-Reverse", "https://dev.azure.com/acme/_apis/wit/workItems/4"},
		)})
		if err := newTestAzureBoards(t, srv.Server, nil).LinkParent("5", "9"); err != nil {
			t.Fatal(err)
		}
		if len(srv.patches) != 1 {
			t.Fatalf("PATCHes = %d, want 1", len(srv.patches))
		}
		ops := srv.patches[0].ops
		var kinds []string
		for _, o := range ops {
			kinds = append(kinds, o["op"].(string)+" "+o["path"].(string))
		}
		if want := []string{"test /rev", "remove /relations/1", "add /relations/-"}; !reflect.DeepEqual(kinds, want) {
			t.Errorf("ops = %q, want %q", kinds, want)
		}
		if ops[0]["value"] != float64(7) {
			t.Errorf("test /rev value = %v, want 7", ops[0]["value"])
		}
	})
	t.Run("a stale revision is an error naming the child, never retried", func(t *testing.T) {
		srv := newADOLinkServer(t, map[string]string{"9": linkItem("9", 7,
			[2]string{"System.LinkTypes.Hierarchy-Reverse", "https://dev.azure.com/acme/_apis/wit/workItems/4"})})
		srv.patchCode = http.StatusPreconditionFailed
		err := newTestAzureBoards(t, srv.Server, nil).LinkParent("5", "9")
		if err == nil || !strings.Contains(err.Error(), "9") {
			t.Fatalf("a refused rev test = %v, want an error naming work item 9", err)
		}
		if len(srv.patches) != 1 {
			t.Errorf("PATCHes = %d after a refusal, want exactly 1", len(srv.patches))
		}
	})
	t.Run("bad ids never reach the wire", func(t *testing.T) {
		srv := newADOLinkServer(t, map[string]string{})
		c := newTestAzureBoards(t, srv.Server, nil)
		for _, pair := range [][2]string{{"9", "9"}, {"PROJ-5", "9"}, {"5", "x"}, {"", "9"}} {
			if err := c.LinkParent(pair[0], pair[1]); err == nil {
				t.Errorf("LinkParent(%q, %q) succeeded", pair[0], pair[1])
			}
			if err := c.LinkIssues(pair[0], pair[1]); err == nil {
				t.Errorf("LinkIssues(%q, %q) succeeded", pair[0], pair[1])
			}
		}
		if srv.gets != 0 || len(srv.patches) != 0 {
			t.Errorf("bad ids made %d GETs and %d PATCHes, want none", srv.gets, len(srv.patches))
		}
	})
}

// TestGitHubIsNotAParentLinker: GitHub's REST issues have no hierarchy, so its
// client must fail the assertion rather than satisfy it with a lossy stub.
func TestGitHubIsNotAParentLinker(t *testing.T) {
	var gh any = (*GitHubClient)(nil)
	if _, ok := gh.(ParentLinker); ok {
		t.Error("*GitHubClient satisfies ParentLinker; GitHub issues have no parent link")
	}
	var ado any = (*AzureBoardsClient)(nil)
	if _, ok := ado.(ParentLinker); !ok {
		t.Error("*AzureBoardsClient does not satisfy ParentLinker")
	}
}

// TestRelationTargetID pins the trailing-id read.
func TestRelationTargetID(t *testing.T) {
	for raw, want := range map[string]int{
		"https://dev.azure.com/acme/_apis/wit/workItems/5":            5,
		"https://dev.azure.com/acme/9b3f0c1e/_apis/wit/workItems/42/": 42,
		"https://acme.visualstudio.com/_apis/wit/workItems/7?x=1":     7,
		"https://dev.azure.com/acme/_apis/wit/workItems/0":            0,
		"https://dev.azure.com/acme/_apis/wit/workItems/abc":          0,
		"%zz": 0,
	} {
		if got := relationTargetID(raw); got != want {
			t.Errorf("relationTargetID(%q) = %d, want %d", raw, got, want)
		}
	}
}
