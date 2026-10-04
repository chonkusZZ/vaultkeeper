// Package agent implements the Vaultkeeper agent: it polls the manager for
// work, runs restic locally, and (as a destination) serves a restic REST
// endpoint so source agents can send data directly.
package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"vaultkeeper/internal/mirror"
	"vaultkeeper/internal/proto"
	"vaultkeeper/internal/restic"
	"vaultkeeper/internal/restserver"
)

const Version = proto.Version
const maxConcurrent = 2

type Config struct {
	Manager   string
	Token     string // enrolment token (first run only)
	Name      string
	Roles     []string
	DataDir   string
	Listen    string
	Advertise string
	Insecure  bool // skip TLS verification of the manager
	// MirrorRoot is where destination agents keep raw mirrors (default <data-dir>/mirrors).
	MirrorRoot string
	// AllowScripts lets the manager run commands on this machine (wake commands and post-job scripts). Off by default.
	AllowScripts bool
}

type state struct {
	AgentID string `json:"agent_id"`
	Secret  string `json:"secret"`
}

type Agent struct {
	cfg       Config
	st        state
	http      *http.Client
	stream    *http.Client // no overall timeout; for long downloads
	rest      *restserver.Server
	configRev int64  // last remote configuration revision applied
	advertise string // effective advertise URL (configured, or auto-detected)
	mirror    *mirror.FS
	certPEM   string
	running   atomic.Int32
	bin       string
	resticV   string

	mu        sync.Mutex
	repoSizes map[string]int64
}

func (c Config) has(role string) bool {
	for _, r := range c.Roles {
		if r == role {
			return true
		}
	}
	return false
}

func Run(ctx context.Context, cfg Config) error {
	cfg.Manager = strings.TrimRight(cfg.Manager, "/")
	if cfg.Manager == "" {
		return fmt.Errorf("--manager is required")
	}
	if len(cfg.Roles) == 0 {
		cfg.Roles = []string{"source"}
	}
	if cfg.Name == "" {
		cfg.Name, _ = os.Hostname()
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	rev := loadRemoteConfig(&cfg)
	a := &Agent{cfg: cfg, repoSizes: map[string]int64{}, configRev: rev}
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if cfg.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	a.http = &http.Client{Transport: tr, Timeout: 60 * time.Second}
	a.stream = &http.Client{Transport: tr}

	if err := a.ensureRestic(); err != nil {
		log.Printf("WARNING: %v (tasks will fail until restic is available)", err)
	}
	if err := a.register(); err != nil {
		return err
	}
	if cfg.MirrorRoot == "" {
		cfg.MirrorRoot = filepath.Join(cfg.DataDir, "mirrors")
	}
	a.cfg.MirrorRoot = cfg.MirrorRoot
	if cfg.has("dest") {
		if err := os.MkdirAll(cfg.MirrorRoot, 0o755); err != nil {
			return fmt.Errorf("mirror root: %w", err)
		}
		a.mirror = &mirror.FS{Root: cfg.MirrorRoot}
		if err := a.startDataServer(ctx); err != nil {
			return err
		}
		go a.sizeLoop(ctx)
	}
	log.Printf("agent %q (%s) online, roles=%v, manager=%s", cfg.Name, a.st.AgentID, cfg.Roles, cfg.Manager)
	a.pollLoop(ctx)
	return nil
}

func (a *Agent) ensureRestic() error {
	a.bin = restic.Find(a.cfg.DataDir)
	if a.bin == "" {
		log.Printf("restic not found; downloading…")
		p, err := restic.Install(a.cfg.DataDir)
		if err != nil {
			return fmt.Errorf("could not install restic: %w", err)
		}
		a.bin = p
	}
	a.resticV = restic.Version(a.bin)
	log.Printf("using restic %s at %s", a.resticV, a.bin)
	if restic.TooOld(a.resticV) {
		log.Printf("WARNING: restic %s is older than 0.16; some features (restore overwrite policy, lock retry) will fail — install a newer restic or remove it from PATH so the agent downloads one", a.resticV)
	}
	return nil
}

func (a *Agent) register() error {
	p := filepath.Join(a.cfg.DataDir, "agent.json")
	if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &a.st) == nil && a.st.AgentID != "" {
		return nil
	}
	if a.cfg.Token == "" {
		return fmt.Errorf("not enrolled: supply --token (see Settings → Agents in the manager)")
	}
	var resp proto.RegisterResponse
	err := a.call("POST", "/api/agent/register", proto.RegisterRequest{Token: a.cfg.Token, Name: a.cfg.Name, Roles: a.cfg.Roles}, &resp, false)
	if err != nil {
		return fmt.Errorf("registration failed: %w", err)
	}
	a.st = state{AgentID: resp.AgentID, Secret: resp.Secret}
	b, _ := json.Marshal(a.st)
	return os.WriteFile(p, b, 0o600)
}

