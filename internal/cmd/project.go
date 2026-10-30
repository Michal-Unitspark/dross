package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/project"
	"github.com/Rivil/dross/internal/render"
)

func Project() *cobra.Command {
	c := &cobra.Command{
		Use:   "project",
		Short: "Read and edit .dross/project.toml",
	}
	c.AddCommand(projectShow(), projectSet(), projectGet())
	return c
}

func projectShow() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "show",
		Short: "Print project.toml",
		RunE: func(_ *cobra.Command, _ []string) error {
			p, path, err := loadProject()
			if err != nil {
				return err
			}
			if asJSON {
				return emitJSON(p)
			}
			Printf("# %s\n", path)
			return render.TOML(os.Stdout, p)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, jsonFlagUsage)
	return c
}

// projectGet prints one or more dotted-path fields (e.g. project.name,
// runtime.mode). One path prints its bare value exactly as it always has; two
// or more emit a single keyed JSON object (locked multi_get_shape).
func projectGet() *cobra.Command {
	return &cobra.Command{
		Use:   "get <dotted.path>...",
		Short: "Print one or more fields by dotted path (multiple emit a keyed JSON object)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			p, _, err := loadProject()
			if err != nil {
				return err
			}
			return renderMultiGet(args, func(path string) (any, error) {
				v, ok := readDotted(p, path)
				if !ok {
					return nil, fmt.Errorf("unknown field: %s", path)
				}
				return v, nil
			})
		},
	}
}

// projectSet writes a single dotted-path field.
// String slices accept comma-separated input; bools accept true/false; ints parsed.
// `--unset <path>` takes no value and clears the field instead.
func projectSet() *cobra.Command {
	var unset bool
	c := &cobra.Command{
		Use:   "set <dotted.path> <value>",
		Short: "Write a single field by dotted path (--unset clears it)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			switch {
			case unset && len(args) != 1:
				return fmt.Errorf("--unset takes a path and no value")
			case !unset && len(args) != 2:
				return fmt.Errorf("accepts 2 arg(s), received %d", len(args))
			}
			// project.version is not an ordinary field: it has a second home in
			// state.json, and one writer owns both so the release-facing value
			// and the one dross bumps cannot diverge (c-4). --unset still takes
			// the generic path — clearing the field is doctor's problem, not a
			// version write.
			if !unset && args[0] == "project.version" {
				s, statePath, err := loadState()
				if err != nil {
					return err
				}
				if err := writeVersion(filepath.Dir(statePath), s, args[1]); err != nil {
					return err
				}
				return s.Save(statePath)
			}
			p, path, err := loadProject()
			if err != nil {
				return err
			}
			// Resolve before writing: a rejected path errors out here, leaving
			// project.toml byte-unchanged rather than truncate-then-fail.
			if unset {
				err = unsetDotted(p, args[0])
			} else {
				err = writeDotted(p, args[0], args[1])
			}
			if err != nil {
				return err
			}
			return p.Save(path)
		},
	}
	c.Flags().BoolVar(&unset, "unset", false, "clear the field at <dotted.path> instead of writing a value")
	return c
}

func loadProject() (*project.Project, string, error) {
	root, err := FindRoot()
	if err != nil {
		return nil, "", err
	}
	path := filepath.Join(root, project.File)
	p, err := project.Load(path)
	if err != nil {
		return nil, "", err
	}
	return p, path, nil
}

