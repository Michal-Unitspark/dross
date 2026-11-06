package cmd

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/forge"
	"github.com/Rivil/dross/internal/hostallow"
)

// Validator/dispatch divergence guard (c-3).
//
// configenum gives every enumerated config value a single definition, but that
// only removes the *validator* copies. The consumers still carry switch
// statements, and a switch is not something a Set can enforce: nothing stops an
// arm being added to OpenPR and forgotten in PostComment, or a host mapping
// being added to remote.go that ship cannot dispatch. That second one is not
// hypothetical — it is precisely how bitbucket came to be written into
// project.toml by a tool that could not ship it.
//
// So these tests read the dispatch and writer sites as source and compare the
// literals they actually branch on against configenum. Adding or removing a
// backend without updating every side breaks the build here, naming the
// function that is out of step.

// providerSwitch is one extracted switch statement: the string literals of its
// case clauses, plus its tag expression rendered back to source.
//
// The tag matters as much as the cases. A switch over strings.ToLower(x)
// accepts a strictly narrower input than one over configenum.Normalize(x) —
// the untrimmed value doctor now blesses would dispatch nowhere — so the tag is
// what TestDispatchUsesConfigenumNormalize asserts on.
type providerSwitch struct {
	cases []string
	tag   string
	// tagSource is the expression that actually produces the switched value.
	// It differs from tag when the site binds the normalised value to a local
	// first (forge.New does), which is the same normalisation written a
	// different way — the guard must judge the shape, not the syntax.
	tagSource string
}

// providerSwitchIn extracts the first tagged switch statement inside funcName.
// funcName matches methods as well as plain functions: the two milestone_mode
// consumers are both methods named EnsureMilestoneEntity, on different types in
// different files.
//
// An untagged switch (switch { case cond: }) is skipped — it branches on
// conditions, not on an enumerated value, so it is never the dispatch site.
func providerSwitchIn(src, funcName string) (providerSwitch, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "src.go", src, 0)
	if err != nil {
		return providerSwitch{}, fmt.Errorf("parse: %w", err)
	}

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == funcName && fd.Body != nil {
			fn = fd
			break
		}
	}
	if fn == nil {
		return providerSwitch{}, fmt.Errorf("no function %q with a body", funcName)
	}

	var found *ast.SwitchStmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if sw, ok := n.(*ast.SwitchStmt); ok && sw.Tag != nil {
			found = sw
			return false
		}
		return true
	})
	if found == nil {
		return providerSwitch{}, fmt.Errorf("%s: no tagged switch statement", funcName)
	}

	out := providerSwitch{tag: types.ExprString(found.Tag)}
	out.tagSource = out.tag
	if ident, ok := found.Tag.(*ast.Ident); ok {
		if src := assignedExpr(fn.Body, ident.Name); src != "" {
			out.tagSource = src
		}
	}
	for _, stmt := range found.Body.List {
		clause, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue // default: has a nil List and contributes nothing
		}
		for _, expr := range clause.List {
			if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					return providerSwitch{}, fmt.Errorf("%s: unquote %s: %w", funcName, lit.Value, err)
				}
				out.cases = append(out.cases, v)
			}
		}
	}
	return out, nil
}

// assignedExpr renders the right-hand side that binds name inside body, or ""
// if nothing does. Only single-value assignments are resolved: a multi-value
// binding (v, ok := m[k]) has no single producing expression, so the caller
// keeps the bare identifier and the site is judged on that — which is correct
// for the writer side, whose value comes out of an already-canonical map.
func assignedExpr(body *ast.BlockStmt, name string) string {
	var out string
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		if ident, ok := as.Lhs[0].(*ast.Ident); ok && ident.Name == name {
			out = types.ExprString(as.Rhs[0])
		}
		return true
	})
	return out
}

// mapValuesIn is the second extraction shape. KnownHostProviders is a
// package-level map var, not a function, so it takes a composite-literal walk
// rather than the switch walk above — the writer side simply is not shaped like
// the dispatch side.
func mapValuesIn(src, varName string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "src.go", src, 0)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	var lit *ast.CompositeLit
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if name.Name != varName || i >= len(vs.Values) {
					continue
				}
				if cl, ok := vs.Values[i].(*ast.CompositeLit); ok {
					lit = cl
				}
			}
		}
	}
	if lit == nil {
		return nil, fmt.Errorf("no map var %q with a composite literal", varName)
	}

	var out []string
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Kind == token.STRING {
			v, err := strconv.Unquote(bl.Value)
			if err != nil {
				return nil, fmt.Errorf("unquote %s: %w", bl.Value, err)
			}
			out = append(out, v)
		}
	}
	return out, nil
}

// --- scan sites -------------------------------------------------------------

