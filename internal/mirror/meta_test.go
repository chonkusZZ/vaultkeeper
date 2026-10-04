package mirror

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func xattrName() string {
	if runtime.GOOS == "linux" {
		return "user.vk.test"
	}
	return "com.vaultkeeper.test"
}

func xattrOf(t *testing.T, p, name string) (string, bool) {
	t.Helper()
	v, err := getXattr(p, name)
	if err != nil {
		return "", false
	}
	return string(v), true
}

// needXattr skips when the temp filesystem can't hold extended attributes.
func needXattr(t *testing.T, dir string) {
	t.Helper()
	if !xattrSupported {
		t.Skip("extended attributes unsupported on this platform")
	}
	p := filepath.Join(dir, ".probe")
	os.WriteFile(p, nil, 0o644)
	if err := setXattr(p, xattrName(), []byte("x")); err != nil {
		t.Skipf("filesystem has no xattr support: %v", err)
	}
}

func TestXattrsPreservedAndMetadataOnlyChangesDetected(t *testing.T) {
	e := newEnv(t)
	needXattr(t, e.src)
	os.Remove(filepath.Join(e.src, ".probe"))
	e.write("doc.txt", "content")
	os.MkdirAll(filepath.Join(e.src, "dir"), 0o755)
	srcFile, srcDir := filepath.Join(e.src, "doc.txt"), filepath.Join(e.src, "dir")
	setXattr(srcFile, xattrName(), []byte("v1"))
	setXattr(srcDir, xattrName(), []byte("dirval"))
	acl := func(o *Options) { o.PreserveACLs = true }

	e.sync(acl)
	if v, ok := xattrOf(t, e.dst("doc.txt"), xattrName()); !ok || v != "v1" {
		t.Fatalf("file attribute not copied: %q %v", v, ok)
	}
	if v, ok := xattrOf(t, e.dst("dir"), xattrName()); !ok || v != "dirval" {
		t.Fatalf("directory attribute not copied: %q %v", v, ok)
	}

	// Change ONLY the attribute: size and mtime are untouched, so only a
	// metadata fingerprint can notice. It must be fixed without re-copying.
	fi, _ := os.Stat(srcFile)
	setXattr(srcFile, xattrName(), []byte("v2-changed"))
	os.Chtimes(srcFile, fi.ModTime(), fi.ModTime())
	r := e.sync(acl)
	if r.FilesCopied != 0 || r.MetaFixed != 1 {
		t.Fatalf("metadata-only change should be a cheap fix, not a re-copy: %+v", r)
	}
	if v, _ := xattrOf(t, e.dst("doc.txt"), xattrName()); v != "v2-changed" {
		t.Fatalf("attribute not updated: %q", v)
	}
	// Removing it on the source removes it on the destination.
	removeXattr(srcFile, xattrName())
	e.sync(acl)
	if _, ok := xattrOf(t, e.dst("doc.txt"), xattrName()); ok {
		t.Fatal("attribute removal not propagated")
	}
	// With the option off, attribute-only differences are ignored (as before).
	setXattr(srcFile, xattrName(), []byte("ignored"))
	os.Chtimes(srcFile, fi.ModTime(), fi.ModTime())
	if r := e.sync(nil); r.MetaFixed != 0 {
		t.Fatalf("attributes must be ignored when not preserving: %+v", r)
	}
}

func TestSpecialModeBitsPreserved(t *testing.T) {
	e := newEnv(t)
	e.write("tool", "bin")
	p := filepath.Join(e.src, "tool")
	if err := os.Chmod(p, 0o755|os.ModeSetuid); err != nil {
		t.Skip("cannot set setuid here")
	}
	e.sync(nil)
	fi, _ := os.Stat(e.dst("tool"))
	if fi.Mode()&os.ModeSetuid == 0 || fi.Mode().Perm() != 0o755 {
		t.Fatalf("setuid bit lost: %v", fi.Mode())
	}
	if r := e.sync(nil); r.MetaFixed != 0 || r.FilesCopied != 0 {
		t.Fatalf("must converge: %+v", r)
	}
}

// A destination agent that isn't root can't chown; the data must still arrive
// and the problem is reported as a warning, not a failure.
func TestOwnerFailureIsAWarningNotAFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can chown")
	}
	e := newEnv(t)
	fs := &FS{Root: e.root}
	ent := Entry{P: "f.txt", T: "f", S: 5, M: 1700000000e9, O: 0o644, Meta: &Meta{Owner: true, U: 0, G: 0}}
	err := fs.Put("m1", ent, 0, strings.NewReader("hello"))
	var mw *MetaWarning
	if err == nil || !asMeta(err, &mw) {
		t.Fatalf("expected a MetaWarning, got %v", err)
	}
	if !strings.Contains(mw.Msg, "root") {
		t.Fatalf("warning should explain the fix: %q", mw.Msg)
	}
	if b, _ := os.ReadFile(e.dst("f.txt")); string(b) != "hello" {
		t.Fatal("data must be in place despite the ownership failure")
	}
}

