package vision

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// Interpretation is one proposal under discussion. Corrections create new revisions of the
// proposal; none of this touches the CAD model.
type Interpretation struct {
	ID       string    `json:"id"`
	Proposal *Proposal `json:"proposal"`
	Scale    *Scale    `json:"scale,omitempty"` // set explicitly by the user (set_sketch_scale)
	Revision int       `json:"revision"`
	// Used lists the CAD-IR nodes created from this interpretation.
	Used []string `json:"used,omitempty"`
	// Closed marks an interpretation that no longer needs attention: the user discarded it or
	// the model was built another way. It stays in the store for audit, but the agent stops
	// carrying the image tools for it and the UI hides its card.
	Closed bool `json:"closed,omitempty"`
}

// Open reports whether the interpretation still waits for the user or the agent.
func (i *Interpretation) Open() bool { return !i.Closed && len(i.Used) == 0 }

// Report validates the current revision.
func (i *Interpretation) Report() *Report { return Validate(i.Proposal, i.Scale) }

// Store holds the interpretations of a session. It is serialised into agent-history.json so
// the reasoning behind image-derived features stays auditable.
type Store struct {
	mu    sync.Mutex
	seq   int
	items map[string]*Interpretation
	order []string
}

func NewStore() *Store { return &Store{items: map[string]*Interpretation{}} }

// Add registers a new proposal and returns its interpretation (int_0001, ...).
func (s *Store) Add(p *Proposal) *Interpretation {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	it := &Interpretation{ID: fmt.Sprintf("int_%04d", s.seq), Proposal: p.Clone(), Revision: 1}
	s.items[it.ID] = it
	s.order = append(s.order, it.ID)
	return it
}

// Get returns a copy, so callers cannot mutate the store behind its back.
func (s *Store) Get(id string) (*Interpretation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[id]
	if !ok {
		return nil, false
	}
	return it.clone(), true
}

func (i *Interpretation) clone() *Interpretation {
	c := *i
	c.Proposal = i.Proposal.Clone()
	if i.Scale != nil {
		sc := *i.Scale
		c.Scale = &sc
	}
	c.Used = append([]string(nil), i.Used...)
	return &c
}

// Latest is the most recent interpretation, "" if there is none.
func (s *Store) Latest() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.order) == 0 {
		return ""
	}
	return s.order[len(s.order)-1]
}

// List returns copies of every interpretation in creation order.
func (s *Store) List() []*Interpretation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Interpretation, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.items[id].clone())
	}
	return out
}

// Modify applies fn to a copy of the interpretation and, if it succeeds, stores it as a new
// revision. A failing edit leaves the stored interpretation untouched.
func (s *Store) Modify(id string, fn func(*Interpretation) error) (*Interpretation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[id]
	if !ok {
		return nil, fmt.Errorf("unknown interpretation %q (known: %v)", id, s.order)
	}
	c := it.clone()
	if err := fn(c); err != nil {
		return nil, err
	}
	c.Revision = it.Revision + 1
	s.items[id] = c
	return c.clone(), nil
}

// MarkUsed records the nodes an interpretation produced (called when its ChangeSet commits).
func (s *Store) MarkUsed(id string, nodes []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if it, ok := s.items[id]; ok {
		it.Used = append(it.Used[:0:0], nodes...)
	}
}

// Close marks an interpretation as no longer needing attention.
func (s *Store) Close(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if it, ok := s.items[id]; ok {
		it.Closed = true
	}
}

// CloseOpen closes every interpretation that is still open.
func (s *Store) CloseOpen() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.items {
		if it.Open() {
			it.Closed = true
		}
	}
}

// HasOpen reports whether any interpretation is still open.
func (s *Store) HasOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.items {
		if it.Open() {
			return true
		}
	}
	return false
}

type storeJSON struct {
	Seq   int               `json:"seq"`
	Items []*Interpretation `json:"items"`
}

func (s *Store) MarshalJSON() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := storeJSON{Seq: s.seq, Items: []*Interpretation{}}
	for _, id := range s.order {
		out.Items = append(out.Items, s.items[id])
	}
	return json.Marshal(out)
}

func (s *Store) UnmarshalJSON(b []byte) error {
	var in storeJSON
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq, s.items, s.order = in.Seq, map[string]*Interpretation{}, nil
	for _, it := range in.Items {
		s.items[it.ID] = it
		s.order = append(s.order, it.ID)
	}
	return nil
}

// ---- corrections ------------------------------------------------------------------------

// Edit is one correction to a proposal.
//
//	set_entity | set_profile | set_dimension | set_operation   id + value (merged onto the existing item, or a new item)
//	remove_entity | remove_profile | remove_dimension | remove_operation   id
//	set_relationship (id = index, "new" appends) | remove_relationship (id = index)
//	set_placement   value
//
// In a set_* value, a null field removes that field.
type Edit struct {
	Op    string         `json:"op"`
	ID    string         `json:"id,omitempty"`
	Value map[string]any `json:"value,omitempty"`
}

