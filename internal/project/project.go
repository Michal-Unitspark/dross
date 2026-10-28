// Package project handles .dross/project.toml — the long-lived
// per-repo identity, stack, runtime, and constraints.
package project

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// File is the canonical filename inside .dross/.
const File = "project.toml"

// Project is the top-level schema.
type Project struct {
	Project     ProjectMeta       `toml:"project" json:"project"`
	Stack       Stack             `toml:"stack" json:"stack"`
	Runtime     Runtime           `toml:"runtime" json:"runtime"`
	Repo        Repo              `toml:"repo" json:"repo"`
	Remote      Remote            `toml:"remote,omitempty" json:"remote,omitempty"`
	Board       Board             `toml:"board,omitempty" json:"board,omitempty"`
	Paths       Paths             `toml:"paths" json:"paths"`
	Env         Env               `toml:"env" json:"env"`
	Goals       Goals             `toml:"goals" json:"goals"`
	Mutation    Mutation          `toml:"mutation,omitempty" json:"mutation,omitempty"`
	Techdebt    Techdebt          `toml:"techdebt,omitempty" json:"techdebt,omitempty"`
	Constraints map[string]string `toml:"constraints,omitempty" json:"constraints,omitempty"`
	Competition []Competitor      `toml:"competition,omitempty" json:"competition,omitempty"`
}

type ProjectMeta struct {
	Name        string `toml:"name" json:"name"`
	Version     string `toml:"version" json:"version"` // 4-part: major.minor.patch.internal
	Description string `toml:"description,omitempty" json:"description,omitempty"`
	Created     string `toml:"created" json:"created"`
}

type Stack struct {
	Languages      []string       `toml:"languages" json:"languages"`
	Profile        string         `toml:"profile,omitempty" json:"profile,omitempty"` // matched stack-profile id (internal/stack)
	Frameworks     []string       `toml:"frameworks,omitempty" json:"frameworks,omitempty"`
	PackageManager string         `toml:"package_manager,omitempty" json:"package_manager,omitempty"`
	TypeChecker    string         `toml:"type_checker,omitempty" json:"type_checker,omitempty"`
	Linter         string         `toml:"linter,omitempty" json:"linter,omitempty"`
	Formatter      string         `toml:"formatter,omitempty" json:"formatter,omitempty"`
	TestRunner     string         `toml:"test_runner,omitempty" json:"test_runner,omitempty"`
	E2ERunner      string         `toml:"e2e_runner,omitempty" json:"e2e_runner,omitempty"`
	Locked         []LockedChoice `toml:"locked,omitempty" json:"locked,omitempty"`
}

type LockedChoice struct {
	Choice   string `toml:"choice" json:"choice"`
	Why      string `toml:"why" json:"why"`
	LockedAt string `toml:"locked_at" json:"locked_at"`
}

