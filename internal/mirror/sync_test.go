package mirror

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type env struct {
	t         *testing.T
	src, root string
	tgt       *FSTarget
}

func newEnv(t *testing.T) *env {
	d := t.TempDir()
	e := &env{t: t, src: filepath.Join(d, "src"), root: filepath.Join(d, "mirrors")}
	os.MkdirAll(e.src, 0o755)
	os.MkdirAll(e.root, 0o755)
	e.tgt = &FSTarget{FS: &FS{Root: e.root}, Dest: "m1"}
	return e
}

func (e *env) dst(rel string) string { return filepath.Join(e.root, "m1", filepath.FromSlash(rel)) }
func (e *env) write(rel, content string) {
	p := filepath.Join(e.src, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}
func (e *env) read(rel string) string {
	b, err := os.ReadFile(e.dst(rel))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}
func (e *env) sync(mod func(*Options)) *Result {
	o := Options{Src: e.src, PropagateDeletes: true, MaxDeletePct: 25, Workers: 3, TmpDir: e.t.TempDir()}
	if mod != nil {
		mod(&o)
	}
	res, err := Sync(context.Background(), e.tgt, o)
	if err != nil {
		e.t.Fatalf("sync: %v", err)
	}
	return res
}

func TestCmpMatchesWalkOrder(t *testing.T) {
	d := t.TempDir()
	names := []string{"a", "a.txt", "a/b", "a/b/c", "a-b", "a b", "a/B", "b", "aa/x", "a0"}
	for _, n := range names {
		p := filepath.Join(d, n)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if !strings.HasSuffix(n, "a") && n != "a/b" && n != "aa/x" {
		}
		if _, err := os.Stat(p); err != nil {
			os.WriteFile(p, []byte("x"), 0o644)
		}
	}
	var got []string
	Walk(context.Background(), d, nil, Capture{}, func(e Entry) error { got = append(got, e.P); return nil }, nil)
	for i := 1; i < len(got); i++ {
		if Cmp(got[i-1], got[i]) >= 0 {
			t.Fatalf("Cmp disagrees with walk order: %q !< %q (all: %v)", got[i-1], got[i], got)
		}
	}
}

func TestInitialAndIncremental(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "one")
	e.write("docs/r1.txt", "report1")
	e.write("docs/deep/r2.txt", "report2")
	os.MkdirAll(filepath.Join(e.src, "empty"), 0o755)
	os.Symlink("a.txt", filepath.Join(e.src, "link"))

	r := e.sync(nil)
	if r.New != 3 || e.read("docs/deep/r2.txt") != "report2" || e.read("a.txt") != "one" {
		t.Fatalf("initial copy wrong: %+v", r)
	}
	if fi, err := os.Stat(e.dst("empty")); err != nil || !fi.IsDir() {
		t.Fatal("empty dir not mirrored")
	}
	if l, _ := os.Readlink(e.dst("link")); l != "a.txt" {
		t.Fatalf("symlink: %q", l)
	}
	// mtime preserved
	a, _ := os.Stat(filepath.Join(e.src, "a.txt"))
	b, _ := os.Stat(e.dst("a.txt"))
	if !a.ModTime().Equal(b.ModTime()) {
		t.Fatalf("mtime not preserved: %v vs %v", a.ModTime(), b.ModTime())
	}

	// Nothing changed: nothing is copied.
	r = e.sync(nil)
	if r.FilesCopied != 0 || r.New != 0 || r.Changed != 0 || r.Unchanged != 3 {
		t.Fatalf("second run should be a no-op: %+v", r)
	}

	// Change one, delete one, add one, replace a dir by a file.
	time.Sleep(10 * time.Millisecond)
	e.write("a.txt", "one-changed!")
	os.Chtimes(filepath.Join(e.src, "a.txt"), time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	os.Remove(filepath.Join(e.src, "docs/r1.txt"))
	e.write("new/n.txt", "n")
	os.RemoveAll(filepath.Join(e.src, "docs/deep"))
	e.write("docs/deep", "now a file")
	r = e.sync(nil)
	if e.read("a.txt") != "one-changed!" || e.read("docs/r1.txt") != "<missing>" || e.read("new/n.txt") != "n" || e.read("docs/deep") != "now a file" {
		t.Fatalf("incremental result wrong: %+v", r)
	}
	if r.Changed != 1 || r.Deleted < 1 {
		t.Fatalf("counters: %+v", r)
	}
}

func TestDeletionPropagatesAndCanBeDisabled(t *testing.T) {
	e := newEnv(t)
	e.write("keep.txt", "k")
	e.write("gone.txt", "g")
	e.sync(nil)
	os.Remove(filepath.Join(e.src, "gone.txt"))
	r := e.sync(func(o *Options) { o.PropagateDeletes = false })
	if e.read("gone.txt") != "g" || r.KeptExtra != 1 {
		t.Fatal("deletes disabled must keep extras")
	}
	e.sync(nil)
	if e.read("gone.txt") != "<missing>" {
		t.Fatal("extra file was not deleted")
	}
}

func TestExcludesAreProtected(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a")
	e.write("cache.tmp", "t")
	e.write("build/out.o", "o")
	x := func(o *Options) { o.Excludes = []string{"*.tmp", "/build"} }
	e.sync(x)
	if e.read("cache.tmp") != "<missing>" || e.read("build/out.o") != "<missing>" {
		t.Fatal("excluded items were copied")
	}
	// A pre-existing excluded file on the destination is left alone (not deleted).
	os.WriteFile(e.dst("keep.tmp"), []byte("mine"), 0o644)
	e.sync(x)
	if e.read("keep.tmp") != "mine" {
		t.Fatal("excluded destination file was deleted")
	}
}

func TestEmptySourceGuard(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"a", "b", "c"} {
		e.write(n+".txt", n)
	}
	e.sync(nil)
	for _, n := range []string{"a", "b", "c"} {
		os.Remove(filepath.Join(e.src, n+".txt"))
	}
	r := e.sync(nil)
	if r.DeleteBlocked == "" || e.read("a.txt") != "a" {
		t.Fatalf("empty source must not wipe the mirror: %+v", r)
	}
	r = e.sync(func(o *Options) { o.ForceDelete = true })
	if r.DeleteBlocked != "" || e.read("a.txt") != "<missing>" {
		t.Fatalf("force should override: %+v", r)
	}
}

