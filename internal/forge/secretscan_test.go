package forge

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Rivil/dross/internal/secretscan"
)

// Synthesized secret shapes. Every one is ASSEMBLED at runtime: a literal
// token shape in a test file is exactly what the phase's self-scan trips on.
func synthAtlassianToken() string { return "ATATT" + strings.Repeat("x9Yz", 6) }
func synthPEMHeader() string {
	return strings.Repeat("-", 5) + "BEGIN RSA PRIVATE KEY" + strings.Repeat("-", 5)
}
func synthGitHubToken() string { return "ghp_" + strings.Repeat("A1b2", 9) }

// benignBody is a renderPhaseBody-shaped text: a 16-hex identity id, a
// 40-hex SHA and a UUID. None of it is a credential and all of it must post.
const benignBody = "phase secret-detection\n\n" +
	"issue id: 0123456789abcdef\n" +
	"commit: " + "0123456789abcdef0123456789abcdef01234567" + "\n" +
	"run: 123e4567-e89b-12d3-a456-426614174000\n"

// counter records what reached the httptest server: every request, and the
// subset that could carry a payload. The discovery GETs some publish paths
// make first (Jira's project lookup, YouTrack's epic search) are reads the
// screen is meant to let through; the assertion that matters is that no WRITE
// ever left.
type counter struct {
	total  atomic.Int32
	writes atomic.Int32
}

// countingHandler serves the minimal shape every backend's decoder accepts,
// with getBody as the response to GETs when the test needs a specific one.
func countingHandler(c *counter, getBody string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.total.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			if getBody == "" {
				getBody = `[]`
			}
			_, _ = w.Write([]byte(getBody))
			return
		}
		c.writes.Add(1)
		_, _ = w.Write([]byte(`{"number":1,"key":"PROJ-1","id":"1","idReadable":"PROJ-1","title":"t","summary":"t","state":"open"}`))
	}
}

// backend is one BoardClient under test with the JSON field its issue body
// travels in — `body` on the GitHub-shaped APIs, `description` on YouTrack.
type backend struct {
	name      string
	bodyField string
	build     func(t *testing.T, h http.HandlerFunc) BoardClient
}

func allBackends() []backend {
	return []backend{
		{"forgejo", "body", func(t *testing.T, h http.HandlerFunc) BoardClient { c, _ := newTestClient(t, h); return c }},
		{"github", "body", func(t *testing.T, h http.HandlerFunc) BoardClient { c, _ := newTestGitHubClient(t, "", h); return c }},
		{"jira", "fields.description", func(t *testing.T, h http.HandlerFunc) BoardClient { c, _ := newTestJiraClient(t, h); return c }},
		{"youtrack", "description", func(t *testing.T, h http.HandlerFunc) BoardClient { c, _ := newTestYTClient(t, h); return c }},
		{"azure-devops", "System.Description", func(t *testing.T, h http.HandlerFunc) BoardClient {
			srv, _ := adoBoardsServer(t, adoCountingHandler(h))
			return newTestAzureBoards(t, srv, nil)
		}},
	}
}

// adoCountingHandler fronts a test handler for Azure Boards. The process
// metadata a write needs first — the backlog configuration, a type's states —
// is answered here, uncounted, so the counter sees exactly the work-item
// traffic the other backends' counters see. Every work-item request goes to h
// to be counted, and whatever issue h answers (its title and description or
// body) is re-shaped into Azure Boards' own: a work item, a WIQL id list, or
// a workitemsbatch page.
func adoCountingHandler(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := strings.TrimPrefix(r.URL.EscapedPath(), adoProjectAPI)
		switch {
		case r.Method == http.MethodGet && p == "/work/backlogconfiguration":
			_, _ = w.Write([]byte(backlogConfig("User Story")))
			return
		case r.Method == http.MethodGet && strings.HasPrefix(p, "/wit/workitemtypes"):
			_, _ = w.Write([]byte(`{"value":[{"name":"New","category":"Proposed"}]}`))
			return
		}
		rec := httptest.NewRecorder()
		h(rec, r)
		var answered map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &answered)
		text := func(keys ...string) string {
			for _, k := range keys {
				if v, ok := answered[k].(string); ok {
					return v
				}
			}
			return ""
		}
		item, _ := json.Marshal(map[string]any{"id": 1, "rev": 1, "fields": map[string]string{
			"System.WorkItemType": "User Story", "System.Title": text("title", "summary"),
			"System.Description": text("description", "body"), "System.State": "New",
		}})
		switch {
		case p == "/wit/wiql":
			_, _ = w.Write([]byte(`{"workItems":[{"id":1}]}`))
		case p == "/wit/workitemsbatch":
			_, _ = w.Write([]byte(`{"count":1,"value":[` + string(item) + `]}`))
		default:
			_, _ = w.Write(item)
		}
	}
}

