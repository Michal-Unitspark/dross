package forge

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// Lifecycle state distinctness, per provider.
//
// c-3 asks for a UAT state "distinct from in-progress and from shipped": a
// phase awaiting a verdict is neither being worked nor delivered, and
// collapsing it into either is exactly what makes a board stop reflecting where
// work actually is. c-2 makes the same shape of claim one level down — picking
// a task and committing it must land in different columns.
//
// Nothing was asserting the distinctness itself. YouTrack's "UAT" is pinned by
// the bundle test only because that value has to be CREATED; Jira's
// defaultJiraStateMap["uat"] was keyed but its value unread, so collapsing it
// into "In Progress" would have satisfied every existing test while quietly
// deleting the state c-3 is about.
//
// What is asserted here is the RELATIONSHIP, not either provider's strings. A
// project is free to rename its columns — that is what [board].state_map is for
// — and a guard written against "UAT" or "In Review" would fight that. A guard
// written against "these two statuses must not be the same column" holds
// whatever the columns are called.

// distinctPairs are the lifecycle statuses that must never share a board
// column, each with the reason a reader needs when it fails.
var distinctPairs = []struct {
	a, b string
	why  string
}{
	{"uat", "in-progress", "a phase awaiting a verdict is not a phase still being worked (c-3)"},
	{"uat", "shipped", "a phase awaiting a verdict is not a phase already delivered (c-3)"},
	{"task-in-review", "task-in-progress", "a committed task is not a task still being written (c-2)"},
	{"task-complete", "task-in-review", "a task whose phase has shipped is not a task still awaiting the phase's verdict (c-2)"},
	{"task-complete", "task-in-progress", "a task whose phase has shipped is not a task still being written (c-2)"},
}

// TestStateMapsKeepTheLifecycleStatesDistinct gates both default maps.
//
// Both are checked, independently: a collapse in one provider's map is a real
// bug for whoever uses that tracker, and it is the provider whose value was
// never read that the collapse would hide in.
func TestStateMapsKeepTheLifecycleStatesDistinct(t *testing.T) {
	for _, m := range []struct {
		provider string
		varName  string
		states   map[string]string
	}{
		{"jira", "defaultJiraStateMap", defaultJiraStateMap},
		{"youtrack", "defaultYouTrackStateMap", defaultYouTrackStateMap},
		// Azure Boards maps to state CATEGORIES (locked state_resolution), so
		// distinctness is asserted between categories.
		{"azure-devops", "defaultADOStateCategory", defaultADOStateCategory},
	} {
		t.Run(m.provider, func(t *testing.T) {
			for _, p := range distinctPairs {
				av, aok := m.states[p.a]
				bv, bok := m.states[p.b]
				// A missing key would make the comparison below vacuous —
				// two absent statuses both read as "". The divergence guard
				// in internal/cmd owns presence; this says so out loud rather
				// than passing on nothing.
				if !aok || !bok {
					t.Errorf("%s is missing %s — the distinctness of %q and %q cannot be checked at all",
						m.varName, missingOf(p.a, aok, p.b, bok), p.a, p.b)
					continue
				}
				if av == bv {
					t.Errorf("%s maps both %q and %q to %q — %s", m.varName, p.a, p.b, av, p.why)
				}
			}
		})
	}
}

// missingOf names whichever side of a pair the map does not hold, for the error
// above.
func missingOf(a string, aok bool, b string, bok bool) string {
	switch {
	case !aok && !bok:
		return "both " + a + " and " + b
	case !aok:
		return a
	default:
		return b
	}
}

// TestTerminalTaskStateResolves pins the two values the task lane's terminal
// status maps to. The distinctness table above says what must NOT collide;
// this says what the default actually is, so a rename shows up as a decision
// rather than as a silently different column.
//
// Both are done-category states on their tracker, which is what makes the close
// read-back in closeBoardIssue able to verify a task card at all.
func TestTerminalTaskStateResolves(t *testing.T) {
	if got, ok := resolveYouTrackState("task-complete", nil); !ok || got != "Verified" {
		t.Errorf(`resolveYouTrackState("task-complete", nil) = %q,%v want "Verified",true`, got, ok)
	}
	if got, ok := resolveJiraState("task-complete", nil); !ok || got != "Done" {
		t.Errorf(`resolveJiraState("task-complete", nil) = %q,%v want "Done",true`, got, ok)
	}
}

// --- StateWriter: raw, unmapped state writes ---
//
// Every other write in this package is MAPPED — CloseIssueAs and SetState take
// a dross lifecycle status and resolve it through one of the two default maps.
// That is right going forward and useless going back: restoring a card to the
// arbitrary column it sat in before a sweep ("In Review") is not expressible as
// a dross status. These guards hold the capability's two invariants — the write
// is verbatim, and it is verified.

// "In Review" is the probe value on purpose: it is a real YouTrack/Jira column
// and is NOT a key in either default state map, so an implementation that
// routed through a map could not produce it verbatim.
const rawStateProbe = "In Review"