// Runtime is the pain-point-killer section. Capture exact commands
// so Claude never guesses pnpm/npm/docker again.
type Runtime struct {
	// Mode is docker | native — see configenum.RuntimeModes, which is the
	// set both gates read. "hybrid" was a third accepted spelling whose only
	// consumer was `Mode != "docker"`, so it compiled to native; a per-service
	// split is what Services below already carries.
	Mode             string             `toml:"mode" json:"mode"`
	DevCommand       string             `toml:"dev_command,omitempty" json:"dev_command,omitempty"`
	StopCommand      string             `toml:"stop_command,omitempty" json:"stop_command,omitempty"`
	TestCommand      string             `toml:"test_command,omitempty" json:"test_command,omitempty"`
	TestWatch        string             `toml:"test_watch,omitempty" json:"test_watch,omitempty"`
	E2ECommand       string             `toml:"e2e_command,omitempty" json:"e2e_command,omitempty"`
	TypecheckCommand string             `toml:"typecheck_command,omitempty" json:"typecheck_command,omitempty"`
	LintCommand      string             `toml:"lint_command,omitempty" json:"lint_command,omitempty"`
	FormatCommand    string             `toml:"format_command,omitempty" json:"format_command,omitempty"`
	BuildCommand     string             `toml:"build_command,omitempty" json:"build_command,omitempty"`
	MigrateCommand   string             `toml:"migrate_command,omitempty" json:"migrate_command,omitempty"`
	SeedCommand      string             `toml:"seed_command,omitempty" json:"seed_command,omitempty"`
	ShellCommand     string             `toml:"shell_command,omitempty" json:"shell_command,omitempty"`
	LogsCommand      string             `toml:"logs_command,omitempty" json:"logs_command,omitempty"`
	Services         map[string]Service `toml:"services,omitempty" json:"services,omitempty"`

	// TestLane declares the optional [[runtime.test_lane]] blocks that let
	// `dross test --files` run only the suites a file set actually touches.
	//
	// omitempty is load-bearing beyond tidiness: a repo that declares no lane
	// must round-trip byte-identically to one written before lanes existed, so
	// the whole feature stays opt-in and `dross test` keeps meaning
	// TestCommand for every repo that has not asked for anything else.
	TestLane []TestLane `toml:"test_lane,omitempty" json:"test_lane,omitempty"`
}

