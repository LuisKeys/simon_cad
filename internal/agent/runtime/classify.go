package runtime

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// Tier is a rung of the model ladder a request can run on. The ladder only goes up: a
// request starts at the tier the classifier picks and may escalate, never come back down.
type Tier int

const (
	TierLocal    Tier = iota // the local model: routine CAD operations
	TierStandard             // Cloud Standard: ambiguous or moderately complex requests
	TierAdvanced             // Cloud Advanced: complex design and difficult failures
)

// Tier names, as they appear in telemetry and settings.
const (
	nameLocal    = "local"
	nameStandard = "cloud_standard"
	nameAdvanced = "cloud_advanced"
)

func (t Tier) String() string {
	switch t {
	case TierStandard:
		return nameStandard
	case TierAdvanced:
		return nameAdvanced
	}
	return nameLocal
}

// ParseTier is the inverse of Tier.String; ok is false for an unknown name.
func ParseTier(s string) (Tier, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case nameLocal, "simple":
		return TierLocal, true
	case nameStandard, "standard":
		return TierStandard, true
	case nameAdvanced, "advanced", "complex":
		return TierAdvanced, true
	}
	return TierLocal, false
}

// Signals are what the caller knows about a request besides its text.
type Signals struct {
	Images, Documents bool
	// UserAsked: the user asked for the frontier model in the prompt (see WantsFrontier).
	UserAsked bool
	// Complex: the request needs a multi-step plan (workflow.NeedsPlan, which this package
	// cannot import).
	Complex bool
}

// routine matches the operations a local model handles well: primitives, transforms,
// parameter changes and appearance, in Spanish and English.
var routine = regexp.MustCompile(`(?i)\b(` +
	`crea\w*|create|add|make|agrega\w*|añade\w*|pon\w*|dibuja\w*|draw|` +
	`mueve\w*|move|translate|rota\w*|rotate|escala\w*|scale|resize|centr\w+|center|align|alinea\w*|` +
	`cambia\w*|change|set|ajusta\w*|adjust|modifica\w*|modify|edit\w*|actualiza\w*|update|` +
	`color\w*|colour|material\w*|pinta\w*|paint|transparen\w*|opacity|opacidad|` +
	`borra\w*|elimina\w*|delete|remove|oculta\w*|hide|muestra\w*|show|` +
	`cubo|cube|box|caja|cilindro|cylinder|esfera|sphere|cono|cone|` +
	`mm|cm|radio|radius|diameter|diámetro|diametro|alto|ancho|largo|height|width|length|depth` +
	`)\b`)

// multiStep marks a request that is not one simple operation.
var multiStep = regexp.MustCompile(`(?im)(^\s*(\d+[.)]|[-*•])\s+\S|\b(and then|then|después|luego|finalmente|por último|paso a paso|step by step|parametric|paramétric\w*|constraint\w*|restricci\w+|tangent\w*|loft|sweep|barrido|ensambl\w+|assembl\w+)\b)`)

const maxRoutineWords = 30

// classifySystem is the system prompt of the local classification call.
const classifySystem = `You route requests of a CAD assistant. Call classify_request with the tier:
- "local": one simple operation (create a primitive, move, rotate, scale, change a dimension, color or material, edit one feature).
- "standard": ambiguous wording that needs interpretation, or several related operations.
- "advanced": complex multi-step parametric modeling, difficult geometric relationships or constraints.
Answer only by calling the tool.`

// classifyTool is the structured answer of the classification call.
var classifyTool = ToolDef{
	Name:        "classify_request",
	Description: "Report the tier that should handle the user's CAD request.",
	InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tier":   map[string]any{"type": "string", "enum": []string{nameLocal, nameStandard, nameAdvanced}},
			"reason": map[string]any{"type": "string"},
		},
		"required": []string{"tier"},
	},
}

// classifyTimeout bounds the local model call: routing must stay cheap.
const classifyTimeout = 20 * time.Second

// Classify picks the tier a request starts on, and says why. The rules are deterministic and
// free; only a request they cannot place is put to the local model (one short forced tool
// call). Classification never reaches the cloud: when local is nil or the call fails, the
// request starts local and escalates only if it fails.
func Classify(ctx context.Context, local Provider, text string, s Signals) (Tier, string) {
	switch {
	case s.UserAsked:
		return TierAdvanced, "requested in the prompt"
	case s.Images:
		return TierAdvanced, "an image is attached"
	case s.Documents:
		return TierAdvanced, "a document is attached"
	case s.Complex:
		return TierStandard, "the request needs a multi-step plan"
	}
	words := len(strings.Fields(text))
	if words == 0 {
		return TierLocal, "empty prompt"
	}
	if words <= maxRoutineWords && routine.MatchString(text) && !multiStep.MatchString(text) {
		return TierLocal, "routine operation"
	}
	if local == nil {
		return TierLocal, "no local classifier"
	}
	cctx, cancel := context.WithTimeout(ctx, classifyTimeout)
	defer cancel()
	resp, err := local.Complete(cctx, Request{System: classifySystem, Tools: []ToolDef{classifyTool}, ForceTool: classifyTool.Name,
		NoThinking: true, MaxTokens: 200,
		Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: text}}}}})
	if err != nil {
		return TierLocal, "classifier unavailable"
	}
	for _, u := range resp.ToolUses() {
		if u.Name != classifyTool.Name {
			continue
		}
		var out struct{ Tier string }
		if json.Unmarshal(u.Input, &out) == nil {
			if t, ok := ParseTier(out.Tier); ok {
				// the model's own reason may quote the prompt: it is not kept (see RouteTrace)
				return t, "classified by the local model"
			}
		}
	}
	return TierLocal, "unclear classification"
}
