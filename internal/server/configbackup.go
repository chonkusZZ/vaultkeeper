package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/scrypt"

	"vaultkeeper/internal/proto"
	"vaultkeeper/internal/store"
)

// Info is what main tells the server about how it was started (shown read-only in the UI).
type Info struct {
	Listen  string
	TLS     bool
	DataDir string
	DBPath  string
	Started time.Time
}

func (s *Server) SetInfo(i Info) { s.info = i }

const (
	cfgFormat  = "vaultkeeper-config"
	cfgVersion = 1
	cfgAAD     = "vaultkeeper-config-v1"
)

// configDoc is everything needed to rebuild a manager: settings, agents (with
// their credentials, so existing agents reconnect), and all job definitions.
// Run history and logs are not included.
type configDoc struct {
	Version  int               `json:"version"`
	Created  int64             `json:"created"`
	Software string            `json:"software"`
	Settings store.Settings    `json:"settings"`
	Agents   []store.Agent     `json:"agents"`
	Jobs     []store.Job       `json:"jobs"`
	Copies   []store.CopyJob   `json:"copies"`
	Mirrors  []store.MirrorJob `json:"mirrors"`
}

type envelope struct {
	Format string `json:"format"`
	V      int    `json:"v"`
	KDF    string `json:"kdf"`
	N      int    `json:"n"`
	R      int    `json:"r"`
	P      int    `json:"p"`
	Salt   string `json:"salt"`
	Nonce  string `json:"nonce"`
	Data   string `json:"data"`
}

func (s *Server) buildDoc() (*configDoc, error) {
	d := &configDoc{Version: cfgVersion, Created: time.Now().Unix(), Software: proto.Version, Settings: s.settings()}
	d.Settings.SessionSecret = "" // regenerated on import
	var err error
	if d.Agents, err = store.List[store.Agent](s.st, store.KindAgent); err != nil {
		return nil, err
	}
	for i := range d.Agents {
		d.Agents[i].Desired = nil
	}
	if d.Jobs, err = store.List[store.Job](s.st, store.KindJob); err != nil {
		return nil, err
	}
	if d.Copies, err = store.List[store.CopyJob](s.st, store.KindCopy); err != nil {
		return nil, err
	}
	if d.Mirrors, err = store.List[store.MirrorJob](s.st, store.KindMirrorJob); err != nil {
		return nil, err
	}
	return d, nil
}

func encryptDoc(d *configDoc, pass string) ([]byte, error) {
	plain, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	env := envelope{Format: cfgFormat, V: cfgVersion, KDF: "scrypt", N: 1 << 15, R: 8, P: 1}
	salt, nonce := make([]byte, 16), make([]byte, 12)
	rand.Read(salt)
	rand.Read(nonce)
	key, err := scrypt.Key([]byte(pass), salt, env.N, env.R, env.P, 32)
	if err != nil {
		return nil, err
	}
	blk, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(blk)
	env.Salt, env.Nonce = base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(nonce)
	env.Data = base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, plain, []byte(cfgAAD)))
	return json.MarshalIndent(env, "", " ")
}

var errBadBackup = errors.New("wrong passphrase, or the file is not a valid Vaultkeeper configuration backup")

func decryptDoc(b []byte, pass string) (*configDoc, error) {
	var env envelope
	if json.Unmarshal(b, &env) != nil || env.Format != cfgFormat {
		return nil, errBadBackup
	}
	if env.V != cfgVersion || env.KDF != "scrypt" || env.N < 1<<14 || env.N > 1<<20 || env.R < 1 || env.R > 16 || env.P < 1 || env.P > 4 {
		return nil, fmt.Errorf("unsupported backup format (version %d)", env.V)
	}
	salt, e1 := base64.StdEncoding.DecodeString(env.Salt)
	nonce, e2 := base64.StdEncoding.DecodeString(env.Nonce)
	ct, e3 := base64.StdEncoding.DecodeString(env.Data)
	if e1 != nil || e2 != nil || e3 != nil || len(nonce) != 12 {
		return nil, errBadBackup
	}
	key, err := scrypt.Key([]byte(pass), salt, env.N, env.R, env.P, 32)
	if err != nil {
		return nil, err
	}
	blk, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(blk)
	plain, err := gcm.Open(nil, nonce, ct, []byte(cfgAAD))
	if err != nil {
		return nil, errBadBackup
	}
	var d configDoc
	if err := json.Unmarshal(plain, &d); err != nil || d.Version != cfgVersion || d.Settings.AdminHash == "" {
		return nil, errBadBackup
	}
	return &d, nil
}