// TestLane is one named subset of the repo paired with the command that tests
// it: Match is a list of globs, Command is the line to run when a supplied file
// set hits any of them.
//
// Those three fields are required and validate says so per-field — a lane with
// no Command cannot be run, a lane with no Match can never be selected, and a
// lane with no Name cannot be granted consent, since the machine-local grant
// store is keyed by lane name. Prepare, Selector and EmptyExit are optional and
// opt-in; see their own comments.
type TestLane struct {
	Name    string   `toml:"name" json:"name"`
	Match   []string `toml:"match" json:"match"`
	Command string   `toml:"command" json:"command"`

	// Prepare is the optional bootstrap line this lane runs before its
	// Command — the `make build` or `docker compose up -d` a cold host needs
	// before the suite means anything. Omitted means the lane spawns exactly
	// what it spawns today, so the field is opt-in per lane like Selector.
	//
	// It runs on the same host and through the same transport as Command, and
	// it is covered by the SAME consent grant: the lane's fingerprint is taken
	// over both lines together, so appending a prepare to a granted lane
	// staleness-refuses it rather than smuggling an untrusted line past a
	// grant issued for the command alone.
	Prepare string `toml:"prepare,omitempty" json:"prepare,omitempty"`

	// Selector names the shape the lane's matched paths take when they are
	// appended to Command — one of configenum.SelectorStyles. Omitted means
	// append nothing, which is what every lane written before selectors
	// existed means, so translation is opt-in per lane and a lane that says
	// nothing keeps spawning Command byte-for-byte.
	Selector string `toml:"selector,omitempty" json:"selector,omitempty"`

	// SelectorTemplate names WHERE this lane's matched paths land on its
	// command line, for a runner whose shape the Selector enum cannot express:
	// cargo wants `--package <p>` repeated per path, `ctest -R` wants one
	// joined regex. Omitted — the normal case — means the derived paths are
	// appended to Command exactly as they are today, so the field is opt-in
	// per lane like Selector itself.
	//
	// It is orthogonal to Selector rather than a replacement for it: Selector
	// still decides whether the substituted values are files, dirs or Go
	// packages, and the template decides where they go. A template declared
	// with no Selector is therefore refused — it would have nothing to place.
	//
	// Two placeholders are recognised. `{path}` repeats the WHOLE template
	// once per derived path; `{paths}` substitutes them all into a single
	// instance. A template containing neither is refused, since it would scope
	// nothing while claiming to. Any other `{...}` run is ordinary template
	// text — a regex quantifier like `a{2,3}` is legitimate here.
	//
	// Its content is fenced by consent alone, exactly as Command and Prepare
	// are: it is folded into the lane's consent line, so adding or changing a
	// template leaves the grant stale rather than running an unread line under
	// a grant issued before it.
	SelectorTemplate string `toml:"selector_template,omitempty" json:"selector_template,omitempty"`

	// SelectorJoin collapses a `{paths}` expansion into ONE argv token,
	// separated by this string — `selector_join = "|"` is what turns `-R
	// {paths}` into `-R 'a|b'` for ctest. Omitted, `{paths}` expands to
	// separate tokens, which is what a trailing path list wants.
	//
	// A regex alternation is unreachable without a separator, and an inline
	// `{paths:|}` syntax would make the template a small language with its own
	// parse errors to name — so it is a declared field. validate refuses it on
	// a lane whose template has no `{paths}`, where it could never apply.
	SelectorJoin string `toml:"selector_join,omitempty" json:"selector_join,omitempty"`

	// Toolchain overrides the binaries this lane needs on the host that runs
	// it. Omitted — the normal case — means the list is DERIVED from the first
	// token of Command and Prepare (testlane.Toolchain), so locality detection
	// works on every lane already declared without an edit.
	//
	// The field is the escape hatch for the lines derivation gets wrong: an env
	// prefix like `FOO=1 go test`, a wrapper script, a `mise exec`. It replaces
	// the derived list wholesale rather than extending it, because appending
	// would keep probing the very token the user overrode to say was not a
	// binary.
	//
	// Every entry must be a bare binary name — validate refuses blanks,
	// embedded whitespace, `=` and path separators — since each entry is asked
	// of a host as `command -v <entry>` and an entry that can never resolve
	// would pin the lane to a local run with no message pointing at the cause.
	Toolchain []string `toml:"toolchain,omitempty" json:"toolchain,omitempty"`

	// Install is the optional line that installs this lane's toolchain onto
	// the machine that is missing it. Omitted — the normal case — means the
	// lane installs from dross's built-in recipe for the tool, or is refused
	// by name when there is none: an install command is never guessed at.
	//
	// Declared, it REPLACES the built-in recipe for this lane's tool rather
	// than extending it, for the reason Toolchain replaces its derived list:
	// appending would keep running the very recipe the user overrode to say
	// was wrong. It is also the only route to installing something the
	// built-in table deliberately refuses — a language runtime — because that
	// choice then belongs to the person who owns the host rather than to
	// dross.
	//
	// It carries its OWN consent grant, separate from the command+prepare
	// grant the lane's test runs bind to. Folding it into that fingerprint the
	// way Prepare is folded in would staleness-refuse a lane's ordinary test
	// runs the moment an install line was added — a line that has never
	// executed breaking a gate that was passing — and installing is a
	// different act with a different blast radius than running a suite.
	Install string `toml:"install,omitempty" json:"install,omitempty"`

	// EmptyExit lists the exit codes this lane's runner uses to say "I
	// collected no tests" — pytest's 5, for example. dross reports those as a
	// selector miss rather than a red suite. It is declared, never inferred:
	// with no codes listed, only a selector that filtered down to nothing can
	// produce a miss, and a runner's output is never scraped for wording.
	//
	// It is meaningless without Selector — an unscoped lane runs its whole
	// suite and can only collect nothing if the repo has no tests at all — so
	// validate refuses the combination rather than letting a user believe they
	// configured a code that can never fire.
	EmptyExit []int `toml:"empty_exit,omitempty" json:"empty_exit,omitempty"`
}

type Service struct {
	URL    string `toml:"url,omitempty" json:"url,omitempty"`
	Health string `toml:"health,omitempty" json:"health,omitempty"`
	Admin  string `toml:"admin,omitempty" json:"admin,omitempty"`
}

