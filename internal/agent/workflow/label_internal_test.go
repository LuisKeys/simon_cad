package workflow

import "testing"

func TestCorrectingLabelSurvivesEmptyDescriptionsAndBadSteps(t *testing.T) {
	p := &Plan{Steps: []Step{{Description: ""}, {Description: "Cut the slot."}}}
	for step, want := range map[int]string{1: "Correcting step 1…", 2: "Correcting step 2 (cut the slot)…", 9: "Correcting the model…"} {
		if got := correctingLabel(p, &Report{Issues: []Issue{{Step: step}}}); got != want {
			t.Errorf("step %d: got %q, want %q", step, got, want)
		}
	}
}
