package app

import (
	"strings"
	"testing"
)

func TestPlaybackScriptReplaysTheConversation(t *testing.T) {
	a := New("demo", clock)
	t.Cleanup(a.Close)
	a.SetActor(Actor{CreatedBy: "agent", Capability: "geometry", Model: "recorded"})
	a.RecordPrompt("A plate with two holes")
	if o := a.ApplyTool("add_hole", []byte(`{"target":"nope","diameter":3,"positions":[[1,1]]}`), PolicyAuto); o.OK {
		t.Fatal("a hole on a missing target must fail")
	}
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"plate","dx":40,"dy":30,"dz":4}`), PolicyAuto))
	mustOK(t, a.ApplyTool("add_hole", []byte(`{"target":"plate","diameter":3,"positions":[[5,5],[35,25]]}`), PolicyAuto))
	a.SetPromptSummary("Done: a plate with two holes.")
	a.SetActor(Actor{CreatedBy: "user"})
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"lid","dx":10,"dy":10,"dz":2}`), PolicyAuto))
	a.SetActor(Actor{CreatedBy: "agent"})
	a.RecordPrompt("Add a block")
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"block","dx":5,"dy":5,"dz":5}`), PolicyAuto))
	a.SetPromptSummary("Added the block.")

	sc := a.PlaybackScript()
	if len(sc) != 4 {
		t.Fatalf("one step per committed ChangeSet, got %d", len(sc))
	}
	roles := func(ls []ChatLine) (out []string) {
		for _, l := range ls {
			r := l.Role
			if l.Role == "tool" {
				r = l.Tool
				if !l.OK {
					r = "!" + r
				}
			}
			out = append(out, r)
		}
		return out
	}
	want := [][2][]string{
		{{"user", "!add_hole", "create_box"}, nil},
		{{"add_hole"}, {"agent"}},
		{{"system"}, nil},
		{{"user", "create_box"}, {"agent"}},
	}
	for i, w := range want {
		b, af := roles(sc[i].Before), roles(sc[i].After)
		if len(b) != len(w[0]) || len(af) != len(w[1]) {
			t.Fatalf("step %d: before %v after %v, want %v", i, b, af, w)
		}
		for j := range b {
			if b[j] != w[0][j] {
				t.Fatalf("step %d: before %v, want %v", i, b, w[0])
			}
		}
	}
	if sc[0].Before[0].Text != "A plate with two holes" || sc[0].Before[1].Text == "" || sc[0].Before[2].Text == "" {
		t.Fatalf("prompt, error and explanation must be shown: %+v", sc[0].Before)
	}
	if sc[1].After[0].Text != "Done: a plate with two holes." {
		t.Fatalf("summary %+v", sc[1].After)
	}
}

func TestPlaybackScriptPlaysPromptsThatCommittedNothing(t *testing.T) {
	a := New("demo", clock)
	t.Cleanup(a.Close)
	a.SetActor(Actor{CreatedBy: "agent", Capability: "geometry", Model: "recorded"})
	a.RecordPrompt("Create the part in the image")
	a.RecordPromptImages(a.promptID, []string{"img_1"})
	a.SetPromptSummary("Review the interpretation first.")
	a.RecordPrompt("Make it 40 wide")
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"plate","dx":40,"dy":30,"dz":4}`), PolicyAuto))
	a.SetPromptSummary("Created the plate.")
	a.RecordPrompt("Which material?")
	a.SetPromptSummary("Steel or aluminum?")
	a.RecordPrompt("A block too")
	mustOK(t, a.ApplyTool("create_box", []byte(`{"id":"block","dx":5,"dy":5,"dz":5}`), PolicyAuto))
	a.SetPromptSummary("Added the block.")
	a.RecordPrompt("Thanks")
	a.SetPromptSummary("You're welcome.")

	sc := a.PlaybackScript()
	if len(sc) != 2 {
		t.Fatalf("one step per committed ChangeSet, got %d", len(sc))
	}
	texts := func(ls []ChatLine) (out []string) {
		for _, l := range ls {
			if l.Role != "tool" {
				out = append(out, l.Text)
			}
		}
		return out
	}
	want := [][2][]string{
		{{"Create the part in the image", "Review the interpretation first.", "Make it 40 wide"}, {"Created the plate."}},
		{{"Which material?", "Steel or aluminum?", "A block too"}, {"Added the block.", "Thanks", "You're welcome."}},
	}
	for i, w := range want {
		b, af := texts(sc[i].Before), texts(sc[i].After)
		if strings.Join(b, "|") != strings.Join(w[0], "|") || strings.Join(af, "|") != strings.Join(w[1], "|") {
			t.Fatalf("step %d: before %q after %q, want %q", i, b, af, w)
		}
	}
	if im := sc[0].Before[0].Images; len(im) != 1 || im[0] != "img_1" {
		t.Fatalf("the first prompt must carry its image: %+v", sc[0].Before[0])
	}
}
