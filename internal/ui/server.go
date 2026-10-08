package ui

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/config"
	"github.com/accelone/simoncad/internal/vision"
)

// Server exposes a Service over HTTP on the loopback interface. It is the development and
// test shell for the same adapters the Wails desktop app binds; every handler is a one-line
// call into Service, so Wails bindings are a mechanical mapping.
type Server struct {
	Svc   *Service
	Token string
	mux   *http.ServeMux
	web   fs.FS
}

// NewServer wires the API and serves the static frontend from web.
func NewServer(svc *Service, web fs.FS) *Server {
	var b [16]byte
	_, _ = rand.Read(b[:])
	s := &Server{Svc: svc, Token: hex.EncodeToString(b[:]), mux: http.NewServeMux(), web: web}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.guard(s.mux) }

// guard rejects cross-site and DNS-rebinding requests: the API only answers requests that
// carry the per-run token and address the loopback host the server is bound to.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" && host != "[::1]" && host != "::1" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			tok := r.Header.Get("X-Simon-Token")
			if tok == "" {
				tok = r.URL.Query().Get("token") // EventSource cannot set headers
			}
			if tok != s.Token {
				http.Error(w, "missing or bad token", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error) {
	// A request that needs the remote fallback the user has not authorized yet: the front
	// end asks (Allow / Cancel) and sends it again.
	var ce *runtime.ConsentError
	if errors.As(err, &ce) && !ce.Declined {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "code": "fallback_consent",
			"selected": ce.Selected, "fallback": ce.Fallback})
		return
	}
	code := http.StatusBadRequest
	if err == ErrBusy {
		code = http.StatusConflict
	}
	writeJSON(w, code, map[string]any{"ok": false, "error": err.Error()})
}