type Repo struct {
	Layout           string   `toml:"layout" json:"layout"` // single | monorepo
	RootRunDir       string   `toml:"root_run_dir,omitempty" json:"root_run_dir,omitempty"`
	Workspaces       []string `toml:"workspaces,omitempty" json:"workspaces,omitempty"`
	GitMainBranch    string   `toml:"git_main_branch" json:"git_main_branch"`
	BranchPattern    string   `toml:"branch_pattern,omitempty" json:"branch_pattern,omitempty"`
	CommitConvention string   `toml:"commit_convention,omitempty" json:"commit_convention,omitempty"` // conventional | freeform
	SquashMerge      bool     `toml:"squash_merge" json:"squash_merge"`
}

// Remote describes the canonical hosting destination for the repo.
// Separated from Repo (which holds branch/layout policy) because hosting
// + auth + reviewer config travels with the code, not the local checkout.
type Remote struct {
	URL        string     `toml:"url,omitempty" json:"url,omitempty"`                 // canonical https URL of the repo
	Provider   string     `toml:"provider,omitempty" json:"provider,omitempty"`       // github | forgejo | gitea | gitlab | bitbucket, or none for no remote (configenum.ShipProviders)
	Public     bool       `toml:"public,omitempty" json:"public,omitempty"`           // true if cloud agents can clone
	APIBase    string     `toml:"api_base,omitempty" json:"api_base,omitempty"`       // override; default derived from provider+URL
	LogAPI     bool       `toml:"log_api,omitempty" json:"log_api,omitempty"`         // instance exposes CI logs via API
	AuthEnv    string     `toml:"auth_env,omitempty" json:"auth_env,omitempty"`       // env var name (NEVER the value)
	AuthUser   string     `toml:"auth_user,omitempty" json:"auth_user,omitempty"`     // bitbucket: account user for HTTP Basic auth (user:token)
	AuthScheme string     `toml:"auth_scheme,omitempty" json:"auth_scheme,omitempty"` // private-token (default) | bearer | basic (configenum.AuthSchemes); basic needs auth_user
	ProjectID  string     `toml:"project_id,omitempty" json:"project_id,omitempty"`   // gitlab: numeric project-id override (else derived from URL)
	APIVersion APIVersion `toml:"api_version,omitempty" json:"api_version,omitempty"` // azure-devops: api-version on every call; empty = 7.1 (Server 2019 5.0, 2020 6.0, 2022 7.0)
	Reviewers  []string   `toml:"reviewers,omitempty" json:"reviewers,omitempty"`     // default human reviewers for /dross-ship
}

// APIVersion is [remote].api_version. It decodes from a string or from a bare
// TOML number: `api_version = 6.0` is what a hand-editor writes, and a float
// would otherwise fail the whole file's load. A number always keeps its minor
// part, so 6.0 is "6.0", never "6". It is written back as a string.
type APIVersion string