func TestPercentageGuard(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 100; i++ {
		e.write("f"+string(rune('a'+i%26))+string(rune('a'+i/26))+".txt", "x")
	}
	e.sync(nil)
	// Remove 50 of 100 files from the source.
	n := 0
	es, _ := os.ReadDir(e.src)
	for _, de := range es {
		if n < 50 {
			os.Remove(filepath.Join(e.src, de.Name()))
			n++
		}
	}
	r := e.sync(nil)
	if r.DeleteBlocked == "" || r.Deleted != 0 {
		t.Fatalf("50%% deletion should be blocked at a 25%% limit: %+v", r)
	}
	if got, _ := os.ReadDir(filepath.Join(e.root, "m1")); len(got) != 100 {
		t.Fatalf("destination must be untouched, has %d", len(got))
	}
	r = e.sync(func(o *Options) { o.ForceDelete = true })
	if r.Deleted != 50 {
		t.Fatalf("forced run should delete 50: %+v", r)
	}
}

func TestUnreadableSourceBlocksDeletes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	e := newEnv(t)
	e.write("ok.txt", "ok")
	e.write("locked/secret.txt", "s")
	e.sync(nil)
	os.Chmod(filepath.Join(e.src, "locked"), 0)
	defer os.Chmod(filepath.Join(e.src, "locked"), 0o755)
	r := e.sync(nil)
	if r.SourceErrors == 0 || r.DeleteBlocked == "" || e.read("locked/secret.txt") != "s" {
		t.Fatalf("an unreadable directory must not cause deletions: %+v", r)
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a")
	e.sync(nil)
	e.write("b.txt", "b")
	os.Remove(filepath.Join(e.src, "a.txt"))
	r := e.sync(func(o *Options) { o.DryRun = true; o.MaxDeletePct = 0 })
	if r.New != 1 || r.ToDelete != 1 || e.read("b.txt") != "<missing>" || e.read("a.txt") != "a" {
		t.Fatalf("dry run must only report: %+v", r)
	}
}

func TestChecksumModeCatchesSameSizeSameMtime(t *testing.T) {
	e := newEnv(t)
	e.write("f.bin", "AAAA")
	e.sync(nil)
	fi, _ := os.Stat(e.dst("f.bin"))
	os.WriteFile(filepath.Join(e.src, "f.bin"), []byte("BBBB"), 0o644)
	os.Chtimes(filepath.Join(e.src, "f.bin"), fi.ModTime(), fi.ModTime())
	e.sync(nil)
	if e.read("f.bin") != "AAAA" {
		t.Fatal("mtime mode is expected to miss a same-size same-mtime change")
	}
	r := e.sync(func(o *Options) { o.Compare = "checksum" })
	if e.read("f.bin") != "BBBB" || r.Changed != 1 {
		t.Fatalf("checksum mode must repair it: %+v", r)
	}
}

