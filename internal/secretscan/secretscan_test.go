package secretscan_test

// Every credential-shaped value in this file is BUILT AT RUNTIME from parts
// that are not themselves a shape. A literal token here would be a real hit for
// the self-scan that proves c-6 — the tests that prove c-1 must not be the
// thing that fails c-6.

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/secretscan"
)

// --- runtime-built shapes ---

func ghTok() string      { return "ghp_" + strings.Repeat("A1b2", 9) }
func ghFineTok() string  { return "github_pat_" + strings.Repeat("Z9_x", 5) + "Qw" } // 22 chars: the floor
func glTok() string      { return "glpat-" + strings.Repeat("xY9-", 5) }
func atlTok() string     { return "ATATT" + strings.Repeat("3xF=", 6) }
func awsKey() string     { return "AKIA" + strings.Repeat("Q7", 8) }
func awsExample() string { return "AKIA" + "IOSFODNN7" + "EXAMPLE" }
func slackTok() string   { return "xoxb-" + strings.Repeat("12-", 4) }
func skKey() string      { return "sk-proj-" + strings.Repeat("aB_1", 5) }
func pemHeader() string  { return "-----BEGIN " + "RSA PRIVATE KEY-----" }
func bearer24() string   { return "Authorization: Bearer " + strings.Repeat("Ab3/", 6) }
func highEntropy() string {
	return strings.Repeat("Aa1!Bb2@Cc3#Dd4%Ee5^Ff6*", 1)
}

// adoPATAt is an alphanumeric run of n chars with the AZDO signature at index
// at; adoPAT is the real shape, 84 chars with the signature at 76.
func adoPATAt(n, at int) string {
	fill := strings.Repeat("Ab3x", 25)
	return fill[:at] + "AZ" + "DO" + fill[:n-at-4]
}
func adoPAT() string { return adoPATAt(84, 76) }

// adoOldPAT is an old-format 52-char base32 PAT shape.
func adoOldPAT() string { return strings.Repeat("abcdefghijklmnopqrstuvwxyz234567", 2)[:52] }

const hex16 = "0123456789abcdef"

func sha40() string { return strings.Repeat(hex16, 2) + hex16[:8] }
func uuid() string  { return "123e4567-e89b-12d3-a456-426614174000" }
func b64_60() string {
	return strings.Repeat("QUJDREVGR0hJSktMTU5P", 3)
}

// hitCorpus maps every rule name to one line that must yield exactly one hit
// of that rule and nothing else.
func hitCorpus() map[string]string {
	return map[string]string{
		"github-token":         ghTok(),
		"gitlab-pat":           glTok(),
		"azure-devops-pat":     adoPAT(),
		"atlassian-token":      atlTok(),
		"aws-access-key":       awsKey(),
		"slack-token":          slackTok(),
		"sk-api-key":           skKey(),
		"pem-private-key":      pemHeader(),
		"authorization-header": bearer24(),
		"key-context":          "password = " + highEntropy(),
	}
}

type benignRow struct {
	name, line string
}

// benignCorpus is every shape the tree legitimately carries that a sloppier
// rule would flag: identity ids, commit SHAs, UUIDs, base64 fixtures, env-var
// references, placeholders, and values below the entropy or length floor.
func benignCorpus() []benignRow {
	return []benignRow{
		{"16hex under json key", `"key": "` + hex16 + `"`},
		{"16hex under id =", "id = " + hex16},
		{"16hex under json Key", `"Key": "` + hex16 + `"`},
		{"bare sha", sha40()},
		{"sha under sha =", "sha = " + sha40()},
		{"bare uuid", uuid()},
		{"uuid under id =", "id = " + uuid()},
		{"base64 under snippet", `"snippet": "` + b64_60() + `"`},
		{"base64 under expected =", "expected = " + b64_60()},
		{"auth_env name", `auth_env = "GITHUB_TOKEN"`},
		{"token env reference", `token = "$GITHUB_TOKEN"`},
		{"bearer placeholder", "Authorization: Bearer <token>"},
		{"redaction marker", "[redacted $GITHUB_TOKEN]"},
		{"aws doc placeholder", awsExample()},
		{"repetitive password (entropy < 3)", "password = " + strings.Repeat("ab", 10) + "c"},
		{"short secret (15 chars)", "secret = " + highEntropy()[:15]},
		{"bearer concatenation in Go", `Authorization: "Bearer "+c.token`},
		{"bearer angle placeholder", "Authorization: Bearer <token-from-auth_env>"},
		{"ado pat placeholder", "<your-pat>"},
		{"ado pat env reference", "$AZURE_DEVOPS_EXT_PAT"},
		{"ado auth_env name", "auth_env = AZURE_DEVOPS_EXT_PAT"},
		{"bare old-format ado pat", adoOldPAT()},
	}
}