// TestSetStateRawBypassesTheStateMap: each implementing backend writes the
// literal string it was given.
func TestSetStateRawBypassesTheStateMap(t *testing.T) {
	t.Run("youtrack", func(t *testing.T) {
		var wrote []string
		c, _ := newTestYTClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" {
				var body struct {
					CustomFields []struct {
						Value struct {
							Name string `json:"name"`
						} `json:"value"`
					} `json:"customFields"`
				}
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &body)
				for _, cf := range body.CustomFields {
					wrote = append(wrote, cf.Value.Name)
				}
				_, _ = io.WriteString(w, `{"idReadable":"PROJ-7"}`)
				return
			}
			_, _ = io.WriteString(w, `{"idReadable":"PROJ-7","customFields":[{"name":"State","value":{"name":"`+rawStateProbe+`"}}]}`)
		})
		if err := c.SetStateRaw("PROJ-7", rawStateProbe); err != nil {
			t.Fatalf("SetStateRaw: %v", err)
		}
		if len(wrote) != 1 || wrote[0] != rawStateProbe {
			t.Errorf("wrote State %v, want the literal %q — a mapped write turns it into a lifecycle status", wrote, rawStateProbe)
		}
	})

	t.Run("jira", func(t *testing.T) {
		applied := ""
		c, _ := newTestJiraClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/transitions") && r.Method == "GET":
				_, _ = io.WriteString(w, `{"transitions":[{"id":"31","to":{"name":"`+rawStateProbe+`"}},{"id":"41","to":{"name":"Done"}}]}`)
			case strings.HasSuffix(r.URL.Path, "/transitions") && r.Method == "POST":
				var body struct {
					Transition struct {
						ID string `json:"id"`
					} `json:"transition"`
				}
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &body)
				applied = body.Transition.ID
				w.WriteHeader(http.StatusNoContent)
			default:
				_, _ = io.WriteString(w, `{"key":"PROJ-7","fields":{"status":{"name":"`+rawStateProbe+`"}}}`)
			}
		})
		if err := c.SetStateRaw("PROJ-7", rawStateProbe); err != nil {
			t.Fatalf("SetStateRaw: %v", err)
		}
		// 31 is the transition whose TARGET is the probe; 41 ("Done") is what a
		// map-routed implementation would reach for.
		if applied != "31" {
			t.Errorf("applied transition %q, want 31 — the one whose target is the literal %q", applied, rawStateProbe)
		}
	})

	t.Run("azure-devops", func(t *testing.T) {
		// "In Review" is no category and no default state: only a verbatim
		// write produces it.
		s := newADOStateServer(t, map[string]*adoStateItem{"7": {"User Story", "Active"}},
			map[string][]ADOState{"User Story": agileStory})
		if err := newTestAzureBoards(t, s.Server, nil).SetStateRaw("7", rawStateProbe); err != nil {
			t.Fatalf("SetStateRaw: %v", err)
		}
		if len(s.written) != 1 || s.written[0] != rawStateProbe {
			t.Errorf("wrote State %v, want the literal %q", s.written, rawStateProbe)
		}
	})

	t.Run("forge", func(t *testing.T) {
		// Forge and GitLab boards have no column model, so verbatim means
		// verbatim: whatever the ledger recorded is what goes back.
		wrote := ""
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "PATCH" || r.Method == "PUT" || r.Method == "POST" {
				var body map[string]any
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &body)
				if s, ok := body["state"].(string); ok {
					wrote = s
				}
			}
			_, _ = io.WriteString(w, `{"number":7,"state":"`+rawStateProbe+`"}`)
		})
		if err := c.SetStateRaw("7", rawStateProbe); err != nil {
			t.Fatalf("SetStateRaw: %v", err)
		}
		if wrote != rawStateProbe {
			t.Errorf("wrote state %q, want the literal %q", wrote, rawStateProbe)
		}
	})
}