// readDotted covers every settable leaf in project.toml. Lists return as
// CSV strings; bools as "true"/"false". Nested map fields like
// runtime.services and stack.locked still require direct toml edits;
// /dross-options surfaces them but doesn't iterate keys.
func readDotted(p *project.Project, path string) (string, bool) {
	// board.state_map.<status> addresses one entry at a time (locked
	// state_map_write). The path is always known; an absent entry reads back
	// empty, exactly like an unset scalar.
	if key, ok := stateMapKey(path); ok {
		return p.Board.StateMap[key], true
	}
	// board.fields.<name> addresses one field-name override. Like state_map,
	// the path is always known; an unset override reads back empty, and a
	// project.toml with no [board.fields] table reads the struct's zero value
	// rather than panicking on an absent table.
	if key, ok := boardFieldKey(path); ok {
		switch key {
		case "state":
			return p.Board.Fields.State, true
		case "type":
			return p.Board.Fields.Type, true
		case "fix_versions":
			return p.Board.Fields.FixVersions, true
		}
		return "", false
	}
	switch path {
	// project
	case "project.name":
		return p.Project.Name, true
	case "project.description":
		return p.Project.Description, true
	case "project.version":
		return p.Project.Version, true
	// stack
	case "stack.languages":
		return strings.Join(p.Stack.Languages, ","), true
	case "stack.frameworks":
		return strings.Join(p.Stack.Frameworks, ","), true
	case "stack.package_manager":
		return p.Stack.PackageManager, true
	case "stack.type_checker":
		return p.Stack.TypeChecker, true
	case "stack.linter":
		return p.Stack.Linter, true
	case "stack.formatter":
		return p.Stack.Formatter, true
	case "stack.test_runner":
		return p.Stack.TestRunner, true
	case "stack.e2e_runner":
		return p.Stack.E2ERunner, true
	// runtime
	case "runtime.mode":
		return p.Runtime.Mode, true
	case "runtime.dev_command":
		return p.Runtime.DevCommand, true
	case "runtime.stop_command":
		return p.Runtime.StopCommand, true
	case "runtime.test_command":
		return p.Runtime.TestCommand, true
	case "runtime.test_watch":
		return p.Runtime.TestWatch, true
	case "runtime.e2e_command":
		return p.Runtime.E2ECommand, true
	case "runtime.typecheck_command":
		return p.Runtime.TypecheckCommand, true
	case "runtime.lint_command":
		return p.Runtime.LintCommand, true
	case "runtime.format_command":
		return p.Runtime.FormatCommand, true
	case "runtime.build_command":
		return p.Runtime.BuildCommand, true
	case "runtime.migrate_command":
		return p.Runtime.MigrateCommand, true
	case "runtime.seed_command":
		return p.Runtime.SeedCommand, true
	case "runtime.shell_command":
		return p.Runtime.ShellCommand, true
	case "runtime.logs_command":
		return p.Runtime.LogsCommand, true
	// repo
	case "repo.layout":
		return p.Repo.Layout, true
	case "repo.root_run_dir":
		return p.Repo.RootRunDir, true
	case "repo.workspaces":
		return strings.Join(p.Repo.Workspaces, ","), true
	case "repo.git_main_branch":
		return p.Repo.GitMainBranch, true
	case "repo.branch_pattern":
		return p.Repo.BranchPattern, true
	case "repo.commit_convention":
		return p.Repo.CommitConvention, true
	case "repo.squash_merge":
		return fmt.Sprintf("%t", p.Repo.SquashMerge), true
	// remote
	case "remote.url":
		return p.Remote.URL, true
	case "remote.provider":
		return p.Remote.Provider, true
	case "remote.public":
		return fmt.Sprintf("%t", p.Remote.Public), true
	case "remote.api_base":
		return p.Remote.APIBase, true
	case "remote.log_api":
		return fmt.Sprintf("%t", p.Remote.LogAPI), true
	case "remote.auth_env":
		return p.Remote.AuthEnv, true
	case "remote.auth_user":
		return p.Remote.AuthUser, true
	case "remote.auth_scheme":
		return p.Remote.AuthScheme, true
	case "remote.project_id":
		return p.Remote.ProjectID, true
	case "remote.api_version":
		return string(p.Remote.APIVersion), true
	case "remote.reviewers":
		return strings.Join(p.Remote.Reviewers, ","), true
	// board
	case "board.provider":
		return p.Board.Provider, true
	case "board.base_url":
		return p.Board.BaseURL, true
	case "board.auth_env":
		return p.Board.AuthEnv, true
	case "board.auth_user":
		return p.Board.AuthUser, true
	case "board.project":
		return p.Board.Project, true
	case "board.enabled":
		return fmt.Sprintf("%t", p.Board.Enabled), true
	case "board.milestone_mode":
		return p.Board.MilestoneMode, true
	case "board.github_project":
		return p.Board.GitHubProject, true
	// paths
	case "paths.source":
		return p.Paths.Source, true
	case "paths.tests":
		return p.Paths.Tests, true
	case "paths.e2e":
		return p.Paths.E2E, true
	case "paths.migrations":
		return p.Paths.Migrations, true
	case "paths.schemas":
		return p.Paths.Schemas, true
	case "paths.i18n":
		return p.Paths.I18n, true
	case "paths.public":
		return p.Paths.Public, true
	// env
	case "env.files":
		return strings.Join(p.Env.Files, ","), true
	case "env.secrets_location":
		return p.Env.SecretsLocation, true
	case "env.gitignored":
		return fmt.Sprintf("%t", p.Env.Gitignored), true
	// goals
	case "goals.core_value":
		return p.Goals.CoreValue, true
	case "goals.audience":
		return p.Goals.Audience, true
	case "goals.non_goals":
		return strings.Join(p.Goals.NonGoals, ","), true
	case "goals.differentiators":
		return strings.Join(p.Goals.Differentiators, ","), true
	// mutation — the three keys /dross-options §13 documents. They were
	// described in the prompt but never wired here, so the whole section was
	// unactionable through the CLI and could only be reached by hand-editing
	// project.toml, which the prompts' own don't-bypass-the-CLI rule forbids.
	case "mutation.adapters":
		return strings.Join(p.Mutation.Adapters, ","), true
	case "mutation.gremlins.timeout_coefficient":
		return strconv.Itoa(p.Mutation.Gremlins.TimeoutCoefficient), true
	case "mutation.stryker.workdir":
		return p.Mutation.Stryker.Workdir, true
	}
	return "", false
}

