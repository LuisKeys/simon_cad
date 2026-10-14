package vision_test

import (
	"testing"
	"time"

	"github.com/accelone/simoncad/internal/agent/capabilities/vision"
	"github.com/accelone/simoncad/internal/app"
)

func TestRelevantOnlyWhileAnImageIsInPlay(t *testing.T) {
	a := app.New("demo", func() time.Time { return time.Unix(0, 0) })
	defer a.Close()
	c := vision.New()
	if c.Relevant(a, nil) {
		t.Fatal("a project with no image must not carry the vision tools")
	}
	if !c.Relevant(a, []string{"img_0001"}) {
		t.Fatal("an image attached to the turn must load the vision tools")
	}
}
