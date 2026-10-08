// simoncad is the desktop shell. It serves the viewport and panels from the embedded
// frontend on a loopback port, backed by the same app services as the CLI, and opens them
// in the system browser. The Wails webview shell binds the identical ui.Service methods
// (see docs/ui.md); only this launcher differs.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	goruntime "runtime"
	"time"

	"github.com/accelone/simoncad/frontend"
	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/config"
	"github.com/accelone/simoncad/internal/dotenv"
	"github.com/accelone/simoncad/internal/ui"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "loopback address to serve on")
	project := flag.String("project", "", "open this .scadproj (created on first save if it does not exist)")
	recording := flag.String("recording", "", "answer every agent turn from this recorded run instead of calling the API (tests, demos)")
	noBrowser := flag.Bool("no-browser", false, "print the URL instead of opening a browser")
	flag.Parse()
	dotenv.Load(".env")
	if err := config.Load(); err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}

	host, _, err := net.SplitHostPort(*addr)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		fmt.Fprintln(os.Stderr, "simoncad: --addr must be a loopback address (the UI has no authentication beyond a per-run token)")
		os.Exit(2)
	}

	var a *app.App
	path := *project
	if path != "" {
		if _, statErr := os.Stat(path); statErr == nil {
			if a, err = app.Open(path, time.Now); err != nil {
				fmt.Fprintln(os.Stderr, "simoncad:", err)
				os.Exit(1)
			}
		}
	}
	if a == nil {
		a = app.New("untitled", time.Now)
	}
	svc := ui.NewService(a, path)
	if *recording != "" {
		raw, err := os.ReadFile(*recording)
		var rec runtime.Recording
		if err == nil {
			err = json.Unmarshal(raw, &rec)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "simoncad: --recording:", err)
			os.Exit(1)
		}
		svc.NewProvider = func() (runtime.Provider, error) { return &runtime.Replay{Rec: rec}, nil }
	}
	defer svc.Close()

	srv := ui.NewServer(svc, frontend.FS())
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "simoncad:", err)
		os.Exit(1)
	}
	url := fmt.Sprintf("http://%s/", ln.Addr())
	fmt.Println("Simon CAD UI:", url)
	if !*noBrowser {
		openBrowser(url)
	}

	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(sctx)
	}()
	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "simoncad:", err)
		os.Exit(1)
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch goruntime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
