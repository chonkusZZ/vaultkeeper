// Package restserver implements the restic REST backend protocol (v2) so a
// destination agent can accept backups directly from source agents.
package restserver

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
)

var (
	repoRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	nameRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
	types  = map[string]bool{"data": true, "index": true, "keys": true, "locks": true, "snapshots": true}
)

type Server struct {
	Root  string
	token atomic.Value // string
}

func New(root string) *Server {
	s := &Server{Root: root}
	s.token.Store("")
	return s
}

func (s *Server) SetToken(t string) { s.token.Store(t) }

// Authorized reports whether the request carries the agent data token.
func (s *Server) Authorized(r *http.Request) bool { return s.authorized(r) }

func (s *Server) authorized(r *http.Request) bool {
	tok, _ := s.token.Load().(string)
	if tok == "" {
		return false // refuse until the manager has provisioned a token
	}
	_, pw, ok := r.BasicAuth()
	return ok && subtle.ConstantTimeCompare([]byte(pw), []byte(tok)) == 1
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="vaultkeeper"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 0 || !repoRe.MatchString(parts[0]) {
		http.Error(w, "bad repo", http.StatusBadRequest)
		return
	}
	repo := filepath.Join(s.Root, parts[0])
	rest := parts[1:]

	switch {
	case len(rest) == 0:
		if r.Method == http.MethodPost && r.URL.Query().Get("create") == "true" {
			s.create(w, repo)
			return
		}
		if r.Method == http.MethodDelete {
			http.Error(w, "repository deletion not permitted", http.StatusForbidden)
			return
		}
		http.NotFound(w, r)
	case rest[0] == "config" && len(rest) == 1:
		s.blob(w, r, filepath.Join(repo, "config"), true)
	case types[rest[0]] && len(rest) == 1:
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		s.list(w, repo, rest[0])
	case types[rest[0]] && len(rest) == 2 && nameRe.MatchString(rest[1]):
		s.blob(w, r, s.blobPath(repo, rest[0], rest[1]), false)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) blobPath(repo, typ, name string) string {
	if typ == "data" {
		return filepath.Join(repo, "data", name[:2], name)
	}
	return filepath.Join(repo, typ, name)
}

func (s *Server) create(w http.ResponseWriter, repo string) {
	if _, err := os.Stat(filepath.Join(repo, "config")); err == nil {
		http.Error(w, "repository already exists", http.StatusConflict)
		return
	}
	dirs := []string{"data", "index", "keys", "locks", "snapshots"}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o700); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	for i := 0; i < 256; i++ {
		_ = os.MkdirAll(filepath.Join(repo, "data", twoHex(i)), 0o700)
	}
}

func twoHex(i int) string {
	const h = "0123456789abcdef"
	return string([]byte{h[i>>4], h[i&15]})
}

func (s *Server) list(w http.ResponseWriter, repo, typ string) {
	type entry struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	out := []entry{}
	base := filepath.Join(repo, typ)
	walk := func(dir string) {
		es, _ := os.ReadDir(dir)
		for _, e := range es {
			if e.IsDir() || !nameRe.MatchString(e.Name()) {
				continue
			}
			if fi, err := e.Info(); err == nil {
				out = append(out, entry{e.Name(), fi.Size()})
			}
		}
	}
	if typ == "data" {
		subs, _ := os.ReadDir(base)
		for _, d := range subs {
			if d.IsDir() {
				walk(filepath.Join(base, d.Name()))
			}
		}
	} else {
		walk(base)
	}
	w.Header().Set("Content-Type", "application/vnd.x.restic.rest.v2")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) blob(w http.ResponseWriter, r *http.Request, path string, isConfig bool) {
	switch r.Method {
	case http.MethodHead:
		fi, err := os.Stat(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", itoa(fi.Size()))
	case http.MethodGet:
		f, err := os.Open(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		fi, _ := f.Stat()
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, "", fi.ModTime(), f) // handles Range
	case http.MethodPost:
		if _, err := os.Stat(path); err == nil {
			http.Error(w, "already exists", http.StatusForbidden)
			return
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_, err = io.Copy(tmp, r.Body)
		if err == nil {
			err = tmp.Sync()
		}
		tmp.Close()
		if err == nil {
			err = os.Rename(tmp.Name(), path)
		}
		if err != nil {
			os.Remove(tmp.Name())
			http.Error(w, err.Error(), 500)
			return
		}
	case http.MethodDelete:
		if isConfig {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if err := os.Remove(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), 500)
		}
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