// UnmarshalTOML accepts a string verbatim, and a float or integer as its
// major.minor spelling.
func (v *APIVersion) UnmarshalTOML(data any) error {
	switch x := data.(type) {
	case string:
		*v = APIVersion(x)
	case float64:
		s := strconv.FormatFloat(x, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		*v = APIVersion(s)
	case int64:
		*v = APIVersion(strconv.FormatInt(x, 10) + ".0")
	default:
		return fmt.Errorf("remote.api_version: want a version such as \"7.1\", got a %T", data)
	}
	return nil
}

// Board describes the issue-tracker destination for board sync, kept separate
// from Remote so a repo can ship code to one host ([remote]) while tracking
// issues on another ([board]) — e.g. ship to GitLab, track in YouTrack. It is
// the single source for `dross issue` board operations; there is no [remote]
// fallback.
type Board struct {
	Provider      string            `toml:"provider,omitempty" json:"provider,omitempty"`             // forgejo | gitea | gitlab | youtrack | jira | github
	BaseURL       string            `toml:"base_url,omitempty" json:"base_url,omitempty"`             // instance base URL of the tracker
	AuthEnv       string            `toml:"auth_env,omitempty" json:"auth_env,omitempty"`             // env var name holding the token (NEVER the value)
	AuthUser      string            `toml:"auth_user,omitempty" json:"auth_user,omitempty"`           // jira: account email for HTTP Basic auth (email:token)
	Project       string            `toml:"project,omitempty" json:"project,omitempty"`               // project short-name / key on the tracker; github: "owner/repo"
	GitHubProject string            `toml:"github_project,omitempty" json:"github_project,omitempty"` // github: Projects v2 board node id to add created issues to
	Enabled       bool              `toml:"enabled,omitempty" json:"enabled,omitempty"`               // board sync is on
	MilestoneMode string            `toml:"milestone_mode,omitempty" json:"milestone_mode,omitempty"` // version (default) | agile | epic
	StateMap      map[string]string `toml:"state_map,omitempty" json:"state_map,omitempty"`           // dross lifecycle state → tracker State value override
	Fields        BoardFields       `toml:"fields,omitempty" json:"fields,omitempty"`                 // tracker-native field-name overrides
}

// BoardFields overrides the tracker-native field names board sync writes to.
// Every key defaults to the literal the provider ships with, so an untouched
// project syncs exactly as it did before — but a project that renamed a field
// (or runs a non-English tracker UI) no longer needs a code change.
//
// Scoped to YouTrack today, which is the only provider this repo runs against
// and therefore the only one where the fix is provable; the Jira and GitHub
// halves are deferred to the provider work itself.
type BoardFields struct {
	State       string `toml:"state,omitempty" json:"state,omitempty"`               // youtrack: the State custom field (default "State")
	Type        string `toml:"type,omitempty" json:"type,omitempty"`                 // youtrack: the issue-type custom field (default "Type")
	FixVersions string `toml:"fix_versions,omitempty" json:"fix_versions,omitempty"` // youtrack: the version bundle field (default "Fix versions")
}

type Paths struct {
	Source     string `toml:"source,omitempty" json:"source,omitempty"`
	Tests      string `toml:"tests,omitempty" json:"tests,omitempty"`
	E2E        string `toml:"e2e,omitempty" json:"e2e,omitempty"`
	Migrations string `toml:"migrations,omitempty" json:"migrations,omitempty"`
	Schemas    string `toml:"schemas,omitempty" json:"schemas,omitempty"`
	I18n       string `toml:"i18n,omitempty" json:"i18n,omitempty"`
	Public     string `toml:"public,omitempty" json:"public,omitempty"`
}

type Env struct {
	Files           []string `toml:"files,omitempty" json:"files,omitempty"`                       // load order
	SecretsLocation string   `toml:"secrets_location,omitempty" json:"secrets_location,omitempty"` // vault | doppler | 1password | local
	Gitignored      bool     `toml:"gitignored,omitempty" json:"gitignored,omitempty"`
}

type Goals struct {
	CoreValue       string   `toml:"core_value,omitempty" json:"core_value,omitempty"`
	Audience        string   `toml:"audience,omitempty" json:"audience,omitempty"`
	NonGoals        []string `toml:"non_goals,omitempty" json:"non_goals,omitempty"`
	Differentiators []string `toml:"differentiators,omitempty" json:"differentiators,omitempty"`
}

// Techdebt holds the knobs for `dross techdebt`. The table is optional; an
// absent one leaves Exclude nil and the scan covers every tracked file outside
// the shared skip set (stack.SkipDirs).
type Techdebt struct {
	// Exclude lists repo-relative patterns the tech-debt scan leaves out — the
	// one mechanism for exempting a package (dross's own internal/techdebt/,
	// whose marker regex and fixtures would otherwise report themselves) or a
	// file class, covering .go, .md and .toml alike. Semantics, in order:
	//   - an entry ending in "/" is a directory prefix ("internal/techdebt/"
	//     drops everything beneath it, but not internal/techdebtx/);
	//   - any other entry is a path.Match glob against the repo-relative slash
	//     path and, when the pattern has no "/", also against the base name
	//     ("*.golden" drops docs/a.golden as well as a.golden).
	// A pattern that does not compile is a scan-time error, not a silent
	// no-op; validate does not pre-check globs (deferred).
	Exclude []string `toml:"exclude,omitempty" json:"exclude,omitempty"`
}

// Mutation holds per-adapter knobs for the mutation testing pipeline.
// Each sub-table is optional; unset values fall back to the adapter's
// built-in default.
type Mutation struct {
	// Adapters is an allowlist of adapter names ("gremlins", "stryker",
	// "stryker.net"). Non-empty means run ONLY these; files whose adapter
	// is filtered out fall into verify's Skipped path ("no mutation adapter
	// for .ts"). The escape hatch for a polyglot repo where one adapter
	// isn't set up yet.
	Adapters []string         `toml:"adapters,omitempty" json:"adapters,omitempty"`
	Gremlins MutationGremlins `toml:"gremlins,omitempty" json:"gremlins,omitempty"`
	Stryker  MutationStryker  `toml:"stryker,omitempty" json:"stryker,omitempty"`

	// RemoteHost and RemoteWorkdir are TRAP fields. They configure nothing.
	// Their only purpose is to exist so that Load can refuse them by name.
	//
	// A remote host is authorization to rsync the working tree to another
	// machine and execute the test suite there. project.toml is TRACKED, so a
	// host committed here would be the repo authorizing itself — the same
	// self-authorizing shape allow_hosts is kept out of project.toml to avoid.
	// The grant belongs in the untracked machine-local store, written by
	// `dross mutation remote grant`, which prints what it is authorizing first.
	//
	// The fields have to be DECLARED to be refused. toml.DecodeFile ignores
	// keys with no matching field, silently — so without these, a committed
	// remote_host would neither work nor complain, and the user would be left
	// wondering why their remote never engaged. The trap turns silence into a
	// refusal that names the fix.
	//
	// The json tags mirror the toml ones because TestTomlFieldsCarryMatchingJSONTags
	// requires every field to, and there is no reason to carve an exception:
	// Load refuses any non-empty value, so a Project that exists always has
	// these empty and omitempty keeps them out of every serialization.
	RemoteHost    string `toml:"remote_host,omitempty" json:"remote_host,omitempty"`
	RemoteWorkdir string `toml:"remote_workdir,omitempty" json:"remote_workdir,omitempty"`
}

// refuseRemote rejects a remote configured in tracked project.toml.
//
// Either field alone is enough. Half a config is not a bypass: the point is
// not that the pair would work, it is that project.toml is the wrong file to
// express any of it in, and a partial attempt is a user who needs the same
// message as a complete one.
func (m Mutation) refuseRemote(path string) error {
	for _, f := range []struct{ key, val string }{
		{"mutation.remote_host", m.RemoteHost},
		{"mutation.remote_workdir", m.RemoteWorkdir},
	} {
		if f.val == "" {
			continue
		}
		return fmt.Errorf(
			"refusing to load %s: it sets %s = %q.\n\n"+
				"A remote mutation host is authorization to copy this working tree to\n"+
				"another machine and run its test suite there. project.toml is tracked, so a\n"+
				"host set here would let the repo authorize itself.\n\n"+
				"Remove the key and grant the host on this machine instead:\n\n"+
				"    dross mutation remote grant <host> <workdir>",
			path, f.key, f.val)
	}
	return nil
}

// MutationStryker surfaces the stryker adapter's tunable settings.
//
// Workdir is the repo-relative package that hosts stryker and its config in
// a monorepo (e.g. "web" when vitest + stryker.config.json live in web/).
// The adapter runs there, strips the prefix from --mutate paths, and
// re-prefixes report paths so tests.json stays repo-relative.
type MutationStryker struct {
	Workdir string `toml:"workdir,omitempty" json:"workdir,omitempty"`
}

// MutationGremlins surfaces the gremlins adapter's tunable settings.
//
// TimeoutCoefficient overrides gremlins' --timeout-coefficient flag.
// Gremlins multiplies this by the baseline test duration to decide
// per-mutant timeout. The tool's built-in default (~3) is too tight
// for Go projects with fast test suites: a 75ms baseline yields a
// 0.22s budget per mutant, far below Go's 1–2s compile-and-test cycle,
// and most mutants get classified TIMED OUT before they can be killed
// or surviving. Dross overrides this default to 30 unless the project
// sets a different value.
type MutationGremlins struct {
	TimeoutCoefficient int `toml:"timeout_coefficient,omitempty" json:"timeout_coefficient,omitempty"`
}

type Competitor struct {
	Name           string `toml:"name" json:"name"`
	URL            string `toml:"url,omitempty" json:"url,omitempty"`
	WhatTheyDo     string `toml:"what_they_do,omitempty" json:"what_they_do,omitempty"`
	Differentiator string `toml:"differentiator,omitempty" json:"differentiator,omitempty"`
}

// Load reads a project.toml file.
func Load(path string) (*Project, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return decode(src, path)
}

// decode is Load on bytes already in hand — the same decoder and the same
// refusals, so a patched document is judged exactly as the file would be.
func decode(src []byte, path string) (*Project, error) {
	v, err := decodeLike(&Project{}, src, path)
	if err != nil {
		return nil, err
	}
	return v.(*Project), nil
}

// checkLoaded is the refusal a Project applies to every document it loads.
// A nil Project goes back alongside the error, not a partly-usable one: no
// caller can safely proceed on a config dross has just said it refuses to
// honour.
func (p *Project) checkLoaded(path string) error {
	return p.Mutation.refuseRemote(path)
}

// loadChecker is a document type that refuses some decodable contents. The
// door runs it on the file it reads and on the text it is about to write, so a
// patched document is refused exactly as Load would refuse the file.
type loadChecker interface {
	checkLoaded(path string) error
}

// checkDocPtr refuses anything but a non-nil pointer to a struct — the only
// shape the door can decode a file back into.
func checkDocPtr(v any, path string) error {
	rt := reflect.TypeOf(v)
	if rt == nil || rt.Kind() != reflect.Pointer || rt.Elem().Kind() != reflect.Struct || reflect.ValueOf(v).IsNil() {
		return fmt.Errorf("save %s: want a non-nil pointer to a struct, got %T", path, v)
	}
	return nil
}

// decodeLike decodes src into a fresh value of v's type (v must be a non-nil
// pointer to a struct), then runs the type's loadChecker if it has one.
func decodeLike(v any, src []byte, path string) (any, error) {
	if err := checkDocPtr(v, path); err != nil {
		return nil, err
	}
	out := reflect.New(reflect.TypeOf(v).Elem()).Interface()
	if _, err := toml.Decode(string(src), out); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if c, ok := out.(loadChecker); ok {
		if err := c.checkLoaded(path); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// planOps is the diff step Save runs, as a variable so a test can hand Save
// an op the patcher renders wrongly and prove the verify step refuses it.
var planOps = func(old, new *Project) ([]op, error) { return diff(old, new) }

// Save writes p to path. It is the ONLY project.toml writer in dross.
//
// A path that does not exist yet gets the encoder's fresh document. A path
// that does is PATCHED: the file is loaded, diffed against p, and only the
// lines the differing fields occupy are rewritten — comments, hand-added
// keys, indentation and line endings elsewhere survive byte-for-byte. The
// patched text is then decoded and re-encoded canonically and must match p's
// own canonical form, so a patcher bug can never replace the file with a
// document that loads differently; on mismatch the file is left untouched.
//
// A file Load refuses (the remote_host trap, a decode error) is never fallen
// back to a whole-file encode: the error is returned and the bytes stay.
// No differing field means no write at all — not even an mtime bump.
//
// The bytes land via a temp file in the same directory, fsynced and renamed
// over the original with its mode preserved, so a crash mid-write leaves
// either the old document or the new one, never a truncated one.
//
// All of that is saveLossless, the door every lossless TOML writer shares;
// Save supplies only its own diff seam, planOps.
func (p *Project) Save(path string) error {
	return saveLossless(path, p, func(old, new any) ([]op, error) {
		return planOps(old.(*Project), new.(*Project))
	})
}

// saveLossless is the lossless write door: Project.Save and SaveTOML both
// write through it, and nothing else in the package writes a file. v is a
// non-nil pointer to a TOML-tagged struct; plan diffs the document on disk
// (decoded into v's type) against v.
//
// An absent path gets the encoder's fresh document. An existing one is read,
// decoded (with v's type's loadChecker), diffed, patched, verified to load as
// v, and only then written — atomically, with its mode kept. No differing
// field means no write at all.
func saveLossless(path string, v any, plan func(old, new any) ([]op, error)) error {
	// Checked before the fresh-encode branch too: a struct value would encode
	// fine on first write and then make every later save fail to decode.
	if err := checkDocPtr(v, path); err != nil {
		return err
	}
	name := filepath.Base(path)
	existing, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("read %s: %w", path, err)
		}
		fresh, err := encodeFresh(v)
		if err != nil {
			return fmt.Errorf("encode %s: %w", name, err)
		}
		return writeAtomic(path, fresh, 0o644)
	}

	old, err := decodeLike(v, existing, path)
	if err != nil {
		return err
	}
	ops, err := plan(old, v)
	if err != nil {
		return fmt.Errorf("diff %s: %w", path, err)
	}
	if len(ops) == 0 {
		return nil
	}
	patched, err := apply(existing, ops)
	if err != nil {
		return fmt.Errorf("patch %s: %w", path, err)
	}
	if err := verifyPatched(patched, v, path); err != nil {
		return err
	}

	// A read-only file refuses the write the way os.Create did. The rename
	// below would replace it regardless — only the directory's permission
	// gates a rename — and a permission the user set on the file is not
	// something atomicity should route around.
	if f, err := os.OpenFile(path, os.O_WRONLY, 0); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	} else {
		f.Close()
	}
	mode := fs.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	return writeAtomic(path, patched, mode)
}

