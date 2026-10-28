package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/yug/unbroke/internal/catalog"
	"github.com/yug/unbroke/internal/ui"
)

const version = "0.1"

// runUI serves the web interface on the loopback address.
func runUI(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8787", "address to listen on (loopback only)")
	noOpen := fs.Bool("no-open", false, "do not open a browser window")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return err
	}
	root := ""
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: unbroke ui [flags] [repo-path]")
	}
	if fs.NArg() == 1 {
		abs, err := filepath.Abs(fs.Arg(0))
		if err != nil {
			return err
		}
		root = abs
	}
	cat, err := catalog.Load()
	if err != nil {
		return err
	}
	srv, err := ui.New(version, root, cat)
	if err != nil {
		return err
	}
	ln, err := srv.Listen(*addr)
	if err != nil {
		// The preferred port is busy; take any free one on the same host.
		host := "127.0.0.1"
		var lerr error
		if ln, lerr = srv.Listen(host + ":0"); lerr != nil {
			return err
		}
	}
	url := "http://" + ln.Addr().String() + "/"
	fmt.Fprintf(out, "Unbroke is running at %s\nPress Ctrl+C to stop.\n", url)
	if !*noOpen {
		openBrowser(url)
	}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}
	cmd.Stdout, cmd.Stderr = nil, nil
	_ = cmd.Start()
}

var _ = os.Stderr
