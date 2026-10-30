package ship

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// adoThreads wraps thread records in the list envelope Azure DevOps answers
// .../threads with.
func adoThreads(threads ...string) string {
	return `{"value":[` + strings.Join(threads, ",") + `],"count":` + strconv.Itoa(len(threads)) + `}`
}

// adoThread is one thread holding comments, with an optional extra field set
// (a threadContext, isDeleted, status) spliced in.
func adoThread(id int, extra string, comments ...string) string {
	if extra != "" {
		extra = "," + extra
	}
	return `{"id":` + strconv.Itoa(id) + extra + `,"comments":[` + strings.Join(comments, ",") + `]}`
}

// adoTextBy is a text comment by an author record.
func adoTextBy(id int, author, body string) string {
	return `{"id":` + strconv.Itoa(id) + `,"parentCommentId":0,"author":` + author + `,"content":"` + body + `","commentType":"text"}`
}

// adoText is a text comment by a plain reviewer.
func adoText(id int, body string) string {
	return adoTextBy(id, `{"id":"`+adoGUID1+`","uniqueName":"rev@corp.com","displayName":"Rev Iewer"}`, body)
}

// listADOThreads serves body as the threads answer and lists PR 7.
func listADOThreads(t *testing.T, body string) ([]PRComment, *adoRecorder, error) {
	t.Helper()
	t.Setenv(adoTestEnv, adoTestTok)
	rec := newADORecorder(t, 200, body)
	got, err := azureDevOpsPRComments(adoTestOpts(rec.Server), 7)
	return got, rec, err
}

// TestADOThreadCommentIDs: a thread id followed by a five-digit comment id
// splits back into one pair, so no two comments on a PR share an id, and every
// id stays digits only.
func TestADOThreadCommentIDs(t *testing.T) {
	got, _, err := listADOThreads(t, adoThreads(
		adoThread(1, "", adoText(12, "a")),
		adoThread(11, "", adoText(2, "b")),
		adoThread(7, "", adoText(1, "c")),
		adoThread(8, "", adoText(1, "d")),
	))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"100012", "1100002", "700001", "800001"}
	if len(got) != len(want) {
		t.Fatalf("got %d comments, want %d: %+v", len(got), len(want), got)
	}
	digits := regexp.MustCompile(`^[0-9]+$`)
	seen := map[string]bool{}
	for i, c := range got {
		if c.ID != want[i] {
			t.Errorf("comment %d: id %q, want %q", i, c.ID, want[i])
		}
		if !digits.MatchString(c.ID) {
			t.Errorf("id %q is not digits only", c.ID)
		}
		if seen[c.ID] {
			t.Errorf("id %q repeats", c.ID)
		}
		seen[c.ID] = true
	}

	for _, id := range []int{0, 100000} {
		got, _, err := listADOThreads(t, adoThreads(adoThread(3, "", adoText(id, "x"))))
		if err == nil || got != nil {
			t.Errorf("comment id %d: got %+v, %v; want (nil, err)", id, got, err)
		}
	}
}

// TestADOThreadsSkipNoise: votes, pushes, status changes and deleted threads
// or comments are not comments anyone wrote; every thread status is listed,
// and the request asks for none in particular.
func TestADOThreadsSkipNoise(t *testing.T) {
	author := `{"id":"` + adoGUID1 + `","uniqueName":"rev@corp.com"}`
	system := func(id int, body string, kind string) string {
		return `{"id":` + strconv.Itoa(id) + `,"author":` + author + `,"content":"` + body + `","commentType":` + kind + `}`
	}
	got, rec, err := listADOThreads(t, adoThreads(
		adoThread(1, `"properties":{"CodeReviewThreadType":{"$value":"VoteUpdate"}}`, system(1, "Rev voted 10", `"system"`)),
		adoThread(2, `"properties":{"CodeReviewThreadType":{"$value":"VoteUpdate"}}`, system(1, "Rev voted -5", `3`)),
		adoThread(3, `"properties":{"CodeReviewThreadType":{"$value":"RefUpdate"}}`, system(1, "Rev pushed 1 commit", `"system"`)),
		adoThread(4, `"properties":{"CodeReviewThreadType":{"$value":"StatusUpdate"}}`, system(1, "Rev set the status", `"system"`)),
		adoThread(5, `"isDeleted":true`, adoText(1, "gone thread")),
		adoThread(6, "", `{"id":1,"author":`+author+`,"content":"","commentType":"text","isDeleted":true}`),
		adoThread(10, `"status":"active"`, system(1, "kept text", `"text"`)),
		adoThread(11, `"status":"fixed"`, system(1, "kept numeric", `1`)),
		adoThread(12, `"status":"closed"`, system(1, "kept code change", `"codeChange"`)),
		adoThread(13, `"status":"wontFix"`, adoText(1, "wontfix")),
		adoThread(14, `"status":"byDesign"`, adoText(1, "bydesign")),
		adoThread(15, `"status":"pending"`, adoText(1, "pending")),
	))
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, c := range got {
		bodies = append(bodies, c.Body)
	}
	want := []string{"kept text", "kept numeric", "kept code change", "wontfix", "bydesign", "pending"}
	if strings.Join(bodies, "|") != strings.Join(want, "|") {
		t.Errorf("listed %q, want %q", bodies, want)
	}
	if !strings.HasSuffix(rec.path, "/pullRequests/7/threads") {
		t.Errorf("path = %q, want .../pullRequests/7/threads", rec.path)
	}
	for k := range rec.query {
		if k != "api-version" {
			t.Errorf("the threads request carries %s=%v; it must send no filter", k, rec.query[k])
		}
	}
}

