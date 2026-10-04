package mirror

import (
	"context"
	"io"
)

// Target is a mirror destination, either on this machine (FSTarget) or on
// another agent reached over HTTPS (Client).
type Target interface {
	Info(ctx context.Context) error
	Manifest(ctx context.Context, excludes []string, cap Capture, emit func(Entry) error) error
	Offset(ctx context.Context, e Entry) (int64, error)
	Put(ctx context.Context, e Entry, offset int64, body io.Reader) error
	Mkdir(ctx context.Context, e Entry) error
	Symlink(ctx context.Context, e Entry) error
	SetMeta(ctx context.Context, es []Entry) error
	Hash(ctx context.Context, rel string) (string, error)
	Delete(ctx context.Context, rels []string) error
	Cleanup(ctx context.Context) error
}

// FSTarget binds an FS to one destination folder.
type FSTarget struct {
	FS   *FS
	Dest string
}

func (t *FSTarget) Info(ctx context.Context) error { return t.FS.Writable(t.Dest) }
func (t *FSTarget) Manifest(ctx context.Context, x []string, cap Capture, emit func(Entry) error) error {
	return t.FS.Manifest(ctx, t.Dest, x, cap, emit)
}
func (t *FSTarget) Offset(ctx context.Context, e Entry) (int64, error) {
	return t.FS.Offset(t.Dest, e.P, e.S, e.M)
}
func (t *FSTarget) Put(ctx context.Context, e Entry, off int64, r io.Reader) error {
	return t.FS.Put(t.Dest, e, off, r)
}
func (t *FSTarget) Mkdir(ctx context.Context, e Entry) error      { return t.FS.Mkdir(t.Dest, e) }
func (t *FSTarget) Symlink(ctx context.Context, e Entry) error    { return t.FS.Symlink(t.Dest, e) }
func (t *FSTarget) SetMeta(ctx context.Context, es []Entry) error { return t.FS.SetMeta(t.Dest, es) }
func (t *FSTarget) Hash(ctx context.Context, rel string) (string, error) {
	return t.FS.Hash(t.Dest, rel)
}
func (t *FSTarget) Delete(ctx context.Context, rels []string) error { return t.FS.Delete(t.Dest, rels) }
func (t *FSTarget) Cleanup(ctx context.Context) error               { return t.FS.Cleanup(t.Dest) }