func scan(t *testing.T, name, text string) []secretscan.Hit {
	t.Helper()
	hits, err := secretscan.Scan(name, strings.NewReader(text))
	if err != nil {
		t.Fatalf("Scan(%s): %v", name, err)
	}
	return hits
}

// --- c-1: every rule catches its shape; nothing benign fires ---

func TestEveryRuleHasAHitCase(t *testing.T) {
	corpus := hitCorpus()
	var ruleNames, corpusNames []string
	for _, r := range secretscan.Rules() {
		ruleNames = append(ruleNames, r.Name)
	}
	for k := range corpus {
		corpusNames = append(corpusNames, k)
	}
	sort.Strings(ruleNames)
	sort.Strings(corpusNames)
	if strings.Join(ruleNames, ",") != strings.Join(corpusNames, ",") {
		t.Fatalf("rule set %v != hit corpus keys %v", ruleNames, corpusNames)
	}
	for name, line := range corpus {
		hits := scan(t, "corpus", line)
		if len(hits) != 1 {
			t.Errorf("%s: want exactly one hit, got %d: %v", name, len(hits), hits)
			continue
		}
		if hits[0].Rule != name {
			t.Errorf("%s: hit rule = %q", name, hits[0].Rule)
		}
	}
}

// TestGitHubFineGrainedPATFires pins the github_pat_ alternation the criterion
// names by hand: the hit corpus holds one row per rule and that row is the
// classic ghp_ shape, so without this the fine-grained arm could be deleted
// from rules.go unnoticed. 22 characters is the rule's floor; 21 must miss.
func TestGitHubFineGrainedPATFires(t *testing.T) {
	tok := ghFineTok()
	if n := len(tok) - len("github_pat_"); n != 22 {
		t.Fatalf("fixture drifted: want a 22-char body, got %d", n)
	}
	hits := scan(t, "corpus", tok)
	if len(hits) != 1 || hits[0].Rule != "github-token" {
		t.Fatalf("github_pat_ at 22 chars: want one github-token hit, got %v", hits)
	}
	if hits := scan(t, "corpus", tok[:len(tok)-1]); len(hits) != 0 {
		t.Fatalf("github_pat_ at 21 chars must miss the floor, got %v", hits)
	}
}

// TestAzureDevOpsPATAnchor: the AZDO signature is pinned to positions 76-79 of
// an exactly-84-char run. Anywhere else, any other length, or lower case, and
// it is not the PAT shape.
func TestAzureDevOpsPATAnchor(t *testing.T) {
	if tok := adoPAT(); len(tok) != 84 || tok[76:80] != "AZ"+"DO" {
		t.Fatalf("fixture drifted: %d chars, signature %q", len(tok), tok[76:80])
	}
	for name, line := range map[string]string{
		"signature at 75":              adoPATAt(84, 75),
		"signature at 77":              adoPATAt(84, 77),
		"83-char run":                  adoPATAt(83, 76),
		"85-char run":                  adoPATAt(85, 76),
		"85-char run, signature at 77": adoPATAt(85, 77),
		"lowercase azdo":               strings.ToLower(adoPAT()[:76]) + "az" + "do" + "ab3x",
	} {
		if hits := scan(t, "anchor", line); len(hits) != 0 {
			t.Errorf("%s: want zero hits, got %v", name, hits)
		}
	}
	for _, line := range []string{adoPAT(), "pat: " + adoPAT() + ".", `"` + adoPAT() + `"`} {
		hits := scan(t, "anchor", line)
		if len(hits) != 1 || hits[0].Rule != "azure-devops-pat" || hits[0].Length != 84 {
			t.Errorf("%q: want one 84-char azure-devops-pat hit, got %v", line, hits)
		}
	}
}

// TestAzureDevOpsOldPATLeftToKeyContext: a bare 52-char base32 run is far too
// generic to call a PAT, but the same value under a key name is still caught —
// by key-context, not by the PAT rule.
func TestAzureDevOpsOldPATLeftToKeyContext(t *testing.T) {
	if hits := scan(t, "old", adoOldPAT()); len(hits) != 0 {
		t.Errorf("a bare 52-char base32 run fired: %v", hits)
	}
	hits := scan(t, "old", "token = "+adoOldPAT())
	if len(hits) != 1 || hits[0].Rule != "key-context" {
		t.Errorf("token = <old PAT>: want exactly one key-context hit, got %v", hits)
	}
}