// numericKeys reports whether a backend's issue keys are bare numbers, so it
// refuses "PROJ-1" before any request.
func numericKeys(backend string) bool {
	return backend == "forgejo" || backend == "github" || backend == "azure-devops"
}

// requireRefused asserts err carries a *secretscan.ErrHit naming rule and a
// location containing locPart, and that no request reached the server.
func requireRefused(t *testing.T, err error, c *counter, rule, locPart string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected refusal, got nil error")
	}
	var hit *secretscan.ErrHit
	if !errors.As(err, &hit) {
		t.Fatalf("error is not a *secretscan.ErrHit: %v", err)
	}
	found := false
	for _, h := range hit.Hits {
		if h.Rule == rule && strings.Contains(h.Location, locPart) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no hit with rule %q and location containing %q in %v", rule, locPart, hit.Hits)
	}
	if n := c.writes.Load(); n != 0 {
		t.Fatalf("%d write request(s) reached the server; the screen must run before the request", n)
	}
}

// TestCreateIssueRefusedBeforeRequest pins the screen's position: a token in
// the issue body is refused as a *secretscan.ErrHit naming the rule and the
// JSON field, and nothing reaches the server.
func TestCreateIssueRefusedBeforeRequest(t *testing.T) {
	for _, b := range allBackends() {
		t.Run(b.name, func(t *testing.T) {
			var c counter
			cl := b.build(t, countingHandler(&c, ""))
			_, err := cl.CreateIssue(IssueInput{Title: "t", Body: "line1\n" + synthAtlassianToken()})
			requireRefused(t, err, &c, "atlassian-token", ":"+b.bodyField)
			if c.total.Load() != 0 {
				t.Fatalf("%d request(s) reached the server", c.total.Load())
			}
		})
	}
}

// TestUpdateIssueBodyPatchRefused pins that a body PATCH is screened, and
// that the screen does not swallow a clean labels-only patch.
func TestUpdateIssueBodyPatchRefused(t *testing.T) {
	for _, b := range allBackends() {
		t.Run(b.name, func(t *testing.T) {
			var c counter
			cl := b.build(t, countingHandler(&c, `{"issues":[],"values":[]}`))
			tok := synthAtlassianToken()
			_, err := cl.UpdateIssue("PROJ-1", IssuePatch{Body: &tok})
			if numericKeys(b.name) {
				// Numeric-key backends refuse a non-numeric key before any
				// request; use a number so the screen is what refuses.
				_, err = cl.UpdateIssue("1", IssuePatch{Body: &tok})
			}
			requireRefused(t, err, &c, "atlassian-token", ":"+b.bodyField)

			labels := []string{"dross/phase"}
			key := "PROJ-1"
			if numericKeys(b.name) {
				key = "1"
			}
			_, _ = cl.UpdateIssue(key, IssuePatch{Labels: &labels})
			if c.total.Load() == 0 {
				t.Fatal("clean labels-only patch never reached the server")
			}
		})
	}
}

// TestEnsureMilestoneDescriptionRefused pins the publish paths with no Body
// field: a PEM header in a milestone description is refused before the POST.
// Both backends make a discovery GET first; that read is allowed through and
// the assertion is that no write follows it.
func TestEnsureMilestoneDescriptionRefused(t *testing.T) {
	t.Run("youtrack epic", func(t *testing.T) {
		var c counter
		cl, _ := newTestYTClient(t, countingHandler(&c, `[]`))
		_, err := cl.EnsureMilestoneEntity("epic", "v9", synthPEMHeader())
		requireRefused(t, err, &c, "pem-private-key", ":description")
	})
	t.Run("jira version", func(t *testing.T) {
		var c counter
		cl, _ := newTestJiraClient(t, countingHandler(&c, `{"id":"100","versions":[]}`))
		_, err := cl.EnsureMilestoneEntity("version", "v9", synthPEMHeader())
		requireRefused(t, err, &c, "pem-private-key", ":description")
	})
}