func asMeta(err error, out **MetaWarning) bool {
	mw, ok := err.(*MetaWarning)
	if ok {
		*out = mw
	}
	return ok
}

func TestEngineCountsMetaWarningsAndStillSucceeds(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs a non-root destination to be unable to chown")
	}
	e := newEnv(t)
	e.write("a.txt", "a")
	// Source entries owned by us; ask for ownership by *name* with a bogus
	// mapping so the destination must try to chown to uid 0, which fails.
	tgt := &chownRoot{FSTarget: e.tgt}
	res, err := Sync(context.Background(), tgt, Options{Src: e.src, PreserveOwner: true, Workers: 2, TmpDir: t.TempDir(), PropagateDeletes: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.MetaWarnings == 0 || res.Errors != 0 || e.read("a.txt") != "a" {
		t.Fatalf("expected warnings, no errors, data present: %+v", res)
	}
}

// chownRoot rewrites the ownership of everything it receives to uid 0.
type chownRoot struct{ *FSTarget }

func (c *chownRoot) Put(ctx context.Context, e Entry, off int64, r io.Reader) error {
	if e.Meta != nil {
		e.Meta.U, e.Meta.G = 0, 0
	}
	return c.FSTarget.Put(ctx, e, off, r)
}

func TestOwnershipAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	e := newEnv(t)
	e.write("a.txt", "a")
	os.MkdirAll(filepath.Join(e.src, "d"), 0o755)
	e.write("d/b.txt", "b")
	os.Chown(filepath.Join(e.src, "a.txt"), 4321, 8765)
	os.Chown(filepath.Join(e.src, "d"), 1111, 2222)
	os.Chown(filepath.Join(e.src, "d/b.txt"), 3333, 4444)
	own := func(o *Options) { o.PreserveOwner = true }
	e.sync(own)
	check := func(rel string, u, g int) {
		t.Helper()
		fi, _ := os.Lstat(e.dst(rel))
		st := fi.Sys().(*syscall.Stat_t)
		if int(st.Uid) != u || int(st.Gid) != g {
			t.Fatalf("%s owner = %d:%d, want %d:%d", rel, st.Uid, st.Gid, u, g)
		}
	}
	check("a.txt", 4321, 8765)
	check("d", 1111, 2222)
	check("d/b.txt", 3333, 4444)
	// Ownership-only change on the source: fixed without re-copying data.
	os.Chown(filepath.Join(e.src, "a.txt"), 5555, 6666)
	r := e.sync(own)
	if r.FilesCopied != 0 || r.MetaFixed != 1 {
		t.Fatalf("ownership-only change must be a metadata fix: %+v", r)
	}
	check("a.txt", 5555, 6666)
	if r := e.sync(own); r.MetaFixed != 0 {
		t.Fatalf("must converge: %+v", r)
	}
	// Symlink ownership.
	os.Symlink("a.txt", filepath.Join(e.src, "ln"))
	syscall.Lchown(filepath.Join(e.src, "ln"), 7, 8)
	e.sync(own)
	check("ln", 7, 8)
}

func TestPOSIXACLsAsRoot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("POSIX ACLs via xattr are Linux-specific")
	}
	if _, err := exec.LookPath("setfacl"); err != nil {
		t.Skip("setfacl not installed")
	}
	e := newEnv(t)
	e.write("secret.txt", "s")
	p := filepath.Join(e.src, "secret.txt")
	if out, err := exec.Command("setfacl", "-m", "u:4242:r--,g:5252:rw-", p).CombinedOutput(); err != nil {
		t.Skipf("filesystem lacks ACL support: %s", out)
	}
	os.MkdirAll(filepath.Join(e.src, "shared"), 0o755)
	exec.Command("setfacl", "-d", "-m", "g:5252:rwx", filepath.Join(e.src, "shared")).Run()
	e.sync(func(o *Options) { o.PreserveACLs = true })
	get := func(path string, args ...string) string {
		out, _ := exec.Command("getfacl", append(args, "--absolute-names", "--omit-header", path)...).Output()
		return string(out)
	}
	if a, b := get(p), get(e.dst("secret.txt")); a != b || !strings.Contains(b, "user:4242:r--") {
		t.Fatalf("ACL differs:\nsource:\n%s\ndest:\n%s", a, b)
	}
	if a, b := get(filepath.Join(e.src, "shared")), get(e.dst("shared")); a != b || !strings.Contains(b, "default:group:5252:rwx") {
		t.Fatalf("default ACL differs:\nsource:\n%s\ndest:\n%s", a, b)
	}
	// ACL-only change is detected and fixed without a re-copy.
	exec.Command("setfacl", "-m", "u:4242:rw-", p).Run()
	r := e.sync(func(o *Options) { o.PreserveACLs = true })
	if r.FilesCopied != 0 || r.MetaFixed < 1 || !strings.Contains(get(e.dst("secret.txt")), "user:4242:rw-") {
		t.Fatalf("ACL-only change not applied: %+v", r)
	}
	_ = strconv.Itoa
}