// TestAzureDevOpsPATReachesEverySink: a PAT pasted into a PR title or body, or
// into a persisted artifact such as project.toml, is screened like any token.
func TestAzureDevOpsPATReachesEverySink(t *testing.T) {
	type pr struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	for _, body := range []pr{{Title: adoPAT()}, {Title: "t", Body: "see\n" + adoPAT()}} {
		eh, ok := secretscan.AsErrHit(secretscan.ScanPayload("POST /pullrequests", body))
		if !ok || len(eh.Hits) != 1 || eh.Hits[0].Rule != "azure-devops-pat" {
			t.Errorf("payload %+v: want one azure-devops-pat ErrHit, got %v", body, eh)
		}
	}
	toml := "[remote]\n  provider = \"azure-devops\"\n  auth_env = \"" + adoPAT() + "\"\n"
	hits := scan(t, ".dross/project.toml", toml)
	if len(hits) != 1 || hits[0].Rule != "azure-devops-pat" || hits[0].Line != 3 {
		t.Errorf("project.toml: want one azure-devops-pat hit on line 3, got %v", hits)
	}
}

func TestBenignCorpusZeroHits(t *testing.T) {
	for _, row := range benignCorpus() {
		for _, h := range scan(t, "benign", row.line) {
			t.Errorf("benign row %q fired rule %s", row.name, h.Rule)
		}
	}
}

// TestCarveOutsAreNotVacuous: each benign carve-out must be doing work — the
// same row one step past the carve-out fires.
func TestCarveOutsAreNotVacuous(t *testing.T) {
	cases := []struct {
		name, line, rule string
	}{
		{"AKIA+16 not ending EXAMPLE", awsKey(), "aws-access-key"},
		{"repetitive password pushed over the entropy floor", "password = " + strings.Repeat("ab", 10) + "cdefghijklmnop", "key-context"},
		{"15-char secret grown to 16", "secret = " + highEntropy()[:16], "key-context"},
	}
	for _, c := range cases {
		hits := scan(t, "edge", c.line)
		if len(hits) != 1 || hits[0].Rule != c.rule {
			t.Errorf("%s: want one %s hit, got %v", c.name, c.rule, hits)
		}
	}
}

// TestKeyContextIsKeyAware: the identity-id shape is benign only under an
// id/key label. The same 16-hex value under password/token is a finding.
func TestKeyContextIsKeyAware(t *testing.T) {
	for _, line := range []string{`"password": "` + hex16 + `"`, `token = "` + hex16 + `"`} {
		hits := scan(t, "kc", line)
		if len(hits) != 1 || hits[0].Rule != "key-context" {
			t.Errorf("%q: want one key-context hit, got %v", line, hits)
		}
	}
	for _, line := range []string{`"key": "` + hex16 + `"`, "id = " + hex16} {
		if hits := scan(t, "kc", line); len(hits) != 0 {
			t.Errorf("%q: identity id must not fire, got %v", line, hits)
		}
	}
}

func TestPEMBlockReportedOnceAtBeginLine(t *testing.T) {
	block := strings.Join([]string{
		pemHeader(),
		strings.Repeat("MIIE", 16),
		strings.Repeat("vQIB", 16),
		strings.Repeat("AAKC", 16),
		"-----END " + "RSA PRIVATE KEY-----",
	}, "\n")
	hits := scan(t, "key.pem", block)
	if len(hits) != 1 || hits[0].Rule != "pem-private-key" || hits[0].Line != 1 {
		t.Fatalf("want one pem hit at line 1, got %v", hits)
	}
}

func TestAllowMarkerSilencesOnlyItsLine(t *testing.T) {
	m := secretscan.AllowMarker
	t.Run("marker silences its own line only", func(t *testing.T) {
		hits := scan(t, "n", ghTok()+" "+m+"\nclean\n"+ghTok())
		if len(hits) != 1 || hits[0].Line != 3 {
			t.Fatalf("want one hit at line 3, got %v", hits)
		}
	})
	t.Run("marker silences two shapes on one line", func(t *testing.T) {
		if hits := scan(t, "n", ghTok()+" "+glTok()+" "+m); len(hits) != 0 {
			t.Fatalf("want no hits, got %v", hits)
		}
		if hits := scan(t, "n", ghTok()+" "+glTok()); len(hits) != 2 {
			t.Fatalf("control: want two hits without marker, got %v", hits)
		}
	})
	t.Run("marker on the line above changes nothing", func(t *testing.T) {
		if hits := scan(t, "n", m+"\n"+ghTok()); len(hits) != 1 || hits[0].Line != 2 {
			t.Fatalf("want one hit at line 2, got %v", hits)
		}
	})
	t.Run("marker on a clean line changes nothing", func(t *testing.T) {
		if hits := scan(t, "n", "clean "+m+"\n"+ghTok()); len(hits) != 1 || hits[0].Line != 2 {
			t.Fatalf("want one hit at line 2, got %v", hits)
		}
	})
}