// enumKeys maps every enum-valued project.toml key to the set that defines it.
//
// One table, read by BOTH gates: writeDotted below refuses an out-of-set value
// at set time, and validate re-checks the file. Set-time rejection is what a
// human sees while they still remember what they meant; validate is what
// catches the file nobody typed, because project.toml is tracked, hand-edited
// and cloned. A value that never passed through the CLI was otherwise unchecked
// forever — which was most of them.
//
// Before this table all five accepted anything: `runtime.mode=banana`
// round-tripped through set and get, and `dross validate` reported it clean.
var enumKeys = map[string]configenum.Set{
	"runtime.mode":           configenum.RuntimeModes,
	"repo.layout":            configenum.RepoLayouts,
	"repo.commit_convention": configenum.CommitConventions,
	"remote.provider":        configenum.ShipProviders,
	"board.milestone_mode":   configenum.MilestoneModes,
}

// checkEnumValue reports an out-of-set value for an enum-valued key.
//
// An EMPTY value is always allowed: it means "unset this", and every optional
// key's absence is legitimate. Reddening on it would fail every partially
// filled project.toml, which is every project.toml between init and the first
// full options pass.
func checkEnumValue(path, value string) error {
	set, ok := enumKeys[path]
	if !ok || strings.TrimSpace(value) == "" {
		return nil
	}
	if !set.Has(value) {
		return fmt.Errorf("invalid %s %q; expected %s", path, value, set.List())
	}
	return nil
}

