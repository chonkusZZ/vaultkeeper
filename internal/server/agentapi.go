package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"vaultkeeper/internal/proto"
	"vaultkeeper/internal/store"
)

func hashSecret(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

type ctxAgent struct{ a *store.Agent }

func (s *Server) agentRegister(w http.ResponseWriter, r *http.Request) {
	var in proto.RegisterRequest
	if readJSON(r, &in) != nil {
		httpErr(w, 400, "bad request")
		return
	}
	if subtle.ConstantTimeCompare([]byte(in.Token), []byte(s.settings().EnrollToken)) != 1 {
		httpErr(w, http.StatusForbidden, "invalid enrolment token")
		return
	}
	secret := rid(32)
	a := store.Agent{ID: rid(6), Name: strings.TrimSpace(in.Name), SecretHash: hashSecret(secret), DataToken: rid(24),
		Roles: in.Roles, Created: time.Now().Unix(), LastSeen: time.Now().Unix()}
	if a.Name == "" {
		a.Name = "agent-" + a.ID
	}
	if err := s.st.Put(store.KindAgent, a.ID, a); err != nil {
		httpErr(w, 500, "%v", err)
		return
	}
	s.st.AddLog(store.LogEntry{Level: proto.LevelInfo, Source: "manager", Message: "agent enrolled: " + a.Name})
	writeJSON(w, 200, proto.RegisterResponse{AgentID: a.ID, Secret: secret})
}

func (s *Server) agentAuth(h func(http.ResponseWriter, *http.Request, *store.Agent)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, err := s.agent(r.Header.Get("X-Agent-ID"))
		if err != nil || subtle.ConstantTimeCompare([]byte(hashSecret(r.Header.Get("X-Agent-Secret"))), []byte(a.SecretHash)) != 1 {
			httpErr(w, http.StatusUnauthorized, "bad agent credentials")
			return
		}
		h(w, r, a)
	}
}

// agentPoll is a heartbeat + long-poll for work.
func (s *Server) agentPoll(w http.ResponseWriter, r *http.Request, a *store.Agent) {
	var in proto.PollRequest
	if readJSON(r, &in) != nil {
		httpErr(w, 400, "bad request")
		return
	}
	st := in.Stats
	_ = store.Update(s.st, store.KindAgent, a.ID, func(x *store.Agent) error {
		x.LastSeen = time.Now().Unix()
		x.Roles, x.Hostname, x.OS, x.Arch, x.Version, x.Restic = st.Roles, st.Hostname, st.OS, st.Arch, st.Version, st.ResticVersion
		x.Advertise, x.CertPEM, x.DataDir, x.DiskTotal, x.DiskFree, x.Running = st.Advertise, st.CertPEM, st.DataDir, st.DiskTotal, st.DiskFree, st.Running
		x.MirrorRoot, x.MirrorTotal, x.MirrorFree = st.MirrorRoot, st.MirrorTotal, st.MirrorFree
		x.Listen, x.ConfigRev, x.AdvertiseSetting, x.AllowScripts = st.Listen, st.ConfigRev, st.AdvertiseSetting, st.AllowScripts
		if x.Desired != nil && st.ConfigRev >= x.Desired.Rev {
			x.Desired = nil // the agent confirmed it applied the change
		}
		if st.RepoSizes != nil {
			x.RepoSizes = st.RepoSizes
		}
		a = x
		return nil
	})
	if st.Running == 0 {
		s.failOrphans(a)
	}
	resp := proto.PollResponse{DataToken: a.DataToken}
	pendingConfig := func() *proto.AgentConfig {
		if cur, err := s.agent(a.ID); err == nil && cur.Desired != nil && cur.Desired.Rev > st.ConfigRev {
			return cur.Desired
		}
		return nil
	}
	// Deliver a pending change at once, but only to an idle agent (a busy one
	// can't apply it yet and would otherwise re-poll in a tight loop).
	if st.Running == 0 {
		if resp.Config = pendingConfig(); resp.Config != nil {
			writeJSON(w, 200, resp)
			return
		}
	}
	if st.Running < 2 {
		deadline := time.Now().Add(25 * time.Second)
		if in.NoWait {
			deadline = time.Now()
		}
		for {
			run, err := s.st.ClaimRun(a.ID)
			if err == nil && run != nil {
				task, terr := s.buildTask(run)
				if terr != nil {
					s.finishRun(run, proto.Result{Status: proto.StatusFailed, Message: terr.Error()})
					continue
				}
				if !run.Hidden {
					s.st.AddLog(store.LogEntry{Level: proto.LevelInfo, RunID: run.ID, JobID: run.JobID, Source: "manager", Message: "dispatched to agent " + a.Name})
				}
				resp.Task = task
				break
			}
			if time.Now().After(deadline) || r.Context().Err() != nil {
				break
			}
			select {
			case <-s.wakeCh(a.ID):
			case <-time.After(time.Second):
			case <-r.Context().Done():
			}
			if st.Running == 0 {
				if cfg := pendingConfig(); cfg != nil {
					resp.Config = cfg
					break
				}
			}
		}
	}
	if resp.Config == nil {
		resp.Config = pendingConfig()
	}
	writeJSON(w, 200, resp)
}