func (a *Agent) call(method, path string, body, out any, auth bool) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, a.cfg.Manager+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if auth {
		req.Header.Set("X-Agent-ID", a.st.AgentID)
		req.Header.Set("X-Agent-Secret", a.st.Secret)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (a *Agent) advertiseURL() string {
	if a.cfg.Advertise != "" {
		return strings.TrimRight(a.cfg.Advertise, "/")
	}
	_, port, _ := net.SplitHostPort(a.cfg.Listen)
	if port == "" {
		port = "8765"
	}
	host := ""
	if u, err := url.Parse(a.cfg.Manager); err == nil {
		mh := u.Host
		if !strings.Contains(mh, ":") {
			mh += ":80"
		}
		if c, err := net.Dial("udp", mh); err == nil {
			host, _, _ = net.SplitHostPort(c.LocalAddr().String())
			c.Close()
		}
	}
	if host == "" {
		host, _ = os.Hostname()
	}
	return "https://" + net.JoinHostPort(host, port)
}

func (a *Agent) startDataServer(ctx context.Context) error {
	adv := a.advertiseURL()
	u, _ := url.Parse(adv)
	cert, pemStr, err := loadOrCreateCert(a.cfg.DataDir, u.Hostname())
	if err != nil {
		return err
	}
	a.certPEM = pemStr
	a.advertise = adv
	root := filepath.Join(a.cfg.DataDir, "repos")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	a.rest = restserver.New(root)
	mh := &mirror.Handler{FS: a.mirror}
	data := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/mirror/v1/") {
			if !a.rest.Authorized(r) {
				w.Header().Set("WWW-Authenticate", `Basic realm="vaultkeeper"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			mh.ServeHTTP(w, r)
			return
		}
		a.rest.ServeHTTP(w, r)
	})
	srv := &http.Server{
		Addr:      a.cfg.Listen,
		Handler:   data,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	ln, err := net.Listen("tcp", a.cfg.Listen)
	if err != nil {
		return err
	}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	go func() { <-ctx.Done(); srv.Close() }()
	log.Printf("data endpoint listening on %s (advertised as %s)", a.cfg.Listen, adv)
	return nil
}

func (a *Agent) stats() proto.Stats {
	host, _ := os.Hostname()
	s := proto.Stats{
		Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: Version,
		ResticVersion: a.resticV, Roles: a.cfg.Roles, Name: a.cfg.Name,
		Running: int(a.running.Load()), DataDir: a.cfg.DataDir,
		Listen: a.cfg.Listen, ConfigRev: a.configRev, AdvertiseSetting: a.cfg.Advertise, AllowScripts: a.cfg.AllowScripts,
	}
	s.DiskTotal, s.DiskFree = diskUsage(a.cfg.DataDir)
	if a.cfg.has("dest") {
		s.Advertise = a.advertise
		s.CertPEM = a.certPEM
		s.MirrorRoot = a.cfg.MirrorRoot
		s.MirrorTotal, s.MirrorFree = diskUsage(a.cfg.MirrorRoot)
		a.mu.Lock()
		s.RepoSizes = map[string]int64{}
		for k, v := range a.repoSizes {
			s.RepoSizes[k] = v
		}
		a.mu.Unlock()
	}
	return s
}

func (a *Agent) sizeLoop(ctx context.Context) {
	root := filepath.Join(a.cfg.DataDir, "repos")
	for {
		sizes := map[string]int64{}
		es, _ := os.ReadDir(root)
		for _, e := range es {
			if !e.IsDir() {
				continue
			}
			var n int64
			_ = filepath.Walk(filepath.Join(root, e.Name()), func(_ string, fi os.FileInfo, err error) error {
				if err == nil && !fi.IsDir() {
					n += fi.Size()
				}
				return nil
			})
			sizes[e.Name()] = n
		}
		a.mu.Lock()
		a.repoSizes = sizes
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Minute):
		}
	}
}

func (a *Agent) pollLoop(ctx context.Context) {
	backoff := time.Second
	first := true
	for ctx.Err() == nil {
		var resp proto.PollResponse
		err := a.call("POST", "/api/agent/poll", proto.PollRequest{Stats: a.stats(), NoWait: first}, &resp, true)
		first = false
		if err != nil {
			log.Printf("poll: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		if resp.DataToken != "" && a.rest != nil {
			a.rest.SetToken(resp.DataToken)
		}
		if resp.Config != nil {
			a.applyRemoteConfig(*resp.Config)
		}
		if resp.Task != nil {
			a.running.Add(1)
			go func(t proto.Task) {
				defer a.running.Add(-1)
				a.execute(ctx, t)
			}(*resp.Task)
		}
	}
}