// ApplyEdits applies edits in order to p. It stops at the first invalid edit; the caller
// discards p in that case (Store.Modify works on a copy).
func ApplyEdits(p *Proposal, edits []Edit) error {
	for i, ed := range edits {
		if err := applyEdit(p, ed); err != nil {
			return fmt.Errorf("edit %d (%s %s): %w", i+1, ed.Op, ed.ID, err)
		}
	}
	return nil
}

func overlay(base, patch map[string]any) {
	for k, v := range patch {
		if v == nil {
			delete(base, k)
		} else {
			base[k] = v
		}
	}
}

// mergeJSON overlays patch onto the JSON form of *dst.
func mergeJSON(dst any, patch map[string]any) error {
	b, err := json.Marshal(dst)
	if err != nil {
		return err
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	overlay(m, patch)
	b, _ = json.Marshal(m)
	return json.Unmarshal(b, dst)
}

func applyEdit(p *Proposal, ed Edit) error {
	needID := func() error {
		if ed.ID == "" {
			return fmt.Errorf("id is required")
		}
		return nil
	}
	switch ed.Op {
	case "set_entity":
		if err := needID(); err != nil {
			return err
		}
		if e := p.entity(ed.ID); e != nil {
			overlay(e, ed.Value)
			return nil
		}
		e := Entity{"id": ed.ID}
		overlay(e, ed.Value)
		p.Entities = append(p.Entities, e)
	case "remove_entity":
		for i, e := range p.Entities {
			if e.ID() == ed.ID {
				p.Entities = append(p.Entities[:i], p.Entities[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("no such entity")
	case "set_profile":
		if err := needID(); err != nil {
			return err
		}
		if pr := p.profile(ed.ID); pr != nil {
			return mergeJSON(pr, ed.Value)
		}
		pr := Profile{ID: ed.ID}
		if err := mergeJSON(&pr, ed.Value); err != nil {
			return err
		}
		p.Profiles = append(p.Profiles, pr)
	case "remove_profile":
		for i, pr := range p.Profiles {
			if pr.ID == ed.ID {
				p.Profiles = append(p.Profiles[:i], p.Profiles[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("no such profile")
	case "set_dimension":
		if err := needID(); err != nil {
			return err
		}
		if d := p.dimension(ed.ID); d != nil {
			return mergeJSON(d, ed.Value)
		}
		d := Dimension{ID: ed.ID}
		if err := mergeJSON(&d, ed.Value); err != nil {
			return err
		}
		p.Dimensions = append(p.Dimensions, d)
	case "remove_dimension":
		for i, d := range p.Dimensions {
			if d.ID == ed.ID {
				p.Dimensions = append(p.Dimensions[:i], p.Dimensions[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("no such dimension")
	case "set_operation":
		if err := needID(); err != nil {
			return err
		}
		if o := p.operation(ed.ID); o != nil {
			return mergeJSON(o, ed.Value)
		}
		o := Operation{ID: ed.ID}
		if err := mergeJSON(&o, ed.Value); err != nil {
			return err
		}
		p.Operations = append(p.Operations, o)
	case "remove_operation":
		for i, o := range p.Operations {
			if o.ID == ed.ID {
				p.Operations = append(p.Operations[:i], p.Operations[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("no such operation")
	case "set_relationship":
		if ed.ID == "new" || ed.ID == "" {
			var r Relationship
			if err := mergeJSON(&r, ed.Value); err != nil {
				return err
			}
			p.Relationships = append(p.Relationships, r)
			return nil
		}
		var idx int
		if _, err := fmt.Sscanf(ed.ID, "%d", &idx); err != nil || idx < 0 || idx >= len(p.Relationships) {
			return fmt.Errorf("relationship index %q out of range", ed.ID)
		}
		return mergeJSON(&p.Relationships[idx], ed.Value)
	case "remove_relationship":
		var idx int
		if _, err := fmt.Sscanf(ed.ID, "%d", &idx); err != nil || idx < 0 || idx >= len(p.Relationships) {
			return fmt.Errorf("relationship index %q out of range", ed.ID)
		}
		p.Relationships = append(p.Relationships[:idx], p.Relationships[idx+1:]...)
	case "set_placement":
		return mergeJSON(&p.Placement, ed.Value)
	default:
		return fmt.Errorf("unknown edit op %q", ed.Op)
	}
	return nil
}

// ConfirmDimension marks a dimension as confirmed by the user, optionally correcting its value.
func ConfirmDimension(p *Proposal, id string, value *float64) error {
	d := p.dimension(id)
	if d == nil {
		ids := make([]string, 0, len(p.Dimensions))
		for _, x := range p.Dimensions {
			ids = append(ids, x.ID)
		}
		sort.Strings(ids)
		return fmt.Errorf("unknown dimension %q (known: %v)", id, ids)
	}
	if value != nil {
		if !(*value > 0) {
			return fmt.Errorf("dimension value must be > 0")
		}
		v := *value
		d.Value = &v
	}
	if d.Value == nil {
		return fmt.Errorf("dimension %q has no value; give one", id)
	}
	d.Source, d.Confidence = "confirmed", 1
	return nil
}
