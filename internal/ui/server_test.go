package ui

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	web := fstest.MapFS{"index.html": {Data: []byte(`<meta name="simon-token" content="__SIMON_TOKEN__">`)},
		"app.js": {Data: []byte("export {}")}}
	srv := NewServer(newSvc(t), web)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func call(t *testing.T, ts *httptest.Server, tok, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if tok != "" {
		req.Header.Set("X-Simon-Token", tok)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	b, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(b, &out)
	return res.StatusCode, out
}

func TestServerRejectsMissingTokenAndForeignHost(t *testing.T) {
	srv, ts := testServer(t)
	if code, _ := call(t, ts, "", "GET", "/api/state", ""); code != http.StatusForbidden {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := call(t, ts, "wrong", "GET", "/api/state", ""); code != http.StatusForbidden {
		t.Fatalf("bad token: %d", code)
	}
	if code, _ := call(t, ts, srv.Token, "GET", "/api/state", ""); code != http.StatusOK {
		t.Fatalf("good token: %d", code)
	}
	// DNS rebinding: a page on evil.example that resolves to 127.0.0.1 must not reach the API.
	req, _ := http.NewRequest("GET", ts.URL+"/api/state", nil)
	req.Host = "evil.example"
	req.Header.Set("X-Simon-Token", srv.Token)
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign host: %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestServerServesFrontendWithToken(t *testing.T) {
	srv, ts := testServer(t)
	res, _ := http.Get(ts.URL + "/")
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), srv.Token) || strings.Contains(string(b), "__SIMON_TOKEN__") {
		t.Fatalf("token not injected: %s", b)
	}
	res, _ = http.Get(ts.URL + "/app.js")
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Fatalf("js content type %q", ct)
	}
	res.Body.Close()
	if res, _ = http.Get(ts.URL + "/nope.js"); res.StatusCode != 404 {
		t.Fatalf("missing file: %d", res.StatusCode)
	}
}

func TestServerEndpointsEndToEnd(t *testing.T) {
	srv, ts := testServer(t)
	tok := srv.Token
	if err := srv.Svc.SetLevel(LevelAuto); err != nil {
		t.Fatal(err)
	}
	code, out := call(t, ts, tok, "POST", "/api/tool", `{"name":"create_box","args":{"id":"p","dx":30,"dy":20,"dz":5}}`)
	if code != 200 || out["committed"] != true {
		t.Fatalf("tool: %d %v", code, out)
	}
	code, out = call(t, ts, tok, "POST", "/api/scene", `{"known":{}}`)
	if code != 200 || len(out["changed"].([]any)) != 1 {
		t.Fatalf("scene: %d %v", code, out)
	}
	keys := out["keys"].(map[string]any)
	code, out = call(t, ts, tok, "POST", "/api/scene", mustJSON(map[string]any{"known": keys}))
	if code != 200 || len(out["changed"].([]any)) != 0 {
		t.Fatalf("a client that is up to date must receive no bodies: %v", out)
	}
	code, out = call(t, ts, tok, "POST", "/api/pick", `{"body":"p","kind":"face","index":0}`)
	if code != 200 || len(out["selectors"].([]any)) != 1 {
		t.Fatalf("pick: %d %v", code, out)
	}
	code, out = call(t, ts, tok, "POST", "/api/pick", `{"body":"p","kind":"face","index":42}`)
	if code != 400 || out["error"] == nil {
		t.Fatalf("bad pick: %d %v", code, out)
	}
	if code, _ = call(t, ts, tok, "GET", "/api/palette", ""); code != 200 {
		t.Fatalf("palette %d", code)
	}
	if code, out = call(t, ts, tok, "POST", "/api/changeset/cs_9999/preview", ""); code != 400 {
		t.Fatalf("unknown preview: %d %v", code, out)
	}
	if code, _ = call(t, ts, tok, "POST", "/api/undo", ""); code != 200 {
		t.Fatalf("undo %d", code)
	}
	if code, out = call(t, ts, tok, "POST", "/api/project/export", `{"format":"obj","path":"x"}`); code != 400 {
		t.Fatalf("bad export format: %d %v", code, out)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestEventStreamDeliversNotices(t *testing.T) {
	srv, ts := testServer(t)
	req, _ := http.NewRequest("GET", ts.URL+"/api/events?token="+srv.Token, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	got := make(chan string, 4)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data: ") {
				got <- sc.Text()
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	srv.Svc.Hub().Publish(Notice{Kind: "state"})
	select {
	case line := <-got:
		if !strings.Contains(line, `"kind":"state"`) {
			t.Fatalf("event %s", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event received")
	}
}

// An idle stream sends a comment line now and then, so a client can tell a quiet server from
// a dead connection and proxies keep the connection open.
func TestEventStreamSendsHeartbeats(t *testing.T) {
	defer func(d time.Duration) { sseHeartbeat = d }(sseHeartbeat)
	sseHeartbeat = 20 * time.Millisecond
	srv, ts := testServer(t)
	req, _ := http.NewRequest("GET", ts.URL+"/api/events?token="+srv.Token, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	pings := make(chan struct{}, 8)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if sc.Text() == ": ping" {
				pings <- struct{}{}
			}
		}
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-pings:
		case <-time.After(2 * time.Second):
			t.Fatalf("heartbeat %d never arrived", i+1)
		}
	}
}
