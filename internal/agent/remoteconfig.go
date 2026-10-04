package agent

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"vaultkeeper/internal/proto"
)

// remoteConfig is what the manager pushed last; when present it overrides the
// command-line flags for these settings.
type remoteConfig struct {
	Roles      []string `json:"roles"`
	Listen     string   `json:"listen"`
	Advertise  string   `json:"advertise"`
	MirrorRoot string   `json:"mirror_root"`
	Rev        int64    `json:"rev"`
}

func remoteConfigPath(dataDir string) string { return filepath.Join(dataDir, "agent-config.json") }

// loadRemoteConfig applies a previously pushed configuration to cfg.
func loadRemoteConfig(cfg *Config) int64 {
	b, err := os.ReadFile(remoteConfigPath(cfg.DataDir))
	if err != nil {
		return 0
	}
	var rc remoteConfig
	if json.Unmarshal(b, &rc) != nil {
		log.Printf("ignoring unreadable %s", remoteConfigPath(cfg.DataDir))
		return 0
	}
	if len(rc.Roles) > 0 {
		cfg.Roles = rc.Roles
	}
	if rc.Listen != "" {
		cfg.Listen = rc.Listen
	}
	cfg.Advertise = rc.Advertise // "" = auto-detect
	cfg.MirrorRoot = rc.MirrorRoot
	log.Printf("applied remote configuration revision %d from the manager (roles=%v listen=%s)", rc.Rev, cfg.Roles, cfg.Listen)
	return rc.Rev
}

func sameRoles(a, b []string) bool {
	x, y := append([]string{}, a...), append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	return strings.Join(x, ",") == strings.Join(y, ",")
}

// applyRemoteConfig persists a newer configuration and restarts the agent.
// It waits until the agent is idle so running work isn't interrupted.
func (a *Agent) applyRemoteConfig(c proto.AgentConfig) {
	if c.Rev <= a.configRev || a.running.Load() > 0 {
		return
	}
	rc := remoteConfig{Roles: c.Roles, Listen: c.Listen, Advertise: c.Advertise, MirrorRoot: c.MirrorRoot, Rev: c.Rev}
	b, _ := json.MarshalIndent(rc, "", "  ")
	if err := os.WriteFile(remoteConfigPath(a.cfg.DataDir), b, 0o600); err != nil {
		log.Printf("cannot save remote configuration: %v", err)
		return
	}
	log.Printf("remote configuration revision %d received (roles=%v listen=%s advertise=%q mirror-root=%q); restarting to apply",
		c.Rev, c.Roles, c.Listen, c.Advertise, c.MirrorRoot)
	restartSelf()
}