// scanSite names one place that branches on an enumerated config value.
//
// minCases guards against a silently-empty extraction: if a dispatch is
// refactored into a map lookup the walker finds nothing, and every set
// comparison below would then pass vacuously. requiresNormalize is false only
// for the writer side, whose switch reads an already-canonical value straight
// out of KnownHostProviders and has nothing to normalise.
type scanSite struct {
	label             string
	path              string
	fn                string
	minCases          int
	requiresNormalize bool
}

var enumScanSites = []scanSite{
	{"ship.OpenPR", "internal/ship/open.go", "OpenPR", 3, true},
	{"ship.PostComment", "internal/ship/comment.go", "PostComment", 3, true},
	{"ship.GetPRStatus", "internal/ship/merged.go", "GetPRStatus", 3, true},
	// ship ci and merge read only two backends; the rest are explicit
	// Unsupported arms, so a new provider still has to choose one.
	{"ship.CIStatus", "internal/ship/ci.go", "CIStatus", 3, true},
	{"ship.MergePR", "internal/ship/merge.go", "MergePR", 3, true},
	{"forge.New", "internal/forge/forge.go", "New", 3, true},
	{"forge.NewBoard", "internal/forge/forge.go", "NewBoard", 4, true},
	{"project.DetectRemote", "internal/project/remote.go", "DetectRemote", 3, false},
	{"forge.YouTrackClient.EnsureMilestoneEntity", "internal/forge/youtrack.go", "EnsureMilestoneEntity", 3, true},
	// jira maps every milestone to a fixVersion, so its switch is legitimately
	// two literals: the empty default arm and "version".
	{"forge.JiraClient.EnsureMilestoneEntity", "internal/forge/jira.go", "EnsureMilestoneEntity", 2, true},
	// The selector styles. minCases is 3 — path, dir and go-package; the ""
	// arm is the no-selector lane and normalizedSet drops it, so it is not
	// counted here either.
	{"testlane.Derive", "internal/testlane/selector.go", "Derive", 3, true},
}

// scanFor extracts the named site's switch, failing the test rather than
// returning an error — a site that cannot be read is a broken guard, not a
// finding about the code under test.
func scanFor(t *testing.T, label string) providerSwitch {
	t.Helper()
	for _, site := range enumScanSites {
		if site.label != label {
			continue
		}
		sw, err := providerSwitchIn(readRepoFile(t, site.path), site.fn)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		return sw
	}
	t.Fatalf("no scan site named %q", label)
	return providerSwitch{}
}

// --- set helpers ------------------------------------------------------------