// verifyPatched proves the patched text loads as v: both are re-encoded
// canonically, so nil-vs-empty slices and map order cannot false-positive,
// and the first differing line is named so the failing key is in the error.
func verifyPatched(patched []byte, v any, path string) error {
	name := filepath.Base(path)
	got, err := decodeLike(v, patched, path)
	if err != nil {
		return fmt.Errorf("patched %s does not load; the file was left untouched: %w", path, err)
	}
	want, err := encodeFresh(v)
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	have, err := encodeFresh(got)
	if err != nil {
		return fmt.Errorf("encode patched %s: %w", name, err)
	}
	if bytes.Equal(have, want) {
		return nil
	}
	wl, hl := strings.Split(string(want), "\n"), strings.Split(string(have), "\n")
	for i := 0; i < len(wl) || i < len(hl); i++ {
		var w, h string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(hl) {
			h = hl[i]
		}
		if w != h {
			return fmt.Errorf("patched %s would not load as saved at %q (wanted %q); the file was left untouched",
				path, strings.TrimSpace(h), strings.TrimSpace(w))
		}
	}
	return fmt.Errorf("patched %s would not load as saved; the file was left untouched", path)
}

// writeAtomic writes data to path through a temp file in the same directory,
// fsynced and renamed into place, with the requested mode.
func writeAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		cleanup()
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}
