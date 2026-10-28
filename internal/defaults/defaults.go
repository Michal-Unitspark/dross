// Package defaults handles ~/.claude/dross/defaults.toml — values that
// pre-fill init/onboard prompts so the user doesn't re-answer the same
// questions on every project. Project values still go to the project's
// own project.toml; defaults only affect what the prompt suggests.
package defaults

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/project"
)

const File = "defaults.toml"

// Defaults is the schema of the global defaults file. Holds both
// cross-project pre-fills (remote_defaults) and global runtime
// toggles (telemetry).
type Defaults struct {
	Remote    RemoteDefaults    `toml:"remote_defaults,omitempty" json:"remote_defaults,omitempty"`
	Telemetry TelemetryDefaults `toml:"telemetry,omitempty" json:"telemetry,omitempty"`
}

// TelemetryDefaults controls the local-only event recorder. Default ON;
// users are asked to confirm at init/onboard time so consent is
// explicit. Enabled is a pointer so we can distinguish "user said yes"
// (true), "user said no" (false), and "never asked" (nil — treated as
// enabled per default-ON policy, but init/onboard should still ask).
type TelemetryDefaults struct {
	Enabled *bool  `toml:"enabled,omitempty" json:"enabled,omitempty"`
	AskedAt string `toml:"asked_at,omitempty" json:"asked_at,omitempty"` // ISO date the user was prompted
}

// TelemetryEnabled returns the effective on/off bit. Unset = on.
func (t TelemetryDefaults) TelemetryEnabled() bool {
	if t.Enabled == nil {
		return true
	}
	return *t.Enabled
}

// RemoteDefaults seeds project.Remote at init/onboard time. Each field is
// the value to pre-fill the prompt with — the user always confirms.
type RemoteDefaults struct {
	Provider  string   `toml:"provider,omitempty" json:"provider,omitempty"`
	APIBase   string   `toml:"api_base,omitempty" json:"api_base,omitempty"`
	LogAPI    bool     `toml:"log_api,omitempty" json:"log_api,omitempty"`
	AuthEnv   string   `toml:"auth_env,omitempty" json:"auth_env,omitempty"`
	Reviewers []string `toml:"reviewers,omitempty" json:"reviewers,omitempty"`
}

// Apply seeds the non-empty fields of d.Remote into r, overwriting r's
// zero values only. Fields already set on r are preserved (e.g. URL +
// Provider from DetectRemote).
//
// auth_env and reviewers name one forge's credential and people. When r
// already names a provider and the defaults were saved for a different one,
// they belong to that other forge — a saved GITHUB_TOKEN seeded onto an Azure
// DevOps repo would be sent to Azure DevOps — so they are not seeded. With no
// detected provider (an unknown host) the defaults' provider seeds too, and
// its credential with it.
func (d Defaults) Apply(r project.Remote) project.Remote {
	foreign := r.Provider != "" && d.Remote.Provider != "" &&
		configenum.Normalize(r.Provider) != configenum.Normalize(d.Remote.Provider)
	if r.Provider == "" {
		r.Provider = d.Remote.Provider
	}
	if r.APIBase == "" {
		r.APIBase = d.Remote.APIBase
	}
	if !r.LogAPI && d.Remote.LogAPI {
		r.LogAPI = true
	}
	if r.AuthEnv == "" && !foreign {
		r.AuthEnv = d.Remote.AuthEnv
	}
	if len(r.Reviewers) == 0 && len(d.Remote.Reviewers) > 0 && !foreign {
		r.Reviewers = append([]string(nil), d.Remote.Reviewers...)
	}
	return r
}

// LoadFile reads a defaults.toml. Missing file = empty Defaults, no error.
func LoadFile(path string) (*Defaults, error) {
	var d Defaults
	_, err := toml.DecodeFile(path, &d)
	if errors.Is(err, fs.ErrNotExist) {
		return &Defaults{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return &d, nil
}

// SaveFile writes a defaults.toml, creating the parent dir.
func (d *Defaults) SaveFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	enc := toml.NewEncoder(f)
	enc.Indent = "  "
	if err := enc.Encode(d); err != nil {
		return fmt.Errorf("encode defaults: %w", err)
	}
	return nil
}

// FromRemote extracts default-worthy fields from a project Remote.
// URL, Public are project-specific; everything else is reusable.
func FromRemote(r project.Remote) RemoteDefaults {
	return RemoteDefaults{
		Provider:  r.Provider,
		APIBase:   r.APIBase,
		LogAPI:    r.LogAPI,
		AuthEnv:   r.AuthEnv,
		Reviewers: append([]string(nil), r.Reviewers...),
	}
}