func (s *Server) ownedRun(w http.ResponseWriter, r *http.Request, a *store.Agent) *store.Run {
	run, err := s.st.GetRun(r.PathValue("id"))
	if err != nil || run.AgentID != a.ID {
		httpErr(w, 404, "no such task")
		return nil
	}
	return run
}

func (s *Server) agentLog(w http.ResponseWriter, r *http.Request, a *store.Agent) {
	run := s.ownedRun(w, r, a)
	if run == nil {
		return
	}
	var in proto.LogBatch
	if readJSON(r, &in) != nil {
		httpErr(w, 400, "bad request")
		return
	}
	// Wake and hook steps log into the run they belong to, so the run page tells the whole story.
	logRun, logJob := run.ID, run.JobID
	if run.Parent != "" {
		if p, err := s.st.GetRun(run.Parent); err == nil {
			logRun, logJob = p.ID, p.JobID
		}
	}
	entries := make([]store.LogEntry, 0, len(in.Lines))
	for _, l := range in.Lines {
		entries = append(entries, store.LogEntry{TS: l.Time.UnixMilli(), Level: l.Level, RunID: logRun, JobID: logJob, Source: a.Name, Message: l.Message})
	}
	s.st.AddLogs(entries)
	cancel := s.st.TouchRun(run.ID)
	writeJSON(w, 200, map[string]bool{"cancel": cancel})
}

func (s *Server) agentResult(w http.ResponseWriter, r *http.Request, a *store.Agent) {
	run := s.ownedRun(w, r, a)
	if run == nil {
		return
	}
	var res proto.Result
	if readJSON(r, &res) != nil {
		httpErr(w, 400, "bad request")
		return
	}
	if run.Status == proto.StatusRunning {
		s.finishRun(run, res)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// failOrphans fails runs the manager thinks this agent is executing although
// the agent reports it is idle (it was restarted or lost the task), so the
// operator can re-run immediately instead of waiting for the watchdog.
func (s *Server) failOrphans(a *store.Agent) {
	active, _ := s.st.ActiveRuns()
	now := time.Now().Unix()
	for _, r := range active {
		if r.AgentID != a.ID || r.Status != proto.StatusRunning || now-r.Started < 5 {
			continue
		}
		msg := "the agent restarted or lost this task; progress made so far is kept and the next run resumes from there"
		_ = s.st.FinishRun(r.ID, proto.StatusFailed, msg, nil)
		s.st.AddLog(store.LogEntry{Level: proto.LevelError, RunID: r.ID, JobID: r.JobID, Source: "manager", Message: msg})
		r.Status, r.Message = proto.StatusFailed, msg
		if job := s.jobLike(r.JobID); job != nil && !r.Hidden {
			var cp *store.CopyJob
			if r.CopyID != "" {
				cp = &store.CopyJob{}
				_ = s.st.Get(store.KindCopy, r.CopyID, cp)
			}
			s.notifyRun(r, job, cp)
		}
		s.maybePostHook(r)
	}
}
