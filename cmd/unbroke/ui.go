package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/yug/unbroke/internal/catalog"
	"github.com/yug/unbroke/internal/ui"
)

const version = "0.1"

// runUI serves the web interface on the loopback address.
func runUI(args []string, out io.Writer) error {
	fs := newFlags("ui", "unbroke ui [-addr 127.0.0.1:8787] [-no-open] [repo-path]")
	addr := fs.String("addr", "127.0.0.1:8787", "address to listen on (loopback only)")
	noOpen := fs.Bool("no-open", false, "do not open a browser window")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	root := ""
	if fs.NArg() > 1 {
		return usagef("usage: unbroke ui [flags] [repo-path] (one path; got %d)", fs.NArg())
	}
	if fs.NArg() == 1 {
		if err := requireDir(fs.Arg(0)); err != nil {
			return err
		}
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
	// Past scans and fixes are kept next to the other local state. Losing the
	// history is not worth refusing to start, so a failure only turns it off.
	if home, herr := os.UserHomeDir(); herr == nil {
		if herr := srv.EnableHistory(filepath.Join(home, ".unbroke", "history")); herr != nil {
			fmt.Fprintln(os.Stderr, "unbroke: history is off:", herr)
		}
	}
	ln, err := srv.Listen(*addr)
	if errors.Is(err, syscall.EADDRINUSE) {
		// The preferred port is busy; take any free one on the same host.
		host, _, _ := net.SplitHostPort(*addr)
		var lerr error
		if ln, lerr = srv.Listen(net.JoinHostPort(host, "0")); lerr != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "unbroke: %s is in use, so another port was chosen\n", *addr)
	} else if err != nil {
		return err
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