func writeDotted(p *project.Project, path, value string) error {
	// Checked before anything is written, so a refused value leaves
	// project.toml byte-unchanged — the same shape as the state_map arm below.
	if err := checkEnumValue(path, value); err != nil {
		return err
	}
	splitCSV := func(s string) []string {
		out := []string{}
		for _, x := range strings.Split(s, ",") {
			x = strings.TrimSpace(x)
			if x != "" {
				out = append(out, x)
			}
		}
		return out
	}
	setBool := func(target *bool) error {
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		*target = b
		return nil
	}
	// The first numeric key on this surface. It refuses rather than coercing,
	// for the same reason setBool does: a value that silently became 0 would
	// configure a real behaviour (gremlins' timeout multiplier) to something
	// the user never asked for, and they would find out from a mutation run.
	// An empty value clears it, matching how every optional key unsets.
	setInt := func(target *int) error {
		if strings.TrimSpace(value) == "" {
			*target = 0
			return nil
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid %s %q: expected a whole number", path, value)
		}
		*target = n
		return nil
	}
	// One state_map entry at a time — never a whole-map replace, so the other
	// entries survive and a project.toml with no [board.state_map] table gets
	// the map created rather than a nil-map panic.
	if key, ok := stateMapKey(path); ok {
		// A key outside the lifecycle set is silently-broken config: the
		// lookup is keyed by what dross emits, so an override on anything else
		// never applies. Rejected before the map is touched, so a refused write
		// leaves project.toml byte-unchanged. A near-miss of case or padding is
		// normalized by stateMapKey rather than rejected.
		if !configenum.LifecycleStatuses.Has(key) {
			return fmt.Errorf("unknown [board].state_map key %q; expected %s", key, configenum.LifecycleStatuses.List())
		}
		if p.Board.StateMap == nil {
			p.Board.StateMap = map[string]string{}
		}
		p.Board.StateMap[key] = value
		return nil
	}
	// One field-name override at a time, same shape as state_map. An
	// unrecognised key is rejected before anything is written — a typo'd
	// override that silently never applies is the same silent-breakage the
	// state_map arm above refuses.
	if key, ok := boardFieldKey(path); ok {
		switch key {
		case "state":
			p.Board.Fields.State = value
		case "type":
			p.Board.Fields.Type = value
		case "fix_versions":
			p.Board.Fields.FixVersions = value
		default:
			return fmt.Errorf("unknown [board].fields key %q; expected state, type or fix_versions", key)
		}
		return nil
	}
	switch path {
	// project
	case "project.name":
		p.Project.Name = value
	case "project.description":
		p.Project.Description = value
	case "project.version":
		p.Project.Version = value
	// stack
	case "stack.languages":
		p.Stack.Languages = splitCSV(value)
	case "stack.frameworks":
		p.Stack.Frameworks = splitCSV(value)
	case "stack.package_manager":
		p.Stack.PackageManager = value
	case "stack.type_checker":
		p.Stack.TypeChecker = value
	case "stack.linter":
		p.Stack.Linter = value
	case "stack.formatter":
		p.Stack.Formatter = value
	case "stack.test_runner":
		p.Stack.TestRunner = value
	case "stack.e2e_runner":
		p.Stack.E2ERunner = value
	// runtime
	case "runtime.mode":
		p.Runtime.Mode = value
	case "runtime.dev_command":
		p.Runtime.DevCommand = value
	case "runtime.stop_command":
		p.Runtime.StopCommand = value
	case "runtime.test_command":
		p.Runtime.TestCommand = value
	case "runtime.test_watch":
		p.Runtime.TestWatch = value
	case "runtime.e2e_command":
		p.Runtime.E2ECommand = value
	case "runtime.typecheck_command":
		p.Runtime.TypecheckCommand = value
	case "runtime.lint_command":
		p.Runtime.LintCommand = value
	case "runtime.format_command":
		p.Runtime.FormatCommand = value
	case "runtime.build_command":
		p.Runtime.BuildCommand = value
	case "runtime.migrate_command":
		p.Runtime.MigrateCommand = value
	case "runtime.seed_command":
		p.Runtime.SeedCommand = value
	case "runtime.shell_command":
		p.Runtime.ShellCommand = value
	case "runtime.logs_command":
		p.Runtime.LogsCommand = value
	// repo
	case "repo.layout":
		p.Repo.Layout = value
	case "repo.root_run_dir":
		p.Repo.RootRunDir = value
	case "repo.workspaces":
		p.Repo.Workspaces = splitCSV(value)
	case "repo.git_main_branch":
		p.Repo.GitMainBranch = value
	case "repo.branch_pattern":
		p.Repo.BranchPattern = value
	case "repo.commit_convention":
		p.Repo.CommitConvention = value
	case "repo.squash_merge":
		return setBool(&p.Repo.SquashMerge)
	// remote
	case "remote.url":
		p.Remote.URL = value
	case "remote.provider":
		p.Remote.Provider = value
	case "remote.public":
		return setBool(&p.Remote.Public)
	case "remote.api_base":
		p.Remote.APIBase = value
	case "remote.log_api":
		return setBool(&p.Remote.LogAPI)
	case "remote.auth_env":
		p.Remote.AuthEnv = value
	case "remote.auth_user":
		p.Remote.AuthUser = value
	case "remote.auth_scheme":
		p.Remote.AuthScheme = value
	case "remote.project_id":
		p.Remote.ProjectID = value
	case "remote.api_version":
		p.Remote.APIVersion = project.APIVersion(value)
	case "remote.reviewers":
		p.Remote.Reviewers = splitCSV(value)
	// board
	case "board.provider":
		p.Board.Provider = value
	case "board.base_url":
		p.Board.BaseURL = value
	case "board.auth_env":
		p.Board.AuthEnv = value
	case "board.auth_user":
		p.Board.AuthUser = value
	case "board.project":
		p.Board.Project = value
	case "board.enabled":
		return setBool(&p.Board.Enabled)
	case "board.milestone_mode":
		p.Board.MilestoneMode = value
	case "board.github_project":
		p.Board.GitHubProject = value
	// paths
	case "paths.source":
		p.Paths.Source = value
	case "paths.tests":
		p.Paths.Tests = value
	case "paths.e2e":
		p.Paths.E2E = value
	case "paths.migrations":
		p.Paths.Migrations = value
	case "paths.schemas":
		p.Paths.Schemas = value
	case "paths.i18n":
		p.Paths.I18n = value
	case "paths.public":
		p.Paths.Public = value
	// env
	case "env.files":
		p.Env.Files = splitCSV(value)
	case "env.secrets_location":
		p.Env.SecretsLocation = value
	case "env.gitignored":
		return setBool(&p.Env.Gitignored)
	// goals
	case "goals.core_value":
		p.Goals.CoreValue = value
	case "goals.audience":
		p.Goals.Audience = value
	case "goals.non_goals":
		p.Goals.NonGoals = splitCSV(value)
	case "goals.differentiators":
		p.Goals.Differentiators = splitCSV(value)
	// mutation — see the matching arm in readDotted for why these were absent.
	case "mutation.adapters":
		p.Mutation.Adapters = splitCSV(value)
	case "mutation.gremlins.timeout_coefficient":
		return setInt(&p.Mutation.Gremlins.TimeoutCoefficient)
	case "mutation.stryker.workdir":
		p.Mutation.Stryker.Workdir = value
	default:
		return fmt.Errorf("unknown or unsettable field: %s", path)
	}
	return nil
}