// TestADOThreadInlineAnchor: a file-anchored thread is inline at its new-side
// line, or its old-side line on a removed line; a file-level thread has a path
// and no line; a thread without context is conversation.
func TestADOThreadInlineAnchor(t *testing.T) {
	for _, tc := range []struct {
		name, ctx string
		kind      CommentKind
		path      string
		line      int
	}{
		{"right side", `"threadContext":{"filePath":"/src/a.go","rightFileStart":{"line":12,"offset":1},"rightFileEnd":{"line":12,"offset":9}}`, CommentInline, "src/a.go", 12},
		{"left side only", `"threadContext":{"filePath":"/src/a.go","leftFileStart":{"line":30,"offset":1}}`, CommentInline, "src/a.go", 30},
		{"file level", `"threadContext":{"filePath":"/docs/x.md"}`, CommentInline, "docs/x.md", 0},
		{"no context", "", CommentConversation, "", 0},
		{"null context", `"threadContext":null`, CommentConversation, "", 0},
	} {
		got, _, err := listADOThreads(t, adoThreads(adoThread(42, tc.ctx, adoText(1, "look"))))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(got) != 1 {
			t.Fatalf("%s: got %d comments", tc.name, len(got))
		}
		if c := got[0]; c.Kind != tc.kind || c.Path != tc.path || c.Line != tc.line {
			t.Errorf("%s: got kind %s path %q line %d, want %s %q %d", tc.name, c.Kind, c.Path, c.Line, tc.kind, tc.path, tc.line)
		}
	}
}

// TestADOThreadsNeverPartial: a fault anywhere in the answer is (nil, err),
// never the threads decoded before it.
func TestADOThreadsNeverPartial(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	good := adoThread(1, "", adoText(1, "fine"))
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"thread id 0":      {200, adoThreads(good, adoThread(0, "", adoText(1, "x")))},
		"non-JSON body":    {200, `<html>not json</html>`},
		"no value array":   {200, `{"count":0}`},
		"500":              {500, `{"message":"boom"}`},
		"203 sign-in page": {http.StatusNonAuthoritativeInfo, `<html>Sign In</html>`},
	} {
		rec := newADORecorder(t, tc.status, tc.body)
		got, err := azureDevOpsPRComments(adoTestOpts(rec.Server), 7)
		if err == nil || got != nil {
			t.Errorf("%s: got %+v, %v; want (nil, err)", name, got, err)
		}
	}

	rec := newADORecorder(t, 200, adoThreads())
	if _, err := azureDevOpsPRComments(adoTestOpts(rec.Server), 0); err == nil {
		t.Error("PR number 0 was accepted")
	}
	if n := rec.hits.Load(); n != 0 {
		t.Errorf("%d request(s) went out without a PR number", n)
	}
}

// TestADOThreadAuthorNotGuessedBot: Azure DevOps has no bot flag, so a build
// service or a group author is still unknown; the link is the browser form of
// the thread, escaped once.
func TestADOThreadAuthorNotGuessedBot(t *testing.T) {
	got, _, err := listADOThreads(t, adoThreads(
		adoThread(42, "", adoTextBy(1, `{"id":"`+adoGUID2+`","uniqueName":"Build\\\\p","displayName":"Project Collection Build Service (o)"}`, "built")),
		adoThread(43, "", adoTextBy(1, `{"id":"`+adoGUID2+`","uniqueName":"[p]\\\\Team","displayName":"Team","isContainer":true}`, "group")),
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d comments, want 2", len(got))
	}
	for _, c := range got {
		if c.Author.Bot != AuthorUnknown {
			t.Errorf("%q: Bot = %s, want unknown", c.Body, c.Author.Bot)
		}
	}
	if want := "https://dev.azure.com/o/p/_git/r/pullrequest/7?discussionId=42"; got[0].URL != want {
		t.Errorf("URL = %q, want %q", got[0].URL, want)
	}
}

// TestADOThreadAuthorUniqueName: the login is the unique name — the one an
// account cannot share — never the display name.
func TestADOThreadAuthorUniqueName(t *testing.T) {
	got, _, err := listADOThreads(t, adoThreads(
		adoThread(1, "", adoTextBy(1, `{"id":"AB12CD34-0000-4000-8000-000000000001","uniqueName":"rev@corp.com","displayName":"Rev Iewer"}`, "hi")),
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d comments, want 1", len(got))
	}
	if a := got[0].Author; a.Login != "rev@corp.com" || a.ID != "ab12cd34-0000-4000-8000-000000000001" {
		t.Errorf("author = %+v, want Login rev@corp.com and the id lowercased", a)
	}

	// The caller's own login is properties.Account, never providerDisplayName.
	rec := newADORecorder(t, 200, adoConnectionData(adoGUID1, "me@corp.com"))
	me, err := azureDevOpsAuthenticatedUser(adoTestOpts(rec.Server))
	if err != nil || me.Login != "me@corp.com" || me.ID != adoGUID1 {
		t.Errorf("connectionData: got %+v, %v; want Login me@corp.com ID %s", me, err, adoGUID1)
	}
	rec = newADORecorder(t, 200, adoConnectionData(adoGUID1, ""))
	if me, err := azureDevOpsAuthenticatedUser(adoTestOpts(rec.Server)); err == nil {
		t.Errorf("connectionData without properties.Account: got %+v with a nil error", me)
	}
}