func TestLongLineIsScannedToTheEnd(t *testing.T) {
	line := strings.Repeat("x", 200*1024) + " " + ghTok()
	hits := scan(t, "big.json", line)
	if len(hits) != 1 || hits[0].Line != 1 || hits[0].Rule != "github-token" {
		t.Fatalf("want one github hit at :1, got %v", hits)
	}
}

func TestCRLFIsTolerated(t *testing.T) {
	hits := scan(t, "n", "clean\r\n"+ghTok()+"\r\n")
	if len(hits) != 1 || hits[0].Line != 2 {
		t.Fatalf("want one hit at line 2, got %v", hits)
	}
}

// --- c-5: no report path echoes the value ---

// randomLine builds one random instance of rule and returns the line and the
// variable part of the value that no report may contain.
func randomLine(rng *rand.Rand, rule string) (line, variable string) {
	pick := func(class string, n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = class[rng.Intn(len(class))]
		}
		return string(b)
	}
	const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	const value = alnum + "+/=_.~@!#%^*-"
	switch rule {
	case "github-token":
		// Both alternations: the classic 36-char ghp_ and the fine-grained
		// github_pat_ (22+ chars, underscores allowed).
		if rng.Intn(2) == 0 {
			v := pick(alnum, 36)
			return "ghp_" + v, v
		}
		v := pick(alnum+"_", 22+rng.Intn(60))
		return "github_pat_" + v, v
	case "gitlab-pat":
		v := pick(alnum+"_-", 20+rng.Intn(10))
		return "glpat-" + v, v
	case "azure-devops-pat":
		v := pick(alnum, 76) + "AZDO" + pick(alnum, 4)
		return v, v
	case "atlassian-token":
		v := pick(alnum+"_=-", 20+rng.Intn(20))
		return "ATATT" + v, v
	case "aws-access-key":
		v := pick("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", 16)
		for strings.HasSuffix(v, "EXAMPLE") {
			v = pick("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", 16)
		}
		return "AKIA" + v, v
	case "slack-token":
		v := pick(alnum, 10+rng.Intn(10))
		return "xoxb-" + v, v
	case "sk-api-key":
		v := pick(alnum+"_-", 20+rng.Intn(20))
		return "sk-" + v, v
	case "pem-private-key":
		v := pick("ABCDEFGHIJKLMNOPQRSTUVWXYZ", 6+rng.Intn(4))
		return "-----BEGIN " + v + " PRIVATE KEY-----", v
	case "authorization-header":
		scheme := []string{"Basic", "Bearer"}[rng.Intn(2)]
		v := pick(value, 8+rng.Intn(30))
		return "Authorization: " + scheme + " " + v, v
	case "key-context":
		key := []string{"password", "token", "secret", "api_key", "x-api-key", "passwd"}[rng.Intn(6)]
		v := pick(value, 16+rng.Intn(24))
		for len(uniqueBytes(v)) < 9 { // keep entropy comfortably over the floor
			v = pick(value, 16+rng.Intn(24))
		}
		return key + " = " + v, v
	}
	panic("no generator for rule " + rule)
}

func uniqueBytes(s string) map[byte]bool {
	m := map[byte]bool{}
	for i := 0; i < len(s); i++ {
		m[s[i]] = true
	}
	return m
}

func TestReportNeverEchoesTheValue(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, rule := range secretscan.Rules() {
		for i := 0; i < 50; i++ {
			line, variable := randomLine(rng, rule.Name)
			hits := scan(t, "f", line)
			if len(hits) == 0 {
				t.Fatalf("%s: random instance %d did not fire", rule.Name, i)
			}
			err := &secretscan.ErrHit{Hits: hits}
			out := hits[0].String() + secretscan.Report(hits) + err.Error()
			for j := 0; j+6 <= len(variable); j++ {
				if strings.Contains(out, variable[j:j+6]) {
					t.Fatalf("%s: report echoes a window of the value: %q", rule.Name, out)
				}
			}
			if rule.Prefix != "" && !strings.Contains(out, "prefix="+rule.Prefix) {
				t.Errorf("%s: report lacks the fixed prefix: %q", rule.Name, out)
			}
			if rule.Name == "key-context" && !strings.Contains(hits[0].String(), "prefix=)") {
				t.Errorf("key-context: prefix must be empty: %q", hits[0].String())
			}
		}
	}
}

