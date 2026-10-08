// Package refine is visual closed-loop refinement: the model is repeatedly observed
// (rendered), measured (silhouettes, proportions, landmarks, a vision critic), modified
// (at most a few structural corrections) and observed again, coarse to fine, with
// automatic rollback of corrections that make it look worse.
//
// Roles are separate components:
//
//	Planner              — modeling strategy (parameters, regions, stage contents)
//	CADBuilder           — executes geometry operations through the ordinary agent tools
//	Renderer             — offscreen renders (internal/visual via App.RenderCamera)
//	VisionCritic         — compares references with renders; structured JSON, never edits
//	RefinementController — chooses the next corrections, enforces stages and rollback
//
// Reference images stay active constraints for the whole process: every evaluation
// compares the current renders with them again.
package refine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/accelone/simoncad/internal/agent/runtime"
)

// imageBlock wraps PNG/JPEG bytes as a model image block.
func imageBlock(data []byte, mediaType string) runtime.Block {
	return runtime.Block{Type: "image", Source: &runtime.ImageSource{Type: "base64", MediaType: mediaType, Data: base64.StdEncoding.EncodeToString(data)}}
}

// structured asks the model for one call of a single "submit" tool and returns its input.
// A model that answers in text gets one reminder.
func structured(ctx context.Context, p runtime.Provider, system string, content []runtime.Block, tool runtime.ToolDef, usage *runtime.Usage) (json.RawMessage, error) {
	msgs := []runtime.Message{{Role: "user", Content: content}}
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := p.Complete(ctx, runtime.Request{System: system, Messages: msgs, Tools: []runtime.ToolDef{tool}, NoThinking: true, MaxTokens: 4096})
		if err != nil {
			return nil, err
		}
		if usage != nil {
			usage.Add(resp.Usage)
		}
		for _, u := range resp.ToolUses() {
			if u.Name == tool.Name {
				return u.Input, nil
			}
		}
		msgs = append(msgs, runtime.Message{Role: "assistant", Content: resp.Content},
			runtime.Message{Role: "user", Content: []runtime.Block{{Type: "text", Text: "Answer only by calling " + tool.Name + "."}}})
	}
	return nil, fmt.Errorf("%s returned no structured result", p.Name())
}

// JSON-schema helpers.
func str() map[string]any                     { return map[string]any{"type": "string"} }
func num() map[string]any                     { return map[string]any{"type": "number"} }
func arr(items map[string]any) map[string]any { return map[string]any{"type": "array", "items": items} }
func obj(props map[string]any, req ...string) map[string]any {
	o := map[string]any{"type": "object", "properties": props}
	if len(req) > 0 {
		o["required"] = req
	}
	return o
}

// pointMap is {name: [x, y]} in normalized image coordinates.
func pointMap() map[string]any {
	return map[string]any{"type": "object", "description": "name → [x, y] normalized image coordinates (0..1, y down)",
		"additionalProperties": map[string]any{"type": "array", "items": num(), "minItems": 2, "maxItems": 2}}
}
