package features

import (
	"errors"
	"fmt"
	"strings"
)

// Machine-readable error codes. A coded error prints as "CODE: message (hint)", so the code
// survives every layer that carries errors as plain strings (rebuild status, ChangeSet
// validation, tool results); CodeOf recovers it.
const (
	CodePathInvalid        = "PATH_INVALID"
	CodePathSelfIntersect  = "PATH_SELF_INTERSECTION"
	CodeProfileInvalid     = "PROFILE_INVALID"
	CodeSweepSelfIntersect = "SWEEP_SELF_INTERSECTION"
	CodeExcessiveCurvature = "EXCESSIVE_CURVATURE"
	CodeSweepFailed        = "SWEEP_FAILED"
	CodeNonManifold        = "NON_MANIFOLD_MESH"
	CodeBooleanFailed      = "BOOLEAN_OPERATION_FAILED"
	CodeProjectionFailed   = "SURFACE_PROJECTION_FAILED"
	CodeVectorizeEmpty     = "VECTORIZE_EMPTY"
	CodeFontNotFound       = "FONT_NOT_FOUND"
	CodeNotPrintable       = "NOT_PRINTABLE"
	CodeScaleRequired      = "SCALE_REQUIRED"
	CodeReliefDisconnected = "RELIEF_DISCONNECTED"
	CodeReliefNoEffect     = "RELIEF_NO_EFFECT"
	CodeRecipeInvalid      = "RECIPE_INVALID"
)

// CodedError is an error with a stable code and an actionable hint.
type CodedError struct {
	Code string
	Msg  string
	Hint string
}

func (e *CodedError) Error() string {
	if e.Hint == "" {
		return e.Code + ": " + e.Msg
	}
	return fmt.Sprintf("%s: %s (%s)", e.Code, e.Msg, e.Hint)
}

// Coded builds a CodedError.
func Coded(code, hint, format string, a ...any) error {
	return &CodedError{Code: code, Msg: fmt.Sprintf(format, a...), Hint: hint}
}

// Codes lists every known error code.
var Codes = []string{CodePathInvalid, CodePathSelfIntersect, CodeProfileInvalid, CodeSweepSelfIntersect,
	CodeExcessiveCurvature, CodeSweepFailed, CodeNonManifold, CodeBooleanFailed, CodeProjectionFailed,
	CodeVectorizeEmpty, CodeFontNotFound, CodeNotPrintable, CodeScaleRequired, CodeReliefDisconnected, CodeReliefNoEffect, CodeRecipeInvalid}

// CodeOf returns the first known error code that appears in msg ("" when there is none).
// Messages travel wrapped ("sweep failed: PROFILE_INVALID: ..."), so it searches the text.
func CodeOf(msg string) string {
	best, at := "", -1
	for _, c := range Codes {
		if i := strings.Index(msg, c+":"); i >= 0 && (at < 0 || i < at) {
			best, at = c, i
		}
	}
	return best
}

// ErrCode returns the code of err (or of the first coded error it wraps).
func ErrCode(err error) string {
	var ce *CodedError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return CodeOf(err.Error())
}
