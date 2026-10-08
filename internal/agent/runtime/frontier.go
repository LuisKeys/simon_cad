package runtime

import "regexp"

// frontierRequest matches a prompt that asks for the fallback (frontier) model, in Spanish
// or English: "usa el modelo de frontera", "use the fallback model", "con GPT/Claude"...
var frontierRequest = regexp.MustCompile(`(?i)` +
	`(frontier\s+model|modelo\s+(de\s+)?frontera|modelo\s+frontera|` +
	`(fallback|respaldo|remoto|cloud|nube)\s+model|modelo\s+(de\s+)?(fallback|respaldo|remoto|la\s+nube|en\s+la\s+nube)|` +
	`(use|using|with|ask|usa|usá|usar|utiliza|usando|con|pregunta(le)?\s+a|consulta(r)?)\s+(the\s+|el\s+|la\s+)?(fallback|frontier|cloud|remote|frontera|nube|respaldo)\b)`)

// WantsFrontier reports whether the user's prompt asks for the fallback (frontier) model.
func WantsFrontier(prompt string) bool { return frontierRequest.MatchString(prompt) }
