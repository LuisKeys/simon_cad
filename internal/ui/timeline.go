package ui

import (
	"sort"

	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/history"
	"github.com/accelone/simoncad/internal/rendering"
)

// ---- timeline -------------------------------------------------------------------------

type CheckpointView struct {
	ID         string   `json:"id"`
	Seq        int      `json:"seq"`
	Label      string   `json:"label"`
	Actor      string   `json:"actor"`
	Time       string   `json:"time"`
	ChangeSets []string `json:"changesets"`
	Current    bool     `json:"current"`
}

func (s *Service) checkpoints() []CheckpointView {
	hash := s.app.Snapshot().Hash()
	out := []CheckpointView{}
	for _, c := range s.app.Hist.Checkpoints {
		out = append(out, CheckpointView{ID: c.ID, Seq: c.Seq, Label: c.Label, Actor: c.Actor, Time: c.Time,
			ChangeSets: c.ChangeSets, Current: c.GraphHash == hash})
	}
	return out
}

// Restore proposes a return to a checkpoint. It is never auto-committed: the user previews
// and commits it like any other ChangeSet.
func (s *Service) Restore(ref string) (Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.guard(); err != nil {
		return Outcome{}, err
	}
	s.app.SetActor(app.Actor{CreatedBy: "user"})
	return s.finishTool("restore_checkpoint", s.app.Restore(ref, app.PolicyPreview)), nil
}

// Comparison is a semantic diff between two checkpoints (or one and the live model).
type Comparison struct {
	A         string       `json:"a"`
	B         string       `json:"b"`
	Diff      history.Diff `json:"diff"`
	VolumeA   float64      `json:"volume_a_mm3"`
	VolumeB   float64      `json:"volume_b_mm3"`
	FeaturesA int          `json:"features_a"`
	FeaturesB int          `json:"features_b"`
	Identical bool         `json:"identical"`
	LabelA    string       `json:"label_a"`
	LabelB    string       `json:"label_b"`
}

// Compare diffs two checkpoints; an empty b means the live model.
func (s *Service) Compare(a, b string) (*Comparison, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ca, err := s.app.Hist.Find(a)
	if err != nil {
		return nil, err
	}
	gb, lb := s.app.Snapshot(), "current model"
	if b != "" {
		cb, err := s.app.Hist.Find(b)
		if err != nil {
			return nil, err
		}
		gb, lb = cb.Snapshot, cb.Label
	}
	d := history.Compare(ca.Snapshot, gb)
	ea, err := s.app.Evaluate(ca.Snapshot)
	if err != nil {
		return nil, err
	}
	eb, err := s.app.Evaluate(gb)
	if err != nil {
		return nil, err
	}
	return &Comparison{A: ca.ID, B: b, LabelA: ca.Label, LabelB: lb, Diff: d, VolumeA: ea.Volume, VolumeB: eb.Volume,
		FeaturesA: len(ca.Snapshot.Nodes), FeaturesB: len(gb.Nodes), Identical: d.Empty()}, nil
}

// ---- playback -------------------------------------------------------------------------

// Frame is one step of Instant Design playback.
type Frame struct {
	Index     int              `json:"index"` // number of ChangeSets applied (0 = empty)
	Total     int              `json:"total"`
	ChangeSet *ChangeSetCard   `json:"changeset,omitempty"`
	Chat      *app.StepChat    `json:"chat,omitempty"` // the conversation behind this step, replayed in the chat panel
	Scene     *rendering.Delta `json:"scene"`
}

// PlaybackFrame rebuilds the model after the first i committed ChangeSets, so geometry
// appears feature by feature, driven by the recorded ChangeSets.
func (s *Service) PlaybackFrame(i int, known map[string]string) (*Frame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sets := s.app.Committed()
	m, err := s.app.ModelAfter(i)
	if err != nil {
		return nil, err
	}
	d := &rendering.Delta{Order: []string{}, Keys: map[string]string{}, Changed: []*rendering.BodyMesh{}, Removed: []string{}, Sketches: []*rendering.SketchLines{}, Paths: []*rendering.PathLines{}}
	if len(m.Leaves) > 0 || len(m.Sketches) > 0 {
		if d, err = s.rend.Scene(m, known); err != nil {
			return nil, err
		}
	} else {
		for id := range known {
			d.Removed = append(d.Removed, id)
		}
		sort.Strings(d.Removed)
	}
	f := &Frame{Index: i, Total: len(sets), Scene: d}
	if i > 0 {
		c := app.CardOf(sets[i-1])
		f.ChangeSet = &c
		if sc := s.app.PlaybackScript(); i-1 < len(sc) {
			f.Chat = &sc[i-1]
		}
	}
	return f, nil
}