// stateMapKey recognises a `board.state_map.<status>` path and returns the
// entry key. The suffix must be non-empty and single-segment — bare
// `board.state_map` is not an addressable leaf.
// The key is normalized here rather than at each call site, so write, read and
// unset all address the same entry. Without it `set board.state_map.Planned`
// stores under "planned" (where the sync-time lookup can find it) while `get`
// and `--unset` keep asking for "Planned" — an entry the CLI wrote and cannot
// address, which is exactly the repair path doctor's new check depends on.
func stateMapKey(path string) (string, bool) {
	key, ok := strings.CutPrefix(path, "board.state_map.")
	if !ok || key == "" || strings.Contains(key, ".") {
		return "", false
	}
	return configenum.Normalize(key), true
}

// boardFieldKey recognises a `board.fields.<name>` path and returns the field
// key, normalized the same way state_map keys are so write, read and unset all
// address the same override. Bare `board.fields` is not an addressable leaf.
//
// Recognising the prefix (rather than only the three valid suffixes) is
// deliberate: it lets writeDotted reject `board.fields.bogus` by name and list
// what is accepted, instead of falling through to the generic
// "unknown or unsettable field".
func boardFieldKey(path string) (string, bool) {
	key, ok := strings.CutPrefix(path, "board.fields.")
	if !ok || key == "" || strings.Contains(key, ".") {
		return "", false
	}
	return configenum.Normalize(key), true
}

// unsetDotted clears a field written by mistake. A scalar is zeroed through
// writeDotted's own arms — so an unknown path fails with the same message
// `set` gives, before anything is written — while a state_map entry is deleted
// outright, and the last one takes the whole table with it.
func unsetDotted(p *project.Project, path string) error {
	if key, ok := stateMapKey(path); ok {
		delete(p.Board.StateMap, key)
		if len(p.Board.StateMap) == 0 {
			p.Board.StateMap = nil
		}
		return nil
	}
	return writeDotted(p, path, "")
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "yes", "y", "1":
		return true, nil
	case "false", "no", "n", "0", "":
		return false, nil
	}
	return false, fmt.Errorf("invalid bool: %q (use true/false)", v)
}
