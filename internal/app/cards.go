package app

import "github.com/accelone/simoncad/internal/commands"

// ChangeSetCard is a ChangeSet as the conversation and review UI show it: plain data, so
// front ends never handle commands directly.
type ChangeSetCard struct {
	ID           string       `json:"id"`
	State        string       `json:"state"`
	Intent       string       `json:"intent"`
	Explanation  string       `json:"explanation"`
	Commands     []string     `json:"commands"`
	Affected     []string     `json:"affected"`
	Warnings     []string     `json:"warnings"`
	Validation   []Validation `json:"validation"`
	Valid        bool         `json:"valid"`
	VolumeBefore float64      `json:"volume_before_mm3"`
	VolumeAfter  float64      `json:"volume_after_mm3"`
	Added        []string     `json:"added"`
	Removed      []string     `json:"removed"`
	Modified     []string     `json:"modified"`
	CreatedBy    string       `json:"created_by"`
}

type Validation struct {
	Check    string `json:"check"`
	OK       bool   `json:"ok"`
	Severity string `json:"severity"`
	Message  string `json:"message,omitempty"`
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// CardOf renders a ChangeSet for display.
func CardOf(cs *commands.ChangeSet) ChangeSetCard {
	c := ChangeSetCard{ID: cs.ID, State: string(cs.State), Intent: cs.Intent, Explanation: cs.Explanation,
		Commands: []string{}, Affected: nz(cs.Affected), Warnings: nz(cs.Warnings), Validation: []Validation{},
		Valid: cs.Valid(), VolumeBefore: cs.Effects.VolumeBefore, VolumeAfter: cs.Effects.VolumeAfter,
		Added: nz(cs.Effects.Added), Removed: nz(cs.Effects.Removed), Modified: nz(cs.Effects.Modified),
		CreatedBy: cs.Provenance.CreatedBy}
	for _, r := range cs.Commands {
		c.Commands = append(c.Commands, r.Type)
	}
	for _, v := range cs.Validation {
		c.Validation = append(c.Validation, Validation{Check: v.Check, OK: v.OK, Severity: v.Severity, Message: v.Message})
	}
	return c
}