type brokenReader struct {
	r io.Reader
	n int
}

func (b *brokenReader) Read(p []byte) (int, error) {
	if b.n <= 0 {
		return 0, errors.New("connection reset")
	}
	if len(p) > b.n {
		p = p[:b.n]
	}
	n, err := b.r.Read(p)
	b.n -= n
	return n, err
}

func TestResumeInterruptedLargeFile(t *testing.T) {
	e := newEnv(t)
	big := make([]byte, 24<<20)
	for i := range big {
		big[i] = byte(i * 7)
	}
	p := filepath.Join(e.src, "big.bin")
	os.WriteFile(p, big, 0o644)
	st, _ := os.Stat(p)
	ent := Entry{P: "big.bin", T: "f", S: st.Size(), M: st.ModTime().UnixNano(), O: 0o644}
	// Simulate a transfer that died after 9 MiB.
	f, _ := os.Open(p)
	err := e.tgt.Put(context.Background(), ent, 0, &brokenReader{r: f, n: 9 << 20})
	f.Close()
	if err == nil {
		t.Fatal("expected interrupted transfer")
	}
	if _, err := os.Stat(e.dst("big.bin")); err == nil {
		t.Fatal("a half-written file must never appear at the final path")
	}
	off, _ := e.tgt.Offset(context.Background(), ent)
	if off != 9<<20 {
		t.Fatalf("expected resume offset %d, got %d", 9<<20, off)
	}
	r := e.sync(nil)
	if r.ResumedFiles != 1 || r.ResumedBytes != 9<<20 || r.BytesCopied != int64(len(big))-(9<<20) {
		t.Fatalf("should resume from the partial file: %+v", r)
	}
	if got, _ := os.ReadFile(e.dst("big.bin")); string(got) != string(big) {
		t.Fatal("resumed file differs from source")
	}
}

func TestChangedSourceInvalidatesPartial(t *testing.T) {
	e := newEnv(t)
	p := filepath.Join(e.src, "big.bin")
	os.WriteFile(p, make([]byte, 20<<20), 0o644)
	st, _ := os.Stat(p)
	ent := Entry{P: "big.bin", T: "f", S: st.Size(), M: st.ModTime().UnixNano(), O: 0o644}
	f, _ := os.Open(p)
	e.tgt.Put(context.Background(), ent, 0, &brokenReader{r: f, n: 5 << 20})
	f.Close()
	// The source changes (new content, new size): the old partial must not be reused.
	newData := make([]byte, 21<<20)
	newData[0] = 9
	os.WriteFile(p, newData, 0o644)
	r := e.sync(nil)
	if r.ResumedFiles != 0 {
		t.Fatalf("stale partial reused: %+v", r)
	}
	if got, _ := os.ReadFile(e.dst("big.bin")); len(got) != len(newData) || got[0] != 9 {
		t.Fatal("wrong content after source change")
	}
}

func TestSymlinkInDestinationCannotEscape(t *testing.T) {
	e := newEnv(t)
	outside := t.TempDir()
	e.write("a/file.txt", "inside")
	// Destination has "a" as a symlink pointing outside the mirror.
	os.MkdirAll(filepath.Join(e.root, "m1"), 0o755)
	os.Symlink(outside, e.dst("a"))
	e.sync(nil)
	if es, _ := os.ReadDir(outside); len(es) != 0 {
		t.Fatalf("write escaped the mirror through a symlink: %v", es)
	}
	if fi, err := os.Lstat(e.dst("a")); err != nil || !fi.IsDir() || e.read("a/file.txt") != "inside" {
		t.Fatal("symlink should have been replaced by a real directory")
	}
}

func TestInvalidPathsRejected(t *testing.T) {
	fs := &FS{Root: t.TempDir()}
	for _, bad := range []string{"../x", "a/../../x", "/abs", "", ".vkmirror/x", "a//b"} {
		if _, err := fs.base(bad); err == nil {
			t.Errorf("dest %q accepted", bad)
		}
	}
	for _, bad := range []string{"../x", "a/../b", "/etc/passwd", "a//b", "./a"} {
		if _, err := fs.full("m1", bad); err == nil {
			t.Errorf("rel %q accepted", bad)
		}
	}
	if err := fs.Delete("m1", []string{""}); err == nil {
		t.Error("deleting the mirror root must be refused")
	}
}