// normalizedSet drops the empty-string case and de-duplicates. The empty arm is
// a Set's `def` (an unset field falling back to a code default), never one of
// its members, so comparing it against Values() would always report a spurious
// extra.
func normalizedSet(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func diffSets(got, want []string) (missing, extra []string) {
	g, w := map[string]bool{}, map[string]bool{}
	for _, v := range normalizedSet(got) {
		g[v] = true
	}
	for _, v := range normalizedSet(want) {
		w[v] = true
	}
	for v := range w {
		if !g[v] {
			missing = append(missing, v)
		}
	}
	for v := range g {
		if !w[v] {
			extra = append(extra, v)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

func assertSetEqual(t *testing.T, label string, got, want []string) {
	t.Helper()
	missing, extra := diffSets(got, want)
	if len(missing) > 0 {
		t.Errorf("%s does not dispatch %v — the validator accepts values this function cannot handle", label, missing)
	}
	if len(extra) > 0 {
		t.Errorf("%s dispatches %v, which the configenum set does not contain — teach configenum or drop the arm", label, extra)
	}
}

func assertSubset(t *testing.T, label string, got, want []string) {
	t.Helper()
	if _, extra := diffSets(got, want); len(extra) > 0 {
		t.Errorf("%s carries %v, which the configenum set does not contain", label, extra)
	}
}

// --- the guards -------------------------------------------------------------

// The ship entry points must agree with each other and with ShipProviders.
// Implementing a backend in OpenPR and forgetting PostComment yields a repo
// that can open a PR and then cannot comment on it — which is exactly what
// /dross-review does next. ci and merge count an explicit Unsupported arm: a
// provider must be named either way.
func TestShipDispatchMatchesShipProviders(t *testing.T) {
	for _, label := range []string{"ship.OpenPR", "ship.PostComment", "ship.GetPRStatus", "ship.CIStatus", "ship.MergePR"} {
		t.Run(label, func(t *testing.T) {
			assertSetEqual(t, label, scanFor(t, label).cases, configenum.ShipProviders.Values())
		})
	}
}

// The board guard compares the UNION of the two forge constructors. NewBoard
// has no default arm: an unmatched provider falls through to New, so neither
// function alone is the accept-set — comparing only NewBoard would report
// forgejo/gitea/gitlab as unhandled when they are handled one call deeper.
func TestBoardDispatchMatchesBoardProviders(t *testing.T) {
	union := append(scanFor(t, "forge.NewBoard").cases, scanFor(t, "forge.New").cases...)
	assertSetEqual(t, "forge.NewBoard ∪ forge.New", union, configenum.BoardProviders.Values())
}

// The scan above proves azure-devops has a case label; this proves the label
// builds the Azure Boards client — padded and mixed-case spellings included,
// since doctor blesses whatever Normalize accepts.
func TestNewBoardBuildsAzureBoards(t *testing.T) {
	t.Setenv("DROSS_ADO_DISPATCH_PAT", "secret")
	for _, provider := range []string{"azure-devops", " Azure-DevOps ", "AZURE-DEVOPS\t"} {
		c, err := forge.NewBoard(forge.Config{
			Provider: provider, APIBase: "https://dev.azure.com/o", Project: "Fabrikam",
			AuthEnv: "DROSS_ADO_DISPATCH_PAT", Hosts: hostallow.Derive("https://dev.azure.com/o/Fabrikam/_git/r", nil),
		})
		if err != nil {
			t.Fatalf("NewBoard(%q): %v", provider, err)
		}
		if _, ok := c.(*forge.AzureBoardsClient); !ok {
			t.Errorf("NewBoard(%q) built %T, want *forge.AzureBoardsClient — it fell through to forge.New", provider, c)
		}
	}
}

// The writer side is where the original divergence came from: DetectRemote
// wrote bitbucket into project.toml for years before ship could dispatch it.
//
// This is a SUBSET assertion, not equality — the map is keyed by hostname and
// there is no public gitea host to map, so a set-equality assertion would fail
// on day one for a mapping that was never wrong.
func TestRemoteWriterSubsetOfShipProviders(t *testing.T) {
	src := readRepoFile(t, "internal/project/remote.go")
	values, err := mapValuesIn(src, "KnownHostProviders")
	if err != nil {
		t.Fatalf("KnownHostProviders: %v", err)
	}
	if len(values) < 3 {
		t.Fatalf("extracted %d host mappings — the walker is not seeing the map", len(values))
	}
	assertSubset(t, "project.KnownHostProviders", values, configenum.ShipProviders.Values())
}

// DetectRemote's api-base switch, by contrast, IS set-equality: every provider
// ship dispatches needs an API base, and a provider with no arm gets an empty
// one and fails at the first REST call.
func TestRemoteAPIBaseMatchesShipProviders(t *testing.T) {
	assertSetEqual(t, "project.DetectRemote api-base switch",
		scanFor(t, "project.DetectRemote").cases, configenum.ShipProviders.Values())
}

// milestone_mode is the enum most free to drift: unlike the providers it has no
// dispatch table shared between validator and consumer, just two methods with
// the same name on different clients.
func TestMilestoneModeDispatchMatchesConfigenum(t *testing.T) {
	// YouTrack implements every mode, so its switch is the full set.
	assertSetEqual(t, "forge.YouTrackClient.EnsureMilestoneEntity",
		scanFor(t, "forge.YouTrackClient.EnsureMilestoneEntity").cases,
		configenum.MilestoneModes.Values())

	// Jira implements only version, so it is a subset of the union — and an
	// exact match for its own MilestoneModesFor set, which is what doctor's
	// c-5 combination warning reads.
	jira := scanFor(t, "forge.JiraClient.EnsureMilestoneEntity").cases
	assertSubset(t, "forge.JiraClient.EnsureMilestoneEntity", jira, configenum.MilestoneModes.Values())

	modes := configenum.MilestoneModesFor("jira")
	if modes == nil {
		t.Fatal("MilestoneModesFor(\"jira\") is nil — doctor's combination warning has nothing to check against")
	}
	assertSetEqual(t, "forge.JiraClient.EnsureMilestoneEntity vs MilestoneModesFor(jira)",
		jira, modes.Values())
}

// The selector styles are the one enum whose two sides are a config field and a
// pure translation function, with no client object between them. A style added
// to SelectorStyles that Derive cannot shape passes validate and then errors at
// the run site; a case added to Derive that SelectorStyles does not carry is a
// shape validate refuses to let anyone declare. Both directions fail here.
func TestSelectorDispatchMatchesSelectorStyles(t *testing.T) {
	assertSetEqual(t, "testlane.Derive",
		scanFor(t, "testlane.Derive").cases, configenum.SelectorStyles.Values())
}

// Without this, every guard above could pass vacuously: an extraction that
// silently finds nothing has no missing values and no extra ones.
func TestGuardsSeeNonEmptySets(t *testing.T) {
	for _, site := range enumScanSites {
		t.Run(site.label, func(t *testing.T) {
			sw := scanFor(t, site.label)
			if len(sw.cases) < site.minCases {
				t.Errorf("%s yielded %d case literal(s) (want >= %d) — the dispatch was probably refactored out of a switch, and the divergence guards are now blind",
					site.label, len(sw.cases), site.minCases)
			}
		})
	}
}

// The leniency guard, and the reason c-4 holds by construction rather than by
// vigilance. doctor validates through configenum.Normalize (trim + lowercase);
// a consumer switching on bare strings.ToLower rejects the padded value doctor
// just blessed, which is the original c-4 bug in mirror image.
func TestDispatchUsesConfigenumNormalize(t *testing.T) {
	for _, site := range enumScanSites {
		if !site.requiresNormalize {
			// The writer side branches on a value read straight out of
			// KnownHostProviders — already canonical, nothing to normalise.
			continue
		}
		t.Run(site.label, func(t *testing.T) {
			src := scanFor(t, site.label).tagSource
			if strings.Contains(src, "strings.ToLower(") {
				t.Errorf("%s switches on %s — bare ToLower does not trim, so it rejects the padded value doctor accepts", site.label, src)
			}
			if !strings.Contains(src, "configenum.Normalize(") {
				t.Errorf("%s switches on %s — use configenum.Normalize so this site is exactly as forgiving as doctor", site.label, src)
			}
		})
	}
}

// --- walker self-tests ------------------------------------------------------

// Both extraction shapes are tested against in-test source, so a walker that
// stops finding anything fails here — loudly and on a fixture it fully controls
// — rather than turning every guard above into a no-op.
func TestProviderCasesInParsesFixture(t *testing.T) {
	const src = `package p

func Untagged(v string) int {
	switch {
	case v != "":
		return 1
	}
	return 0
}

func Dispatch(v string) int {
	switch configenum.Normalize(v) {
	case "alpha":
		return 1
	case "beta", "gamma":
		return 2
	default:
		return 0
	}
}

func Indirect(v string) int {
	p := configenum.Normalize(v)
	switch p {
	case "alpha":
		return 1
	}
	return 0
}

func Sloppy(v string) int {
	p := strings.ToLower(v)
	switch p {
	case "alpha":
		return 1
	}
	return 0
}

func NoSwitch() {}
`
	sw, err := providerSwitchIn(src, "Dispatch")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	assertSetEqual(t, "fixture Dispatch", sw.cases, []string{"alpha", "beta", "gamma"})
	if sw.tag != "configenum.Normalize(v)" {
		t.Errorf("tag = %q, want %q", sw.tag, "configenum.Normalize(v)")
	}

	// Binding the normalised value to a local first is the same shape written
	// differently — forge.New does exactly this, and judging it on the bare
	// identifier would report a false divergence.
	indirect, err := providerSwitchIn(src, "Indirect")
	if err != nil {
		t.Fatalf("Indirect: %v", err)
	}
	if indirect.tag != "p" {
		t.Errorf("tag = %q, want the bare identifier %q", indirect.tag, "p")
	}
	if indirect.tagSource != "configenum.Normalize(v)" {
		t.Errorf("tagSource = %q — the binding was not resolved, so a local-variable site reads as un-normalised", indirect.tagSource)
	}

	// The relaxation must not swallow the failure it exists to catch.
	sloppy, err := providerSwitchIn(src, "Sloppy")
	if err != nil {
		t.Fatalf("Sloppy: %v", err)
	}
	if sloppy.tagSource != "strings.ToLower(v)" {
		t.Errorf("tagSource = %q — a bare ToLower behind a local must still be visible", sloppy.tagSource)
	}

	// An untagged switch branches on conditions, not on an enumerated value.
	if _, err := providerSwitchIn(src, "Untagged"); err == nil {
		t.Error("an untagged switch must not be mistaken for a dispatch site")
	}
	if _, err := providerSwitchIn(src, "NoSwitch"); err == nil {
		t.Error("a function with no switch must report an error, not an empty set")
	}
	if _, err := providerSwitchIn(src, "Absent"); err == nil {
		t.Error("a missing function must report an error, not an empty set")
	}
}

func TestMapValuesInParsesFixture(t *testing.T) {
	const src = `package p

var Hosts = map[string]string{
	"a.example": "alpha",
	"b.example": "beta",
}

var NotAMap = 3
`
	got, err := mapValuesIn(src, "Hosts")
	if err != nil {
		t.Fatalf("Hosts: %v", err)
	}
	assertSetEqual(t, "fixture Hosts", got, []string{"alpha", "beta"})

	if _, err := mapValuesIn(src, "NotAMap"); err == nil {
		t.Error("a non-composite var must report an error, not an empty set")
	}
	if _, err := mapValuesIn(src, "Absent"); err == nil {
		t.Error("a missing var must report an error, not an empty set")
	}
}