// TestSetStateRawVerifiesReadBack: a tracker that accepts the write and changes
// nothing must produce an error naming the key, not a silent success. Without
// it an undo reports every card restored while the board still holds the
// sweep's writes.
func TestSetStateRawVerifiesReadBack(t *testing.T) {
	t.Run("youtrack", func(t *testing.T) {
		c, _ := newTestYTClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" {
				_, _ = io.WriteString(w, `{"idReadable":"PROJ-7"}`)
				return
			}
			// The workflow refused: the card still reads its old column.
			_, _ = io.WriteString(w, `{"idReadable":"PROJ-7","customFields":[{"name":"State","value":{"name":"Verified"}}]}`)
		})
		err := c.SetStateRaw("PROJ-7", rawStateProbe)
		if err == nil {
			t.Fatal("SetStateRaw reported success over a write the tracker did not take")
		}
		if !strings.Contains(err.Error(), "PROJ-7") {
			t.Errorf("error %q does not name the issue key", err)
		}
	})

	t.Run("azure-devops", func(t *testing.T) {
		s := newADOStateServer(t, map[string]*adoStateItem{"7": {"User Story", "Closed"}},
			map[string][]ADOState{"User Story": agileStory})
		s.stick = true // the process accepted the PATCH and kept the item where it was
		err := newTestAzureBoards(t, s.Server, nil).SetStateRaw("7", rawStateProbe)
		if err == nil {
			t.Fatal("SetStateRaw reported success over a write the tracker did not take")
		}
		for _, want := range []string{"7", rawStateProbe, "Closed"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}
	})

	t.Run("forge", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"number":7,"state":"closed"}`)
		})
		err := c.SetStateRaw("7", "open")
		if err == nil {
			t.Fatal("SetStateRaw reported success over a write the tracker did not take")
		}
		if !strings.Contains(err.Error(), "7") {
			t.Errorf("error %q does not name the issue key", err)
		}
	})

	// Jira is the leg an undo most needs this on. Its transition API answers
	// 204 for a transition a workflow condition then declines to complete, so
	// the POST succeeding is not evidence the issue moved — and unlike the
	// no-transition-available case, nothing upstream refuses first.
	t.Run("jira", func(t *testing.T) {
		const stuckAt = "Verified"
		posted := false
		c, _ := newTestJiraClient(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/transitions") && r.Method == "GET":
				_, _ = io.WriteString(w, `{"transitions":[{"id":"31","to":{"name":"`+rawStateProbe+`"}}]}`)
			case strings.HasSuffix(r.URL.Path, "/transitions") && r.Method == "POST":
				posted = true
				w.WriteHeader(http.StatusNoContent)
			default:
				// The read-back: the issue never left the column the sweep put
				// it in.
				_, _ = io.WriteString(w, `{"key":"PROJ-7","fields":{"status":{"name":"`+stuckAt+`"}}}`)
			}
		})

		err := c.SetStateRaw("PROJ-7", rawStateProbe)
		if !posted {
			t.Fatal("no transition was attempted — the fixture proves nothing about the READ-BACK")
		}
		if err == nil {
			t.Fatal("SetStateRaw reported success over a 204 the workflow did not honour — an undo would report every card restored while the board still holds the sweep's writes")
		}
		if !strings.Contains(err.Error(), "PROJ-7") {
			t.Errorf("error %q does not name the issue key", err)
		}
		if !strings.Contains(err.Error(), rawStateProbe) {
			t.Errorf("error %q does not name the status that was asked for", err)
		}
		// The status it ACTUALLY reads is what tells an operator whether the
		// restore was declined or landed somewhere else entirely. An error that
		// reports an empty column names a condition the tracker never showed.
		if !strings.Contains(err.Error(), stuckAt) {
			t.Errorf("error %q does not name the status the issue actually reads", err)
		}
	})
}

// TestJiraSetStateRawRefusesAnUnavailableTransition: SetState warns and returns
// nil when no transition is available, which is right for a cosmetic status
// label and wrong for a restore — an undo that transitioned nothing and
// reported success leaves the board as the sweep left it.
func TestJiraSetStateRawRefusesAnUnavailableTransition(t *testing.T) {
	c, _ := newTestJiraClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/transitions") && r.Method == "GET" {
			_, _ = io.WriteString(w, `{"transitions":[{"id":"41","to":{"name":"Done"}}]}`)
			return
		}
		t.Errorf("unexpected %s %s — no transition should have been attempted", r.Method, r.URL.Path)
		_, _ = io.WriteString(w, `{}`)
	})
	if err := c.SetStateRaw("PROJ-7", rawStateProbe); err == nil {
		t.Fatal("SetStateRaw succeeded with no transition to the requested status")
	}
}

// TestGitHubIsNotAStateWriter: GitHub has no column model, so it must FAIL the
// assertion rather than satisfy it with a lossy reopen that lands an "In
// Review" card in `open` and calls it restored.
func TestGitHubIsNotAStateWriter(t *testing.T) {
	if _, ok := any((*GitHubClient)(nil)).(StateWriter); ok {
		t.Error("*GitHubClient satisfies StateWriter — GitHub issues have no column to restore, so a caller must be able to refuse by provider name")
	}
	// The other half: the backends that DO have a column model must satisfy it,
	// or --undo silently refuses everywhere.
	for name, c := range map[string]any{
		"*YouTrackClient": (*YouTrackClient)(nil),
		"*JiraClient":     (*JiraClient)(nil),
		"*Client":         (*Client)(nil),
	} {
		if _, ok := c.(StateWriter); !ok {
			t.Errorf("%s does not satisfy StateWriter", name)
		}
	}
}

// TestStateWriterIsNotOnBoardClient: keeping the capability off BoardClient is
// what leaves every existing fake — including the two in internal/cmd —
// satisfying the interface unchanged. Folding it in would make each of them
// grow a method they have no way to implement honestly.
func TestStateWriterIsNotOnBoardClient(t *testing.T) {
	bc := reflect.TypeOf((*BoardClient)(nil)).Elem()
	if _, ok := bc.MethodByName("SetStateRaw"); ok {
		t.Error("BoardClient declares SetStateRaw — it belongs on the optional StateWriter, alongside IssueLinker")
	}
	sw := reflect.TypeOf((*StateWriter)(nil)).Elem()
	if _, ok := sw.MethodByName("SetStateRaw"); !ok {
		t.Fatal("StateWriter does not declare SetStateRaw — the guard above would pass vacuously")
	}
}
