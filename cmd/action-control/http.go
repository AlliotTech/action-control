package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

//go:embed all:web/dist
var webFiles embed.FS

func sameOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return u.Scheme == scheme && u.Host == r.Host && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
func apiDispatch(mux *http.ServeMux, w http.ResponseWriter, r *http.Request) {
	handler, pattern := mux.Handler(r)
	if pattern != "" {
		handler.ServeHTTP(w, r)
		return
	}
	allowed := []string{}
	for _, method := range []string{"GET", "HEAD", "POST", "DELETE"} {
		copy := r.Clone(r.Context())
		copy.Method = method
		if _, pattern := mux.Handler(copy); pattern != "" {
			allowed = append(allowed, method)
		}
	}
	if len(allowed) > 0 {
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		jsonError(w, 405, "method_not_allowed", errors.New("method not allowed"))
		return
	}
	jsonError(w, 404, "not_found", errors.New("unknown API"))
}
func newHandler(a *App) (http.Handler, func() error, error) {
	api := http.NewServeMux()
	registerConfig(api, a)
	closers := []func() error{}
	closeAll := func() error {
		var err error
		for i := len(closers) - 1; i >= 0; i-- {
			err = errors.Join(err, closers[i]())
		}
		closers = nil
		return err
	}
	for _, register := range []func(*http.ServeMux, *App) (func() error, error){RegisterFiles, RegisterNetwork, RegisterSystem, RegisterCamera} {
		close, err := register(api, a)
		if err != nil {
			_ = closeAll()
			return nil, nil, err
		}
		if close != nil {
			closers = append(closers, close)
		}
	}
	api.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		port := "8080"
		if address, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
			if _, localPort, err := net.SplitHostPort(address.String()); err == nil {
				port = localPort
			}
		}
		jsonResponse(w, 200, map[string]any{"ok": true, "version": Version, "device": a.RequireDevice() == nil, "http_port": port})
	})
	staticFS, err := fs.Sub(webFiles, "web/dist")
	if err != nil {
		_ = closeAll()
		return nil, nil, err
	}
	static := http.FileServerFS(staticFS)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' blob: data:; media-src 'self' blob:; connect-src 'self'; worker-src 'self' blob:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			if r.Method != "GET" && r.Method != "HEAD" {
				if !sameOrigin(r) {
					jsonError(w, 403, "origin_forbidden", errors.New("cross-origin mutations are forbidden"))
					return
				}
			}
			apiDispatch(api, w, r)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			http.Error(w, "method not allowed", 405)
			return
		}
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		st, err := fs.Stat(staticFS, name)
		if err != nil || st.IsDir() {
			http.NotFound(w, r)
			return
		}
		static.ServeHTTP(w, r)
	})
	return handler, closeAll, nil
}

func serve(root, listen string) (restart bool, err error) {
	if root == "/" {
		probe, e := appAt(root)
		if e != nil {
			return false, e
		}
		e = probe.RequireManaged()
		probe.cancel()
		if e != nil {
			return false, e
		}
	}
	a, err := NewApp(root)
	if err != nil {
		return false, err
	}
	defer a.cancel()
	handler, closeAll, err := newHandler(a)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, closeAll()) }()
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return false, err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 * 1024}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	fmt.Printf("Action Control %s listening on http://%s\n", Version, listener.Addr())
	if a.RequireDevice() != nil {
		fmt.Println("Local filesystem mode: hardware operations are unavailable.")
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case <-a.Restart:
		restart = true
	case <-signals:
	case e := <-finished:
		if !errors.Is(e, http.ErrServerClosed) {
			return false, e
		}
		return false, nil
	}
	a.cancel()
	err = closeAll()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e := server.Shutdown(ctx); e != nil {
		_ = server.Close()
		err = errors.Join(err, e)
	}
	return restart, err
}
