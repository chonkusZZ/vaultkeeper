package mirror

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	ErrOffset = errors.New("resume offset does not match the partial file")
	partTTL   = 7 * 24 * time.Hour
)

const syncThreshold = 1 << 20 // fsync files >= 1 MiB before the atomic rename

// FS performs mirror operations beneath a root directory. All "dest" and
// "rel" arguments are untrusted slash-separated relative paths.
type FS struct {
	Root string
	mu   sync.Mutex // serialises structural changes (mkdir / replace) between workers
}

func validRel(p string, allowEmpty bool) bool {
	if p == "" {
		return allowEmpty
	}
	if len(p) > 4096 || strings.ContainsRune(p, 0) || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return false
	}
	for _, c := range strings.Split(p, "/") {
		if c == "" || c == "." || c == ".." {
			return false
		}
	}
	return true
}

func (f *FS) base(dest string) (string, error) {
	if !validRel(dest, false) || strings.HasPrefix(dest, ".vkmirror") {
		return "", fmt.Errorf("invalid mirror destination %q", dest)
	}
	return filepath.Join(f.Root, filepath.FromSlash(dest)), nil
}

func (f *FS) full(dest, rel string) (string, error) {
	b, err := f.base(dest)
	if err != nil {
		return "", err
	}
	if !validRel(rel, true) {
		return "", fmt.Errorf("invalid path %q", rel)
	}
	if rel == "" {
		return b, nil
	}
	return filepath.Join(b, filepath.FromSlash(rel)), nil
}

// ensureParents makes sure every directory above rel exists and is a *real*
// directory. A symlink or file in the way is replaced, so a write can never
// follow a symlink out of the mirror.
func (f *FS) ensureParents(dest, rel string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := f.base(dest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(b, 0o755); err != nil {
		return err
	}
	dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel)))
	if dir == "." || dir == "" {
		return nil
	}
	cur := b
	for _, c := range strings.Split(dir, "/") {
		cur = filepath.Join(cur, c)
		fi, err := os.Lstat(cur)
		switch {
		case os.IsNotExist(err):
			if err := os.Mkdir(cur, 0o755); err != nil && !os.IsExist(err) {
				return err
			}
		case err != nil:
			return err
		case fi.IsDir():
		default:
			if err := os.RemoveAll(cur); err != nil {
				return err
			}
			if err := os.Mkdir(cur, 0o755); err != nil {
				return err
			}
		}
	}
	return nil
}

// Manifest streams the destination tree in WalkDir order.
func (f *FS) Manifest(ctx context.Context, dest string, excludes []string, cap Capture, emit func(Entry) error) error {
	b, err := f.base(dest)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(b); os.IsNotExist(err) {
		return nil // nothing mirrored yet
	}
	var first error
	err = Walk(ctx, b, NewMatcher(excludes), cap, emit, func(p string, e error) {
		if first == nil {
			first = fmt.Errorf("reading destination %s: %w", p, e)
		}
	})
	if err != nil {
		return err
	}
	return first
}

func (f *FS) partPaths(dest, rel string, size, mtime int64) (dir, file string) {
	dh := sha1.Sum([]byte(dest))
	rh := sha1.Sum([]byte(rel))
	dir = filepath.Join(f.Root, ".vkmirror", hex.EncodeToString(dh[:8]), "parts")
	return dir, filepath.Join(dir, fmt.Sprintf("%s-%d-%d.part", hex.EncodeToString(rh[:10]), size, mtime))
}

// Offset reports how many bytes of a matching partial transfer already exist.
func (f *FS) Offset(dest, rel string, size, mtime int64) (int64, error) {
	if _, err := f.full(dest, rel); err != nil {
		return 0, err
	}
	_, pf := f.partPaths(dest, rel, size, mtime)
	fi, err := os.Stat(pf)
	if err != nil {
		return 0, nil
	}
	if fi.Size() > size {
		_ = os.Remove(pf)
		return 0, nil
	}
	return fi.Size(), nil
}

