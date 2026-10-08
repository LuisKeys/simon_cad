package config

import (
	"fmt"
	"strconv"
)

// Rendering preferences of the 3D view: lighting, exposure, tone mapping and shadows. They are
// presentation only and never reach the project, its history or an export.

// Reflection environments, tone mappers and shadow qualities.
const (
	EnvStudio = "studio" // procedural softboxes
	EnvRoom   = "room"   // the plain three.js room

	ToneNeutral = "neutral" // keeps colors close to the material's own
	ToneACES    = "aces"    // filmic: more contrast, warmer highlights
	ToneAgX     = "agx"     // filmic: softer highlights
	ToneNone    = "none"

	ShadowsOff      = "off"
	ShadowsStandard = "standard"
	ShadowsHigh     = "high"
)

// RenderSettings are the Settings → Rendering preferences. Percentages keep the stored values
// plain integers.
type RenderSettings struct {
	Environment  string `json:"environment"`
	EnvIntensity int    `json:"envIntensity"` // percent, 0–300
	Exposure     int    `json:"exposure"`     // percent, 25–300
	ToneMapping  string `json:"toneMapping"`
	Shadows      string `json:"shadows"`
}

const (
	keyEnvironment = "SIMONCAD_RENDER_ENVIRONMENT"
	keyEnvIntens   = "SIMONCAD_RENDER_ENV_INTENSITY"
	keyExposure    = "SIMONCAD_RENDER_EXPOSURE"
	keyToneMapping = "SIMONCAD_RENDER_TONE_MAPPING"
	keyShadows     = "SIMONCAD_RENDER_SHADOWS"
)

// DefaultRender is what the viewport used before these were configurable.
var DefaultRender = RenderSettings{Environment: EnvStudio, EnvIntensity: 100, Exposure: 100, ToneMapping: ToneNeutral, Shadows: ShadowsStandard}

func inRange(s string, lo, hi, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n >= lo && n <= hi {
		return n
	}
	return def
}

// Render returns the effective rendering preferences, defaults filled in.
func Render() RenderSettings {
	r := DefaultRender
	r.EnvIntensity = inRange(Get(keyEnvIntens), 0, 300, r.EnvIntensity)
	r.Exposure = inRange(Get(keyExposure), 25, 300, r.Exposure)
	switch s := Get(keyEnvironment); s {
	case EnvStudio, EnvRoom:
		r.Environment = s
	}
	switch s := Get(keyToneMapping); s {
	case ToneNeutral, ToneACES, ToneAgX, ToneNone:
		r.ToneMapping = s
	}
	switch s := Get(keyShadows); s {
	case ShadowsOff, ShadowsStandard, ShadowsHigh:
		r.Shadows = s
	}
	return r
}

// renderValues validates r and returns it as configuration values.
func renderValues(r RenderSettings) (map[string]string, error) {
	switch {
	case r.EnvIntensity < 0 || r.EnvIntensity > 300:
		return nil, fmt.Errorf("environment intensity %d is out of range (0–300)", r.EnvIntensity)
	case r.Exposure < 25 || r.Exposure > 300:
		return nil, fmt.Errorf("exposure %d is out of range (25–300)", r.Exposure)
	case r.Environment != EnvStudio && r.Environment != EnvRoom:
		return nil, fmt.Errorf("unknown environment %q", r.Environment)
	case r.ToneMapping != ToneNeutral && r.ToneMapping != ToneACES && r.ToneMapping != ToneAgX && r.ToneMapping != ToneNone:
		return nil, fmt.Errorf("unknown tone mapping %q", r.ToneMapping)
	case r.Shadows != ShadowsOff && r.Shadows != ShadowsStandard && r.Shadows != ShadowsHigh:
		return nil, fmt.Errorf("unknown shadow quality %q", r.Shadows)
	}
	return map[string]string{
		keyEnvironment: r.Environment, keyEnvIntens: strconv.Itoa(r.EnvIntensity),
		keyExposure: strconv.Itoa(r.Exposure), keyToneMapping: r.ToneMapping, keyShadows: r.Shadows,
	}, nil
}