// TestBoardPublishBenignBodyPosts pins that the gate does not break ordinary
// board sync: identity ids, SHAs and UUIDs post on every backend.
func TestBoardPublishBenignBodyPosts(t *testing.T) {
	for _, b := range allBackends() {
		t.Run(b.name, func(t *testing.T) {
			var c counter
			cl := b.build(t, countingHandler(&c, ""))
			if _, err := cl.CreateIssue(IssueInput{Title: "phase", Body: benignBody}); err != nil {
				t.Fatalf("benign body refused: %v", err)
			}
			if n := c.writes.Load(); n != 1 {
				t.Fatalf("want exactly 1 write, got %d", n)
			}
		})
	}
}

// TestDoRawNilBodyIsUntouched pins that a nil body passes the screen and a
// GET goes out exactly as before.
func TestDoRawNilBodyIsUntouched(t *testing.T) {
	for _, b := range allBackends() {
		t.Run(b.name, func(t *testing.T) {
			var c counter
			cl := b.build(t, countingHandler(&c, `{"number":1,"key":"PROJ-1","idReadable":"PROJ-1","title":"t","summary":"t","state":"open","fields":{"summary":"t"}}`))
			key := "PROJ-1"
			if numericKeys(b.name) {
				key = "1"
			}
			if _, err := cl.GetIssue(key); err != nil {
				t.Fatalf("GET refused or failed: %v", err)
			}
			if n := c.total.Load(); n != 1 {
				t.Fatalf("want exactly 1 request, got %d", n)
			}
		})
	}
}

// TestGetAndListAreNotScreened pins that the screen reads the request, never
// the response: an issue whose body carries a token is fetched and returned.
func TestGetAndListAreNotScreened(t *testing.T) {
	tok := synthGitHubToken()
	issue := `{"number":1,"key":"PROJ-1","idReadable":"PROJ-1","title":"t","summary":"t","body":` + quote(tok) +
		`,"description":` + quote(tok) + `,"state":"open","fields":{"summary":"t","description":` + quote(tok) + `}}`
	for _, b := range allBackends() {
		t.Run(b.name, func(t *testing.T) {
			var c counter
			getBody := issue
			cl := b.build(t, func(w http.ResponseWriter, r *http.Request) {
				c.total.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "/search") || strings.HasSuffix(r.URL.Path, "/issues") {
					if b.name == "jira" {
						_, _ = w.Write([]byte(`{"issues":[` + getBody + `]}`))
					} else {
						_, _ = w.Write([]byte(`[` + getBody + `]`))
					}
					return
				}
				_, _ = w.Write([]byte(getBody))
			})
			key := "PROJ-1"
			if numericKeys(b.name) {
				key = "1"
			}
			got, err := cl.GetIssue(key)
			if err != nil {
				t.Fatalf("GetIssue: %v", err)
			}
			if got == nil {
				t.Fatal("GetIssue returned nil issue")
			}
			if b.name == "azure-devops" && !strings.Contains(got.Body, tok) {
				t.Errorf("GetIssue dropped the token-bearing description: %q", got.Body)
			}
			list, err := cl.ListIssues(IssueFilter{})
			if err != nil {
				t.Fatalf("ListIssues: %v", err)
			}
			if len(list) != 1 {
				t.Fatalf("ListIssues returned %d issues, want 1", len(list))
			}
		})
	}
}

// TestPayloadErrorNeverEchoes pins that the string a refusal produces — the
// one that would reach telemetry err_detail — carries no window of the token
// even after redact.Err wrapping.
func TestPayloadErrorNeverEchoes(t *testing.T) {
	tok := synthAtlassianToken()
	variable := strings.TrimPrefix(tok, "ATATT")
	for _, b := range allBackends() {
		t.Run(b.name, func(t *testing.T) {
			var c counter
			cl := b.build(t, countingHandler(&c, ""))
			_, err := cl.CreateIssue(IssueInput{Title: "t", Body: tok})
			if err == nil {
				t.Fatal("expected refusal")
			}
			msg := err.Error()
			for i := 0; i+6 <= len(variable); i++ {
				if strings.Contains(msg, variable[i:i+6]) {
					t.Fatalf("error echoes the token (window %d): %s", i, msg)
				}
			}
			// The remedy is asserted by its phrase, not by AllowMarker: the
			// test clients' token is the literal word "secret", so redact.Err
			// scrubs it out of `dross:allow-secret` here. No real token is
			// that word; the scrub is the fixture's, not the report's.
			if !strings.Contains(msg, "atlassian-token") || !strings.Contains(msg, "fix the line by hand") {
				t.Fatalf("error lacks the rule name or the remedy: %s", msg)
			}
		})
	}
}

func quote(s string) string { return `"` + s + `"` }
