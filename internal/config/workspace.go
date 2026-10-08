package config

import (
	"encoding/json"

	"github.com/accelone/simoncad/internal/workspace"
)

// Global workspace preferences: the units, grid and snapping a new project starts with.
// They are a user preference, not project state: an open or existing project keeps its own
// settings (workspace.json) and never follows a later change here.

const keyWorkspace = "SIMONCAD_WORKSPACE_DEFAULTS"

// WorkspaceDefaults returns the global workspace defaults: the saved ones, else the factory
// defaults. Saved settings are read like a project's (missing or invalid sections fall back
// to the factory values), so an old or hand-edited settings file never blocks startup.
func WorkspaceDefaults() workspace.Settings {
	raw := Get(keyWorkspace)
	if raw == "" {
		return workspace.Defaults()
	}
	s, _, err := workspace.Decode([]byte(raw), workspace.Defaults())
	if err != nil {
		return workspace.Defaults()
	}
	return s
}

// workspaceValues validates the global defaults and returns them as configuration values.
func workspaceValues(s workspace.Settings) (map[string]string, error) {
	s = s.Normalized()
	if err := s.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return map[string]string{keyWorkspace: string(b)}, nil
}