func decode(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// reply writes v or the error.
func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		st, err := s.Svc.State()
		reply(w, st, err)
	})
	m.HandleFunc("/api/scene", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Known map[string]string `json:"known"`
		}
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		d, err := s.Svc.Scene(in.Known)
		reply(w, d, err)
	})
	m.HandleFunc("/api/pick", func(w http.ResponseWriter, r *http.Request) {
		in := PickRequest{Triangle: -1}
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		v, err := s.Svc.Pick(in)
		reply(w, v, err)
	})
	m.HandleFunc("/api/select", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Selectors []string `json:"selectors"`
		}
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		v, err := s.Svc.SetSelection(in.Selectors)
		reply(w, v, err)
	})
	m.HandleFunc("/api/prompt", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text     string   `json:"text"`
			Thinking *bool    `json:"thinking"` // omitted = on
			Images   []string `json:"images"`   // ids returned by /api/image
		}
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		if err := s.Svc.PromptWith(in.Text, in.Thinking == nil || *in.Thinking, in.Images); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
	})
	m.HandleFunc("/api/image", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST the raw image bytes", http.StatusMethodNotAllowed)
			return
		}
		// Raw body (not JSON): the 4 MB JSON limit does not apply, images have their own.
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, vision.MaxImageBytes+1))
		if err != nil {
			fail(w, fmt.Errorf("image is larger than %d MB or unreadable", vision.MaxImageBytes>>20))
			return
		}
		info, err := s.Svc.UploadImage(b)
		reply(w, info, err)
	})
	m.HandleFunc("/api/spec", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST the raw document (text or PDF) with ?name=", http.StatusMethodNotAllowed)
			return
		}
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDocumentBytes+1))
		if err != nil {
			fail(w, fmt.Errorf("document is larger than %d MB or unreadable", maxDocumentBytes>>20))
			return
		}
		sp, err := s.Svc.AddAttachment(r.URL.Query().Get("name"), b)
		reply(w, sp, err)
	})
	m.HandleFunc("/api/spec/remove", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name string `json:"name"`
		}
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		reply(w, map[string]any{"ok": true}, s.Svc.RemoveSpec(in.Name))
	})
	m.HandleFunc("/api/image/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/image/"), "/")
		if len(parts) == 2 && parts[1] == "retain" && r.Method == http.MethodPost {
			var in struct {
				Keep bool `json:"keep"`
			}
			if err := decode(r, &in); err != nil {
				fail(w, err)
				return
			}
			reply(w, map[string]any{"ok": true}, s.Svc.RetainImage(parts[0], in.Keep))
			return
		}
		if len(parts) != 1 {
			http.NotFound(w, r)
			return
		}
		data, mt, ok := s.Svc.ImageData(parts[0])
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mt)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "private, max-age=3600")
		_, _ = w.Write(data)
	})
	// ---- visual refinement ----
	pngOut := func(w http.ResponseWriter, data []byte) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "private, max-age=3600")
		_, _ = w.Write(data)
	}
	m.HandleFunc("/api/visual", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, s.Svc.Visual()) })
	m.HandleFunc("/api/render/", func(w http.ResponseWriter, r *http.Request) {
		data, ok := s.Svc.RenderPNG(strings.TrimPrefix(r.URL.Path, "/api/render/"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		pngOut(w, data)
	})
	m.HandleFunc("/api/visual/diff", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		n, _ := strconv.Atoi(q.Get("iteration"))
		data, err := s.Svc.VisualDiff(n, q.Get("ref"), q.Get("previous") == "1")
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		pngOut(w, data)
	})
	m.HandleFunc("/api/visual/run", func(w http.ResponseWriter, r *http.Request) {
		var in RefineRequest
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		if err := s.Svc.RunRefinement(in); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
	})
	m.HandleFunc("/api/visual/stop", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"ok": true}, s.Svc.StopRefinement())
	})
	iterationAction := func(f func(int) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Iteration int `json:"iteration"`
			}
			if err := decode(r, &in); err != nil {
				fail(w, err)
				return
			}
			reply(w, map[string]any{"ok": true}, f(in.Iteration))
		}
	}
	m.HandleFunc("/api/visual/accept", iterationAction(s.Svc.AcceptIteration))
	m.HandleFunc("/api/visual/revert", iterationAction(s.Svc.RevertIteration))
	m.HandleFunc("/api/visual/lock", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Region string `json:"region"`
			Locked bool   `json:"locked"`
		}
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		reply(w, map[string]any{"ok": true}, s.Svc.SetRegionLock(in.Region, in.Locked))
	})
	m.HandleFunc("/api/draft/keep", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"ok": true}, s.Svc.KeepDraft())
	})
	m.HandleFunc("/api/draft/discard", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"ok": true}, s.Svc.DiscardDraft())
	})
	m.HandleFunc("/api/chat/clear", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"ok": true}, s.Svc.ClearChat())
	})
	m.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusOK, config.Current())
			return
		}
		var in config.Settings
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		if err := config.Save(in); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, config.Current())
	})
	m.HandleFunc("/api/fallback/consent", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Allow    bool `json:"allow"`
			Remember bool `json:"remember"`
		}
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		reply(w, map[string]any{"ok": true}, s.Svc.AuthorizeFallback(in.Allow, in.Remember))
	})
	m.HandleFunc("/api/level", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Level string `json:"level"`
		}
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		reply(w, map[string]any{"ok": true}, s.Svc.SetLevel(in.Level))
	})
	m.HandleFunc("/api/tool", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		}
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		o, err := s.Svc.CallTool(in.Name, in.Args)
		reply(w, o, err)
	})
	m.HandleFunc("/api/delete", func(w http.ResponseWriter, r *http.Request) {
		var in DeleteRequest
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		o, err := s.Svc.Delete(in)
		reply(w, o, err)
	})
	m.HandleFunc("/api/param", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Feature, Parameter string
			Value              any
		}
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		o, err := s.Svc.SetParameter(in.Feature, in.Parameter, in.Value)
		reply(w, o, err)
	})
	m.HandleFunc("/api/changeset/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/changeset/"), "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		id := parts[0]
		switch parts[1] {
		case "preview":
			g, err := s.Svc.Preview(id)
			reply(w, g, err)
		case "commit":
			c, err := s.Svc.Commit(id)
			reply(w, c, err)
		case "reject":
			reply(w, map[string]any{"ok": true}, s.Svc.Reject(id))
		default:
			http.NotFound(w, r)
		}
	})
	m.HandleFunc("/api/undo", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.Svc.Undo()
		reply(w, c, err)
	})
	m.HandleFunc("/api/redo", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.Svc.Redo()
		reply(w, c, err)
	})
	m.HandleFunc("/api/restore", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Checkpoint string `json:"checkpoint"`
		}
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		o, err := s.Svc.Restore(in.Checkpoint)
		reply(w, o, err)
	})
	m.HandleFunc("/api/compare", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.Svc.Compare(r.URL.Query().Get("a"), r.URL.Query().Get("b"))
		reply(w, c, err)
	})
	m.HandleFunc("/api/materials", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.Svc.Materials())
	})
	m.HandleFunc("/api/materials/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/materials/"), "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		data, ct, ok := s.Svc.MaterialFile(parts[0], parts[1])
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-cache") // project materials can change under the same id
		_, _ = w.Write(data)
	})
	m.HandleFunc("/api/palette", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.Svc.Palette())
	})
	m.HandleFunc("/api/playback", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Index int               `json:"index"`
			Known map[string]string `json:"known"`
		}
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		f, err := s.Svc.PlaybackFrame(in.Index, in.Known)
		reply(w, f, err)
	})
	m.HandleFunc("/api/project/new", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Name string }
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		reply(w, map[string]any{"ok": true}, s.Svc.New(in.Name))
	})
	m.HandleFunc("/api/project/list", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.Svc.ListProjects())
	})
	m.HandleFunc("/api/project/open", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Path string }
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		reply(w, map[string]any{"ok": true}, s.Svc.Open(in.Path))
	})
	m.HandleFunc("/api/project/save", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Path string }
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		p, err := s.Svc.Save(in.Path)
		reply(w, map[string]any{"ok": true, "path": p}, err)
	})
	m.HandleFunc("/api/project/export", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Format, Path string }
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		o, err := s.Svc.Export(in.Format, in.Path)
		reply(w, o, err)
	})
	m.HandleFunc("/api/components", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Query string }
		if r.Method == http.MethodPost {
			if err := decode(r, &in); err != nil {
				fail(w, err)
				return
			}
		} else {
			in.Query = r.URL.Query().Get("q")
		}
		writeJSON(w, http.StatusOK, s.Svc.Components(in.Query))
	})
	m.HandleFunc("/api/components/open", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Component string }
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		p, err := s.Svc.OpenComponent(in.Component)
		reply(w, map[string]any{"ok": true, "path": p}, err)
	})
	m.HandleFunc("/api/components/rename", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Component, Name, Description string
			Tags                         []string
		}
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		reply(w, map[string]any{"ok": true}, s.Svc.RenameComponent(in.Component, in.Name, in.Description, in.Tags))
	})
	m.HandleFunc("/api/components/delete", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Component string }
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		reply(w, map[string]any{"ok": true}, s.Svc.DeleteComponent(in.Component))
	})
	m.HandleFunc("/api/components/publish", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Reusable    bool
			Description string
			Tags        []string
		}
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		reply(w, map[string]any{"ok": true}, s.Svc.PublishProject(in.Reusable, in.Description, in.Tags))
	})
	m.HandleFunc("/api/events", s.events)

	m.HandleFunc("/", s.static)
}

// events streams Hub notices as server-sent events.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch, cancel := s.Svc.Hub().Subscribe()
	defer cancel()
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case n := <-ch:
			b, _ := json.Marshal(n)
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
	}
}

// static serves the embedded frontend; index.html gets the run token injected.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	b, err := fs.ReadFile(s.web, p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(p, ".html"):
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		b = []byte(strings.ReplaceAll(string(b), "__SIMON_TOKEN__", s.Token))
	case strings.HasSuffix(p, ".js"), strings.HasSuffix(p, ".mjs"):
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case strings.HasSuffix(p, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}