// ---- protocol over TLS ----

func TestOverHTTPS(t *testing.T) {
	e := newEnv(t)
	e.write("x/y/z.txt", "zzz")
	e.write("top.txt", "top")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pw, ok := r.BasicAuth(); !ok || pw != "tok" {
			http.Error(w, "no", 401)
			return
		}
		(&Handler{FS: &FS{Root: e.root}}).ServeHTTP(w, r)
	}))
	defer srv.Close()
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	c, err := NewClient(srv.URL, string(pemBytes), "tok", "m1", 4)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Sync(context.Background(), c, Options{Src: e.src, PropagateDeletes: true, MaxDeletePct: 25, TmpDir: t.TempDir()})
	if err != nil || res.New != 2 {
		t.Fatalf("remote sync: %v %+v", err, res)
	}
	if e.read("x/y/z.txt") != "zzz" {
		t.Fatal("content did not arrive")
	}
	os.Remove(filepath.Join(e.src, "top.txt"))
	res, err = Sync(context.Background(), c, Options{Src: e.src, PropagateDeletes: true, TmpDir: t.TempDir()})
	if err != nil || res.Deleted != 1 || e.read("top.txt") != "<missing>" {
		t.Fatalf("remote delete: %v %+v", err, res)
	}
	// Wrong token is refused.
	bad, _ := NewClient(srv.URL, string(pemBytes), "wrong", "m1", 1)
	if _, err := Sync(context.Background(), bad, Options{Src: e.src, TmpDir: t.TempDir()}); err == nil {
		t.Fatal("wrong token accepted")
	}
	// A different certificate than the one the server presents must be refused (pinning).
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "other"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	pin, _ := NewClient(srv.URL, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), "tok", "m1", 1)
	if _, err := Sync(context.Background(), pin, Options{Src: e.src, TmpDir: t.TempDir()}); err == nil {
		t.Fatal("certificate pinning not enforced")
	}
}

// treeDigest lists every path with size for a quick whole-tree comparison.
func treeDigest(t *testing.T, root string) map[string]string {
	out := map[string]string{}
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			out[rel] = "dir"
		} else if d.Type()&os.ModeSymlink != 0 {
			l, _ := os.Readlink(p)
			out[rel] = "link:" + l
		} else {
			b, _ := os.ReadFile(p)
			out[rel] = string(b)
		}
		return nil
	})
	return out
}

func TestManyFilesConcurrent(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 3000; i++ {
		e.write(filepath.Join("d"+string(rune('a'+i%7)), "s"+string(rune('a'+i%11)), "f"+itoa(i)+".txt"), "content "+itoa(i))
	}
	e.sync(func(o *Options) { o.Workers = 16 })
	if !equalTrees(treeDigest(t, e.src), treeDigest(t, e.dst(""))) {
		t.Fatal("trees differ after initial sync")
	}
	// Change, delete and add a bunch, then sync again.
	for i := 0; i < 3000; i += 3 {
		p := filepath.Join(e.src, "d"+string(rune('a'+i%7)), "s"+string(rune('a'+i%11)), "f"+itoa(i)+".txt")
		os.Remove(p)
	}
	for i := 1; i < 3000; i += 7 {
		p := filepath.Join(e.src, "d"+string(rune('a'+i%7)), "s"+string(rune('a'+i%11)), "f"+itoa(i)+".txt")
		if _, err := os.Stat(p); err == nil {
			os.WriteFile(p, []byte("changed "+itoa(i)), 0o644)
			os.Chtimes(p, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
		}
	}
	e.write("brandnew/x/y/z.txt", "n")
	r := e.sync(func(o *Options) { o.Workers = 16; o.MaxDeletePct = 0 })
	if r.Errors != 0 || !equalTrees(treeDigest(t, e.src), treeDigest(t, e.dst(""))) {
		t.Fatalf("trees differ after incremental sync: %+v", r)
	}
	if r2 := e.sync(nil); r2.FilesCopied != 0 || r2.Deleted != 0 {
		t.Fatalf("converged mirror must be a no-op: %+v", r2)
	}
}

func itoa(i int) string { return strconvItoa(i) }
func equalTrees(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
func strconvItoa(i int) string { return strconv.Itoa(i) }