func TestHitLocationIsFileLine(t *testing.T) {
	text := strings.Repeat("clean\n", 6) + ghTok() + "\n"
	hits := scan(t, "notes.md", text)
	if len(hits) != 1 {
		t.Fatalf("want one hit, got %v", hits)
	}
	h := hits[0]
	if h.Location != "notes.md" || h.Line != 7 || !strings.Contains(h.String(), "notes.md:7") {
		t.Fatalf("location drifted: %+v / %s", h, h.String())
	}
	if !strings.Contains(secretscan.Report(hits), secretscan.AllowMarker) {
		t.Fatalf("Report must name the marker in its remedy line: %q", secretscan.Report(hits))
	}
}

// --- payload and argv walks ---

func TestScanPayloadWalksNestedLeaves(t *testing.T) {
	tok := ghTok()
	body := map[string]any{
		"fields": map[string]any{
			"summary": "clean",
			"description": map[string]any{
				"content": []any{map[string]any{"text": tok}},
			},
		},
	}
	err := secretscan.ScanPayload("POST /issue", body)
	var eh *secretscan.ErrHit
	if !errors.As(err, &eh) || len(eh.Hits) != 1 {
		t.Fatalf("want one hit inside *ErrHit, got %v", err)
	}
	if eh.Hits[0].Location != "POST /issue:fields.description.content[0].text" {
		t.Errorf("json path = %q", eh.Hits[0].Location)
	}

	type issue struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	err = secretscan.ScanPayload("x", issue{Title: "t", Body: "l1\n" + tok})
	if !errors.As(err, &eh) || len(eh.Hits) != 1 || eh.Hits[0].Location != "x:body" || eh.Hits[0].Line != 2 {
		t.Errorf("struct body: want x:body line 2, got %v", err)
	}
	if err := secretscan.ScanPayload("x", nil); err != nil {
		t.Errorf("nil body: %v", err)
	}
	err = secretscan.ScanPayload("x", []string{"clean", tok})
	if !errors.As(err, &eh) || len(eh.Hits) != 1 || eh.Hits[0].Location != "x:[1]" {
		t.Errorf("slice: want x:[1], got %v", err)
	}
	if err := secretscan.ScanPayload("x", issue{Title: "t", Body: "16-hex " + hex16}); err != nil {
		t.Errorf("clean struct body refused: %v", err)
	}
}

func TestScanArgvNamesTheFlag(t *testing.T) {
	tok := ghTok()
	err := secretscan.ScanArgv("gh", []string{"pr", "create", "--title", "t", "--body", "l1\n" + tok})
	var eh *secretscan.ErrHit
	if !errors.As(err, &eh) || len(eh.Hits) != 1 || !strings.Contains(eh.Hits[0].String(), "gh --body:2") {
		t.Fatalf("want `gh --body:2`, got %v", err)
	}
	err = secretscan.ScanArgv("gh", []string{"pr", "x", "--", tok})
	if !errors.As(err, &eh) || len(eh.Hits) != 1 || !strings.Contains(eh.Hits[0].String(), "gh argv[3]:1") {
		t.Fatalf("want `gh argv[3]:1`, got %v", err)
	}
	err = secretscan.ScanArgv("gh", []string{"pr", "create", "--body=" + tok})
	if !errors.As(err, &eh) || len(eh.Hits) != 1 || !strings.Contains(eh.Hits[0].String(), "gh --body:1") {
		t.Fatalf("want `gh --body:1` for the = form, got %v", err)
	}
	if err := secretscan.ScanArgv("gh", []string{"pr", "create", "--body", "sha " + sha40()}); err != nil {
		t.Fatalf("clean argv refused: %v", err)
	}
}

func TestErrHitIsUnwrappable(t *testing.T) {
	err := fmt.Errorf("publish: %w", &secretscan.ErrHit{Hits: scan(t, "n", ghTok())})
	if eh, ok := secretscan.AsErrHit(err); !ok || len(eh.Hits) != 1 {
		t.Fatalf("AsErrHit through a wrap: %v", err)
	}
	if _, ok := secretscan.AsErrHit(errors.New("plain")); ok {
		t.Fatal("AsErrHit matched a plain error")
	}
}