func (s *Server) checkAdmin(w http.ResponseWriter, r *http.Request, pw string) bool {
	ip := clientIP(r)
	if s.throttled(ip) {
		httpErr(w, http.StatusTooManyRequests, "too many failed attempts; try again in a few minutes")
		return false
	}
	if bcrypt.CompareHashAndPassword([]byte(s.settings().AdminHash), []byte(pw)) != nil {
		s.noteFail(ip)
		httpErr(w, http.StatusForbidden, "your admin password is incorrect")
		return false
	}
	return true
}

func (s *Server) apiExport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AdminPassword string `json:"admin_password"`
		Passphrase    string `json:"passphrase"`
	}
	if readJSON(r, &in) != nil {
		httpErr(w, 400, "bad request")
		return
	}
	if len(in.Passphrase) < 10 {
		httpErr(w, 400, "choose a passphrase of at least 10 characters; it protects the repository keys inside the file")
		return
	}
	if !s.checkAdmin(w, r, in.AdminPassword) {
		return
	}
	d, err := s.buildDoc()
	if err == nil {
		var out []byte
		if out, err = encryptDoc(d, in.Passphrase); err == nil {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="vaultkeeper-config-%s.vkcfg"`, time.Now().Format("20060102-150405")))
			w.Header().Set("Cache-Control", "no-store")
			w.Write(out)
			s.st.AddLog(store.LogEntry{Level: proto.LevelInfo, Source: "manager", Message: "configuration exported"})
			return
		}
	}
	httpErr(w, 500, "%v", err)
}

func (s *Server) apiImport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AdminPassword string `json:"admin_password"`
		Passphrase    string `json:"passphrase"`
		Data          string `json:"data"`
	}
	if readJSON(r, &in) != nil {
		httpErr(w, 400, "bad request")
		return
	}
	if !s.checkAdmin(w, r, in.AdminPassword) {
		return
	}
	d, err := decryptDoc([]byte(in.Data), in.Passphrase)
	if err != nil {
		httpErr(w, 400, "%v", err)
		return
	}
	d.Settings.SessionSecret = rid(32) // invalidates every existing session, including this one
	items := []store.Item{{Kind: store.KindSettings, ID: "main", V: d.Settings}}
	for _, a := range d.Agents {
		items = append(items, store.Item{Kind: store.KindAgent, ID: a.ID, V: a})
	}
	for _, j := range d.Jobs {
		items = append(items, store.Item{Kind: store.KindJob, ID: j.ID, V: j})
	}
	for _, c := range d.Copies {
		items = append(items, store.Item{Kind: store.KindCopy, ID: c.ID, V: c})
	}
	for _, m := range d.Mirrors {
		items = append(items, store.Item{Kind: store.KindMirrorJob, ID: m.ID, V: m})
	}
	kinds := []string{store.KindSettings, store.KindAgent, store.KindJob, store.KindCopy, store.KindMirrorJob}
	if err := s.st.ReplaceObjects(kinds, items); err != nil {
		httpErr(w, 500, "import failed, nothing was changed: %v", err)
		return
	}
	s.reloadSchedules()
	s.st.AddLog(store.LogEntry{Level: proto.LevelWarning, Source: "manager", Message: fmt.Sprintf("configuration imported (from %s): %d jobs, %d copy jobs, %d mirror jobs, %d agents",
		time.Unix(d.Created, 0).Format("2006-01-02 15:04"), len(d.Jobs), len(d.Copies), len(d.Mirrors), len(d.Agents))})
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]any{"ok": true, "jobs": len(d.Jobs), "copies": len(d.Copies), "mirrors": len(d.Mirrors), "agents": len(d.Agents), "created": d.Created})
}

// ---------- automatic backup ----------

func (s *Server) autoBackupDir(c store.Settings) string {
	if c.AutoBackupDir != "" {
		return c.AutoBackupDir
	}
	return filepath.Join(s.info.DataDir, "config-backups")
}

// writeAutoBackup writes one encrypted export into the configured folder and
// prunes old ones.
func (s *Server) writeAutoBackup() error {
	c := s.settings()
	if len(c.AutoBackupPass) < 10 {
		return errors.New("set an automatic-backup passphrase of at least 10 characters")
	}
	d, err := s.buildDoc()
	if err != nil {
		return err
	}
	b, err := encryptDoc(d, c.AutoBackupPass)
	if err != nil {
		return err
	}
	dir := s.autoBackupDir(c)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := filepath.Join(dir, "vaultkeeper-config-"+time.Now().Format("20060102-150405")+".vkcfg")
	if err := os.WriteFile(name, b, 0o600); err != nil {
		return err
	}
	if keep := c.AutoBackupKeep; keep > 0 {
		es, _ := os.ReadDir(dir)
		var names []string
		for _, e := range es {
			if strings.HasPrefix(e.Name(), "vaultkeeper-config-") && strings.HasSuffix(e.Name(), ".vkcfg") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for len(names) > keep {
			_ = os.Remove(filepath.Join(dir, names[0]))
			names = names[1:]
		}
	}
	return nil
}

func (s *Server) runAutoBackup(trigger string) error {
	err := s.writeAutoBackup()
	msg := "ok"
	if err != nil {
		msg = err.Error()
		s.st.AddLog(store.LogEntry{Level: proto.LevelError, Source: "manager", Message: "automatic configuration backup failed: " + err.Error()})
		s.emit(proto.LevelError, "configuration backup failed", "The manager could not write its automatic configuration backup: "+err.Error())
	} else {
		s.st.AddLog(store.LogEntry{Level: proto.LevelInfo, Source: "manager", Message: "configuration backup written (" + trigger + ")"})
	}
	_ = s.saveSettings(func(c *store.Settings) { c.LastAutoBackup, c.LastAutoBackupMsg = time.Now().Unix(), msg })
	return err
}

func (s *Server) maybeAutoBackup() {
	c := s.settings()
	if c.AutoBackup && time.Since(time.Unix(c.LastAutoBackup, 0)) > 24*time.Hour {
		_ = s.runAutoBackup("daily")
	}
}

func (s *Server) apiAutoBackupNow(w http.ResponseWriter, r *http.Request) {
	if err := s.runAutoBackup("manual"); err != nil {
		httpErr(w, 500, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]string{"dir": s.autoBackupDir(s.settings())})
}

// ---------- system info ----------

func (s *Server) apiSystem(w http.ResponseWriter, r *http.Request) {
	l := s.lookups()
	online := 0
	for _, a := range l.agents {
		if a.Online {
			online++
		}
	}
	c := s.settings()
	writeJSON(w, 200, map[string]any{
		"version": proto.Version, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
		"listen": s.info.Listen, "tls": s.info.TLS, "data_dir": s.info.DataDir, "db_path": s.info.DBPath,
		"started": s.info.Started.Unix(), "agents": len(l.agents), "agents_online": online,
		"jobs": len(l.jobs), "copies": len(l.copies), "mirrors": len(l.mirrors),
		"auto_backup_dir": s.autoBackupDir(c),
	})
}
