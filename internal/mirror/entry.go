// Package mirror implements a raw, resumable, changed-only directory mirror
// between two agents (or two locations on one agent), with deletion propagation.
package mirror

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Entry describes one file, directory or symlink, with a slash-separated path
// relative to the mirror root.
type Entry struct {
	P string `json:"p,omitempty"`
	T string `json:"t"` // f (file) | d (dir) | l (symlink) | end (manifest terminator)
	S int64  `json:"s,omitempty"`
	M int64  `json:"m,omitempty"` // mtime, unix nanoseconds
	O uint32 `json:"o,omitempty"` // permission bits
	L string `json:"l,omitempty"` // symlink target

	// Compared when ownership / ACL preservation is on.
	U  int    `json:"u,omitempty"`
	G  int    `json:"g,omitempty"`
	UN string `json:"un,omitempty"`
	GN string `json:"gn,omitempty"`
	X  string `json:"x,omitempty"` // fingerprint of ACLs / extended attributes

	// Meta carries the full metadata to apply; it is attached only when sending an item.
	Meta *Meta `json:"meta,omitempty"`
}

func cut(s string) (head, rest string, more bool) {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i], s[i+1:], true
	}
	return s, "", false
}

// Cmp orders paths exactly as filepath.WalkDir visits them (component-wise,
// parents before children). Both sides must agree on this for the streaming
// merge to work; a plain string compare would not ("a/b" sorts after "a.txt").
func Cmp(a, b string) int {
	for {
		ca, ra, ma := cut(a)
		cb, rb, mb := cut(b)
		if c := strings.Compare(ca, cb); c != 0 {
			return c
		}
		switch {
		case !ma && !mb:
			return 0
		case !ma:
			return -1
		case !mb:
			return 1
		}
		a, b = ra, rb
	}
}

// Matcher implements exclusion patterns (path.Match globs):
// "*.tmp" matches a base name anywhere; "a/b*" matches a relative path;
// "/build" is anchored at the mirror root.
type Matcher struct{ pats []string }

func NewMatcher(p []string) *Matcher {
	m := &Matcher{}
	for _, x := range p {
		if x = strings.TrimSpace(x); x != "" {
			m.pats = append(m.pats, x)
		}
	}
	return m
}

func (m *Matcher) Excluded(rel string) bool {
	if m == nil || len(m.pats) == 0 {
		return false
	}
	base := rel
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		base = rel[i+1:]
	}
	for _, p := range m.pats {
		var ok bool
		switch {
		case strings.HasPrefix(p, "/"):
			ok, _ = path.Match(p[1:], rel)
		case strings.Contains(p, "/"):
			ok, _ = path.Match(p, rel)
		default:
			ok, _ = path.Match(p, base)
		}
		if ok {
			return true
		}
	}
	return false
}

// Walk streams the entries under root in WalkDir order. Per-path errors are
// reported to onErr and the walk continues; a non-nil return means the walk
// itself could not complete (e.g. root missing, context cancelled).
func Walk(ctx context.Context, root string, m *Matcher, cap Capture, emit func(Entry) error, onErr func(p string, err error)) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			if p == root {
				return err
			}
			if onErr != nil {
				onErr(p, err)
			}
			return nil
		}
		if p == root {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if m.Excluded(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			if onErr != nil && !os.IsNotExist(ierr) {
				onErr(p, ierr)
			}
			return nil
		}
		e := Entry{P: rel, M: info.ModTime().UnixNano(), O: modeBits(info.Mode())}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			tgt, lerr := os.Readlink(p)
			if lerr != nil {
				if onErr != nil {
					onErr(p, lerr)
				}
				return nil
			}
			e.T, e.L, e.O = "l", tgt, 0
		case info.IsDir():
			e.T = "d"
		case info.Mode().IsRegular():
			e.T, e.S = "f", info.Size()
		default:
			return nil // sockets, devices, fifos: not mirrored
		}
		cap.fill(&e, p, info)
		return emit(e)
	})
}
