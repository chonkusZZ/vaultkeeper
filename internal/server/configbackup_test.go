package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"vaultkeeper/internal/store"
)

func testServer(t *testing.T) *Server {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	t.Setenv("VK_ADMIN_PASSWORD", "adminpass123")
	s, err := New(st)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestConfigBackupRoundTrip(t *testing.T) {
	s := testServer(t)
	s.st.Put(store.KindJob, "j1", store.Job{ID: "j1", Name: "Docs", RepoPassword: "super-secret-repo-key", Paths: []string{"/x"}})
	s.st.Put(store.KindMirrorJob, "m1", store.MirrorJob{ID: "m1", Name: "Mir", DestPath: "a/b", PreserveOwner: true})
	s.st.Put(store.KindAgent, "a1", store.Agent{ID: "a1", Name: "agent", SecretHash: "hash", DataToken: "tok"})

	d, err := s.buildDoc()
	if err != nil {
		t.Fatal(err)
	}
	if d.Settings.SessionSecret != "" {
		t.Fatal("the session secret must never be exported")
	}
	enc, err := encryptDoc(d, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(enc, []byte("super-secret-repo-key")) || bytes.Contains(enc, []byte("Docs")) {
		t.Fatal("backup file leaks plaintext")
	}
	got, err := decryptDoc(enc, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Jobs) != 1 || got.Jobs[0].RepoPassword != "super-secret-repo-key" || len(got.Mirrors) != 1 || !got.Mirrors[0].PreserveOwner {
		t.Fatalf("content lost: %+v", got)
	}
	if len(got.Agents) != 1 || got.Agents[0].SecretHash != "hash" || got.Agents[0].DataToken != "tok" {
		t.Fatalf("agent credentials must survive so agents can reconnect: %+v", got.Agents)
	}
}

func TestConfigBackupRejectsWrongPassphraseAndTampering(t *testing.T) {
	s := testServer(t)
	d, _ := s.buildDoc()
	enc, _ := encryptDoc(d, "correct horse battery")
	if _, err := decryptDoc(enc, "wrong passphrase!!"); err != errBadBackup {
		t.Fatalf("wrong passphrase: %v", err)
	}
	var env envelope
	json.Unmarshal(enc, &env)
	raw, _ := base64.StdEncoding.DecodeString(env.Data)
	raw[len(raw)/2] ^= 0xff
	env.Data = base64.StdEncoding.EncodeToString(raw)
	bad, _ := json.Marshal(env)
	if _, err := decryptDoc(bad, "correct horse battery"); err != errBadBackup {
		t.Fatalf("tampered file must fail authentication: %v", err)
	}
	for _, junk := range []string{"", "not json", `{"format":"other"}`, `{"format":"vaultkeeper-config","v":1,"kdf":"scrypt","n":2,"r":8,"p":1}`} {
		if _, err := decryptDoc([]byte(junk), "x"); err == nil {
			t.Fatalf("accepted junk %q", junk)
		}
	}
}

func TestEachExportUsesFreshSaltAndNonce(t *testing.T) {
	s := testServer(t)
	d, _ := s.buildDoc()
	a, _ := encryptDoc(d, "correct horse battery")
	b, _ := encryptDoc(d, "correct horse battery")
	if bytes.Equal(a, b) {
		t.Fatal("two exports of the same data must differ (random salt/nonce)")
	}
}

func TestAutoBackupWritesAndPrunes(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	s.info = Info{DataDir: t.TempDir()}
	s.saveSettings(func(c *store.Settings) {
		c.AutoBackupDir, c.AutoBackupPass, c.AutoBackupKeep = dir, "a-long-passphrase", 2
	})
	for i := 0; i < 4; i++ {
		if err := s.writeAutoBackup(); err != nil {
			t.Fatal(err)
		}
		// file names carry a second-resolution timestamp
		for j := 0; j < 1; j++ {
			filepathSleep()
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "vaultkeeper-config-*.vkcfg"))
	if len(files) != 2 {
		t.Fatalf("expected the newest 2 backups to be kept, got %d: %v", len(files), files)
	}
	s.saveSettings(func(c *store.Settings) { c.AutoBackupPass = "short" })
	if err := s.writeAutoBackup(); err == nil {
		t.Fatal("a short passphrase must be refused")
	}
}

func filepathSleep() { time.Sleep(1100 * time.Millisecond) }