// Put appends r (the bytes from offset onward) to the partial file and, once
// complete, atomically installs it with the entry's mtime and mode.
func (f *FS) Put(dest string, e Entry, offset int64, r io.Reader) error {
	final, err := f.full(dest, e.P)
	if err != nil || e.P == "" {
		return fmt.Errorf("invalid path %q", e.P)
	}
	if err := f.ensureParents(dest, e.P); err != nil {
		return err
	}
	pdir, pf := f.partPaths(dest, e.P, e.S, e.M)
	if err := os.MkdirAll(pdir, 0o700); err != nil {
		return err
	}
	// Drop partials of an older version of this file.
	prefix := strings.SplitN(filepath.Base(pf), "-", 2)[0] + "-"
	if es, err := os.ReadDir(pdir); err == nil {
		for _, de := range es {
			if strings.HasPrefix(de.Name(), prefix) && filepath.Join(pdir, de.Name()) != pf {
				_ = os.Remove(filepath.Join(pdir, de.Name()))
			}
		}
	}
	var out *os.File
	if offset == 0 {
		out, err = os.OpenFile(pf, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	} else {
		fi, serr := os.Stat(pf)
		if serr != nil || fi.Size() != offset {
			return ErrOffset
		}
		out, err = os.OpenFile(pf, os.O_WRONLY|os.O_APPEND, 0o600)
	}
	if err != nil {
		return err
	}
	n, cerr := io.Copy(out, r)
	if cerr != nil {
		out.Close()
		return cerr // partial kept for resume
	}
	if offset+n != e.S {
		out.Close()
		return fmt.Errorf("received %d of %d bytes", offset+n, e.S)
	}
	if e.S >= syncThreshold {
		if err := out.Sync(); err != nil {
			out.Close()
			return err
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	warn, err := finishItem(pf, e, fileMode(e.O))
	if err != nil {
		return err
	}
	if fi, err := os.Lstat(final); err == nil && fi.IsDir() {
		if err := forceRemoveAll(final); err != nil {
			return err
		}
	}
	if err := os.Rename(pf, final); err != nil {
		if err := moveAcrossDevices(pf, final); err != nil {
			return err
		}
	}
	return warn // metadata we couldn't apply: data is in place, caller reports it
}

// finishItem applies ownership, permissions, extended attributes/ACLs and the
// modification time, in the only order that works: chown clears setuid bits
// and chmod rewrites the ACL mask, so ownership goes first and attributes
// (which restore the ACL) after the mode. Ownership/attribute failures are
// returned as a soft warning; mode/time failures are hard errors.
func finishItem(path string, e Entry, mode os.FileMode) (warn error, err error) {
	var ws []string
	m := e.Meta
	if m != nil && m.Owner {
		u, g := m.U, m.G
		if m.ByName {
			u, g = lookupUID(m.UN, m.U), lookupGID(m.GN, m.G)
		}
		if err := lchown(path, u, g); err != nil {
			ws = append(ws, "ownership: "+simplifyErr(err))
		}
	}
	if err := os.Chmod(path, mode); err != nil {
		return nil, err
	}
	if m != nil && m.ACL {
		if err := applyXattrs(path, m.XA); err != nil {
			ws = append(ws, "ACLs/attributes: "+simplifyErr(err))
		}
	}
	mt := time.Unix(0, e.M)
	if err := os.Chtimes(path, mt, mt); err != nil {
		return nil, err
	}
	return warnJoin(ws), nil
}

func simplifyErr(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	if errors.Is(err, os.ErrPermission) {
		return "operation not permitted (run the destination agent as root)"
	}
	return err.Error()
}

func fileMode(o uint32) os.FileMode {
	m := osMode(o)
	if m.Perm() == 0 {
		m |= 0o644
	}
	return m
}

func moveAcrossDevices(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".vkmirror-tmp-")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	fi, _ := os.Stat(src)
	tmp.Close()
	if fi != nil {
		_ = os.Chmod(tmp.Name(), fi.Mode().Perm())
		_ = os.Chtimes(tmp.Name(), fi.ModTime(), fi.ModTime())
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Remove(src)
}

func (f *FS) Mkdir(dest string, e Entry) error {
	full, err := f.full(dest, e.P)
	if err != nil || e.P == "" {
		return fmt.Errorf("invalid path %q", e.P)
	}
	if err := f.ensureParents(dest, e.P); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	fi, err := os.Lstat(full)
	switch {
	case err == nil && fi.IsDir():
		return nil
	case err == nil:
		if err := os.RemoveAll(full); err != nil {
			return err
		}
	}
	return os.Mkdir(full, 0o755)
}

func (f *FS) Symlink(dest string, e Entry) error {
	full, err := f.full(dest, e.P)
	if err != nil || e.P == "" {
		return fmt.Errorf("invalid path %q", e.P)
	}
	if err := f.ensureParents(dest, e.P); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if fi, err := os.Lstat(full); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if cur, _ := os.Readlink(full); cur == e.L {
				return nil
			}
		}
		if err := forceRemoveAll(full); err != nil {
			return err
		}
	}
	if err := os.Symlink(e.L, full); err != nil {
		return err
	}
	if e.Meta != nil && e.Meta.Owner {
		u, g := e.Meta.U, e.Meta.G
		if e.Meta.ByName {
			u, g = lookupUID(e.Meta.UN, u), lookupGID(e.Meta.GN, g)
		}
		if err := lchown(full, u, g); err != nil {
			return &MetaWarning{Msg: "ownership: " + simplifyErr(err)}
		}
	}
	return nil
}

func wantOwner(m *Meta) (int, int) {
	if m.ByName {
		return lookupUID(m.UN, m.U), lookupGID(m.GN, m.G)
	}
	return m.U, m.G
}

// SetMeta brings existing items in line with the given entries, changing only
// what differs: ownership, permissions, ACLs/attributes, modification time.
func (f *FS) SetMeta(dest string, es []Entry) error {
	var first error
	var ws []string
	for _, e := range es {
		full, err := f.full(dest, e.P)
		if err != nil {
			return err
		}
		fi, err := os.Lstat(full)
		if err != nil {
			continue
		}
		m := e.Meta
		isLink := fi.Mode()&os.ModeSymlink != 0

		ownerChanged := false
		if m != nil && m.Owner {
			wu, wg := wantOwner(m)
			if cu, cg, ok := ownerOf(fi); !ok || cu != wu || cg != wg {
				if err := lchown(full, wu, wg); err != nil {
					ws = append(ws, "ownership: "+simplifyErr(err))
				} else {
					ownerChanged = true
				}
			}
		}
		if isLink {
			continue
		}
		want := fileMode(e.O)
		if e.T == "d" {
			want = osMode(e.O) | 0o700
		}
		modeChanged := false
		if cur, _ := os.Lstat(full); cur != nil && (modeBits(cur.Mode()) != modeBits(want) || ownerChanged) {
			if err := os.Chmod(full, want); err != nil && first == nil {
				first = err
			}
			modeChanged = true
		}
		if m != nil && m.ACL {
			cur, _ := readXattrs(full)
			if modeChanged || xattrDigest(cur) != xattrDigest(m.XA) {
				if err := applyXattrs(full, m.XA); err != nil {
					ws = append(ws, "ACLs/attributes: "+simplifyErr(err))
				}
			}
		}
		if cur, _ := os.Lstat(full); cur != nil && cur.ModTime().UnixNano() != e.M {
			mt := time.Unix(0, e.M)
			if err := os.Chtimes(full, mt, mt); err != nil && first == nil {
				first = err
			}
		}
	}
	if first != nil {
		return first
	}
	return warnJoin(ws)
}

func (f *FS) Hash(dest, rel string) (string, error) {
	full, err := f.full(dest, rel)
	if err != nil {
		return "", err
	}
	return HashFile(full)
}

func HashFile(path string) (string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Delete removes the given paths (directories recursively). Missing paths are fine.
func (f *FS) Delete(dest string, rels []string) error {
	for _, rel := range rels {
		full, err := f.full(dest, rel)
		if err != nil || rel == "" {
			return fmt.Errorf("refusing to delete %q", rel)
		}
		if err := forceRemoveAll(full); err != nil {
			return err
		}
	}
	return nil
}

// forceRemoveAll removes a tree, restoring write permission on read-only
// directories that would otherwise block the removal.
func forceRemoveAll(p string) error {
	err := os.RemoveAll(p)
	if err == nil {
		return nil
	}
	_ = filepath.Walk(p, func(q string, fi os.FileInfo, err error) error {
		if err == nil && fi.IsDir() {
			_ = os.Chmod(q, 0o700)
		}
		return nil
	})
	return os.RemoveAll(p)
}

// Cleanup removes abandoned partial files.
func (f *FS) Cleanup(dest string) error {
	if _, err := f.base(dest); err != nil {
		return err
	}
	dh := sha1.Sum([]byte(dest))
	pdir := filepath.Join(f.Root, ".vkmirror", hex.EncodeToString(dh[:8]), "parts")
	es, err := os.ReadDir(pdir)
	if err != nil {
		return nil
	}
	for _, de := range es {
		if fi, err := de.Info(); err == nil && time.Since(fi.ModTime()) > partTTL {
			_ = os.Remove(filepath.Join(pdir, de.Name()))
		}
	}
	return nil
}

// Writable verifies the destination can be created and written.
func (f *FS) Writable(dest string) error {
	b, err := f.base(dest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(b, 0o755); err != nil {
		return err
	}
	t, err := os.CreateTemp(b, ".vkmirror-probe-")
	if err != nil {
		return err
	}
	t.Close()
	return os.Remove(t.Name())
}

// ValidDest reports whether p is acceptable as a destination folder name
// (relative, slash-separated, no "..").
func ValidDest(p string) bool {
	return validRel(p, false) && !strings.HasPrefix(p, ".vkmirror")
}

// PurgeDest permanently removes a mirror destination folder (and its partial
// transfers). It refuses to touch the mirror root itself, and removes
// now-empty parent folders it leaves behind.
func (f *FS) PurgeDest(dest string) error {
	b, err := f.base(dest)
	if err != nil {
		return err
	}
	if filepath.Clean(b) == filepath.Clean(f.Root) {
		return fmt.Errorf("refusing to remove the mirror root")
	}
	if fi, err := os.Lstat(b); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to remove %s: it is a symlink", b)
	}
	if err := forceRemoveAll(b); err != nil {
		return err
	}
	dh := sha1.Sum([]byte(dest))
	_ = os.RemoveAll(filepath.Join(f.Root, ".vkmirror", hex.EncodeToString(dh[:8])))
	// Tidy empty parents (e.g. "proj" after "proj/alpha"), never the root.
	for p := filepath.Dir(b); filepath.Clean(p) != filepath.Clean(f.Root) && strings.HasPrefix(p, f.Root); p = filepath.Dir(p) {
		if os.Remove(p) != nil {
			break
		}
	}
	return nil
}
