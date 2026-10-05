package tools

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// ValidateSchema checks v (decoded JSON) against the JSON-Schema subset used by tool
// definitions: type, properties, required, enum, minimum/maximum, exclusiveMinimum,
// items, minItems/maxItems, additionalProperties:false, pattern-free string lengths.
func ValidateSchema(schema map[string]any, v any) error {
	return validate(schema, v, "args")
}

func validate(s map[string]any, v any, path string) error {
	if enum, ok := s["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if e == v {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%s must be one of %v", path, enum)
		}
	}
	t, _ := s["type"].(string)
	switch t {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]string); ok {
			for _, r := range req {
				if _, has := m[r]; !has {
					return fmt.Errorf("%s.%s is required", path, r)
				}
			}
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ps, known := props[k].(map[string]any)
			if !known {
				if ap, ok := s["additionalProperties"].(bool); ok && !ap {
					return fmt.Errorf("%s.%s is not a recognized argument", path, k)
				}
				continue
			}
			if err := validate(ps, m[k], path+"."+k); err != nil {
				return err
			}
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", path)
		}
		if n, ok := s["maxLength"].(int); ok && len(str) > n {
			return fmt.Errorf("%s is longer than %d characters", path, n)
		}
		if n, ok := s["minLength"].(int); ok && len(str) < n {
			return fmt.Errorf("%s must not be empty", path)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
	case "number", "integer":
		f, ok := v.(float64)
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("%s must be a finite number", path)
		}
		if t == "integer" && f != math.Trunc(f) {
			return fmt.Errorf("%s must be an integer", path)
		}
		if mn, ok := s["minimum"].(float64); ok && f < mn {
			return fmt.Errorf("%s must be >= %g (got %g)", path, mn, f)
		}
		if mx, ok := s["maximum"].(float64); ok && f > mx {
			return fmt.Errorf("%s must be <= %g (got %g)", path, mx, f)
		}
		if ex, ok := s["exclusiveMinimum"].(float64); ok && f <= ex {
			return fmt.Errorf("%s must be > %g (got %g)", path, ex, f)
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		if n, ok := s["minItems"].(int); ok && len(a) < n {
			return fmt.Errorf("%s needs at least %d items", path, n)
		}
		if n, ok := s["maxItems"].(int); ok && len(a) > n {
			return fmt.Errorf("%s allows at most %d items", path, n)
		}
		if is, ok := s["items"].(map[string]any); ok {
			for i, e := range a {
				if err := validate(is, e, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	case "":
	default:
		return fmt.Errorf("%s: unsupported schema type %q", path, t)
	}
	return nil
}

// ---- schema builders ------------------------------------------------------------

type props map[string]any

func object(p props, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": map[string]any(p), "additionalProperties": false}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func number(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }

func positive(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc, "exclusiveMinimum": 0.0, "maximum": 100000.0}
}

func integer(desc string, min, max float64) map[string]any {
	return map[string]any{"type": "integer", "description": desc, "minimum": min, "maximum": max}
}

func text(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "maxLength": 2000}
}

func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func enum(desc string, vals ...string) map[string]any {
	e := make([]any, len(vals))
	for i, v := range vals {
		e[i] = v
	}
	return map[string]any{"type": "string", "description": desc + " (" + strings.Join(vals, " | ") + ")", "enum": e}
}

func array(desc string, items map[string]any, min, max int) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": items, "minItems": min, "maxItems": max}
}
