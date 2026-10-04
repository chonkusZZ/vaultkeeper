package server

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"vaultkeeper/internal/proto"
	"vaultkeeper/internal/store"
)

func absPath(p string) bool {
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) || (len(p) > 2 && p[1] == ':' && (p[2] == '\\' || p[2] == '/'))
}

func (s *Server) nextRev(a *store.Agent) int64 {
	rev := a.ConfigRev
	if a.Desired != nil && a.Desired.Rev > rev {
		rev = a.Desired.Rev
	}
	return rev + 1
}

// apiAgentConfig pushes new settings to an agent. The agent applies them (and
// restarts itself) the next time it is idle.
func (s *Server) apiAgentConfig(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Roles             []string `json:"roles"`
		Listen            string   `json:"listen"`
		Advertise         string   `json:"advertise"`
		MirrorRoot        string   `json:"mirror_root"`
		ConfirmMirrorMove bool     `json:"confirm_mirror_move"`
	}
	if readJSON(r, &in) != nil {
		httpErr(w, 400, "bad request")
		return
	}
	l := s.lookups()
	a := l.agents[r.PathValue("id")]
	if a == nil {
		httpErr(w, 404, "no such agent")
		return
	}
	roles := map[string]bool{}
	for _, x := range in.Roles {
		if x != "source" && x != "dest" {
			httpErr(w, 400, "unknown role %q", x)
			return
		}
		roles[x] = true
	}
	if len(roles) == 0 {
		httpErr(w, 400, "an agent needs at least one role")
		return
	}
	// A role can't be removed while something relies on it.
	if !roles["dest"] {
		for _, j := range l.jobs {
			if j.DestAgent == a.ID {
				httpErr(w, 409, "can't remove the destination role: backup job %q stores data here", j.Name)
				return
			}
		}
		for _, c := range l.copies {
			if c.DestAgent == a.ID {
				httpErr(w, 409, "can't remove the destination role: copy job %q stores data here", c.Name)
				return
			}
		}
		for _, m := range l.mirrors {
			if m.DestAgent == a.ID {
				httpErr(w, 409, "can't remove the destination role: mirror job %q stores data here", m.Name)
				return
			}
		}
	}
	if !roles["source"] {
		for _, j := range l.jobs {
			if j.SourceAgent == a.ID {
				httpErr(w, 409, "can't remove the source role: backup job %q reads from here", j.Name)
				return
			}
		}
		for _, m := range l.mirrors {
			if m.SourceAgent == a.ID {
				httpErr(w, 409, "can't remove the source role: mirror job %q reads from here", m.Name)
				return
			}
		}
	}
	in.Listen = strings.TrimSpace(in.Listen)
	if in.Listen == "" {
		in.Listen = ":8765"
	}
	if _, port, err := net.SplitHostPort(in.Listen); err != nil {
		httpErr(w, 400, "listen address must look like :8765 or 0.0.0.0:8765")
		return
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		httpErr(w, 400, "listen port must be 1-65535")
		return
	}
	in.Advertise = strings.TrimRight(strings.TrimSpace(in.Advertise), "/")
	if in.Advertise != "" {
		if u, err := url.Parse(in.Advertise); err != nil || u.Scheme != "https" || u.Hostname() == "" {
			httpErr(w, 400, "the advertise address must be an https:// URL that other agents can reach, e.g. https://nas.lan:8765 (leave empty to auto-detect)")
			return
		}
	}
	in.MirrorRoot = strings.TrimSpace(in.MirrorRoot)
	if in.MirrorRoot != "" && !absPath(in.MirrorRoot) {
		httpErr(w, 400, "the mirror folder must be an absolute path on that machine (leave empty for the default)")
		return
	}
	if in.MirrorRoot != "" && in.MirrorRoot != a.MirrorRoot && !in.ConfirmMirrorMove {
		for _, m := range l.mirrors {
			if m.DestAgent == a.ID {
				httpErr(w, 409, "mirror job %q already stores files under %s. Changing the folder does not move them: the next sync would copy everything again into the new location. Move the existing files first (or confirm to proceed anyway).", m.Name, a.MirrorRoot)
				return
			}
		}
	}
	rev := s.nextRev(a)
	cfg := &proto.AgentConfig{Roles: in.Roles, Listen: in.Listen, Advertise: in.Advertise, MirrorRoot: in.MirrorRoot, Rev: rev}
	_ = store.Update(s.st, store.KindAgent, a.ID, func(x *store.Agent) error { x.Desired = cfg; return nil })
	s.wake(a.ID)
	s.st.AddLog(store.LogEntry{Level: proto.LevelInfo, Source: "manager", Message: "configuration change queued for agent " + a.Name + " (applied when it is idle; the agent restarts)"})
	writeJSON(w, 200, map[string]any{"ok": true, "rev": rev})
}

// apiAgentRestart asks an agent to restart itself (same configuration).
func (s *Server) apiAgentRestart(w http.ResponseWriter, r *http.Request) {
	a, err := s.agent(r.PathValue("id"))
	if err != nil {
		httpErr(w, 404, "no such agent")
		return
	}
	if !a.Online {
		httpErr(w, 409, "the agent is offline")
		return
	}
	cfg := &proto.AgentConfig{Roles: a.Roles, Listen: a.Listen, Advertise: a.AdvertiseSetting, MirrorRoot: a.MirrorRoot, Rev: s.nextRev(a)}
	if a.Desired != nil { // keep a pending change rather than reverting it
		cfg = a.Desired
		cfg.Rev = s.nextRev(a)
	}
	_ = store.Update(s.st, store.KindAgent, a.ID, func(x *store.Agent) error { x.Desired = cfg; return nil })
	s.wake(a.ID)
	s.st.AddLog(store.LogEntry{Level: proto.LevelInfo, Source: "manager", Message: "restart requested for agent " + a.Name})
	writeJSON(w, 200, map[string]bool{"ok": true})
}
