package server

import (
	"fmt"
	"strings"
	"time"

	"vaultkeeper/internal/netutil"
	"vaultkeeper/internal/proto"
	"vaultkeeper/internal/store"
)

// kinds after which the post-completion script may run (browsing alone does not trigger it).
func postEligible(kind string) bool {
	switch kind {
	case proto.KindBackup, proto.KindCopy, proto.KindMirror, proto.KindTest, proto.KindPrune, proto.KindRestore, proto.KindForget:
		return true
	}
	return false
}

// hooksForRun finds the hooks that govern a run: the mirror's, the copy job's
// (for copy runs and for browsing a copy repository), or the backup job's.
func (s *Server) hooksForRun(r *store.Run) *store.Hooks {
	switch {
	case r.Kind == proto.KindMirror:
		var m store.MirrorJob
		if s.st.Get(store.KindMirrorJob, r.JobID, &m) == nil {
			return m.Hooks
		}
	case r.CopyID != "":
		var cp store.CopyJob
		if s.st.Get(store.KindCopy, r.CopyID, &cp) == nil {
			return cp.Hooks
		}
	default:
		var j store.Job
		if s.st.Get(store.KindJob, r.JobID, &j) == nil {
			return j.Hooks
		}
	}
	return nil
}

// runName is the display name of the job a run belongs to.
func (s *Server) runName(r *store.Run) string {
	if r.CopyID != "" {
		var cp store.CopyJob
		if s.st.Get(store.KindCopy, r.CopyID, &cp) == nil {
			return cp.Name
		}
	}
	if j := s.jobLike(r.JobID); j != nil {
		return j.Name
	}
	return r.JobID
}

// targetKey identifies the physical target (NAS) so jobs sharing one can be
// coordinated: an explicit label, else MAC, else host, else the watched path.
func targetKey(h *store.Hooks) string {
	if h == nil {
		return ""
	}
	if t := strings.ToLower(strings.TrimSpace(h.Target)); t != "" {
		return "name:" + t
	}
	if w := h.Wake; w != nil && w.Enabled {
		switch {
		case w.MAC != "":
			if hw, err := netutil.ParseMAC(w.MAC); err == nil {
				return "mac:" + hw.String()
			}
		case w.Host != "":
			return "host:" + strings.ToLower(w.Host)
		case w.Path != "":
			return "path:" + w.Agent + ":" + w.Path
		}
	}
	return ""
}

// targetBusy reports whether any other queued/running run uses the same target.
func (s *Server) targetBusy(key string, exclude string) int {
	if key == "" {
		return 0
	}
	n := 0
	active, _ := s.st.ActiveRuns()
	for _, r := range active {
		if r.ID == exclude || r.Kind == proto.KindWake || r.Kind == proto.KindHook {
			continue
		}
		if targetKey(s.hooksForRun(r)) == key {
			n++
		}
	}
	return n
}

// wakeEnabled reports whether this run must wait for the target to be woken first.
func wakeEnabled(h *store.Hooks, kind string) bool {
	return h != nil && h.Wake != nil && h.Wake.Enabled && kind != proto.KindWake && kind != proto.KindHook
}

func (s *Server) notifyParent(p *store.Run) {
	if p.Hidden {
		return
	}
	if job := s.jobLike(p.JobID); job != nil {
		var cp *store.CopyJob
		if p.CopyID != "" {
			cp = &store.CopyJob{}
			if s.st.Get(store.KindCopy, p.CopyID, cp) != nil {
				cp = nil
			}
		}
		s.notifyRun(p, job, cp)
	}
}

// finishWake releases the run that was waiting for the target, or fails it.
func (s *Server) finishWake(run *store.Run, status, message string) {
	parent, err := s.st.GetRun(run.Parent)
	if err != nil || parent.Status != proto.StatusQueued {
		return
	}
	if status == proto.StatusSuccess {
		s.st.ReleaseWait(parent.ID)
		s.wake(parent.AgentID)
		return
	}
	msg := "could not wake the backup target: " + message
	_ = s.st.FinishRun(parent.ID, proto.StatusFailed, msg, nil)
	s.st.AddLog(store.LogEntry{Level: proto.LevelError, RunID: parent.ID, JobID: parent.JobID, Source: "manager", Message: msg})
	parent.Status, parent.Message = proto.StatusFailed, msg
	s.notifyParent(parent)
}

// finishHook records the outcome of a post-completion script on its run.
func (s *Server) finishHook(run *store.Run, status, message string) {
	parent, err := s.st.GetRun(run.Parent)
	if err != nil {
		return
	}
	name := s.runName(parent)
	if status == proto.StatusSuccess {
		s.st.AddLog(store.LogEntry{Level: proto.LevelInfo, RunID: parent.ID, JobID: parent.JobID, Source: "manager", Message: "post-completion script finished"})
		return
	}
	msg := "post-completion script failed: " + message
	s.st.AddLog(store.LogEntry{Level: proto.LevelError, RunID: parent.ID, JobID: parent.JobID, Source: "manager", Message: msg})
	s.emit(proto.LevelError, "post-completion script failed: "+name, fmt.Sprintf("The script that runs after %q (%s) failed.\n\n%s\n\nThe backup itself is unaffected.", name, parent.Kind, message))
}

// maybePostHook queues the post-completion script once a run has finished,
// unless another run still uses the same target.
func (s *Server) maybePostHook(run *store.Run) {
	if run.Started == 0 || !postEligible(run.Kind) {
		return
	}
	h := s.hooksForRun(run)
	if h == nil || h.Post == nil || !h.Post.Enabled || strings.TrimSpace(h.Post.Script) == "" {
		return
	}
	if h.Post.On != "always" && run.Status == proto.StatusFailed {
		return
	}
	logp := func(level, f string, a ...any) {
		s.st.AddLog(store.LogEntry{Level: level, RunID: run.ID, JobID: run.JobID, Source: "manager", Message: fmt.Sprintf(f, a...)})
	}
	if n := s.targetBusy(targetKey(h), run.ID); n > 0 {
		logp(proto.LevelInfo, "post-completion script not run: the target is still in use by %d other job run(s); it runs when the last one finishes", n)
		return
	}
	a, err := s.agent(h.Post.Agent)
	if err != nil || !a.Online {
		msg := "post-completion script skipped: its agent is offline"
		logp(proto.LevelError, "%s", msg)
		s.emit(proto.LevelError, "post-completion script skipped: "+s.runName(run), msg+".")
		return
	}
	hook := &store.Run{ID: rid(8), Kind: proto.KindHook, AgentID: a.ID, Status: proto.StatusQueued, Trigger: "post-completion script",
		Created: time.Now().Unix(), Hidden: true, Parent: run.ID}
	if s.st.InsertRun(hook) == nil {
		logp(proto.LevelInfo, "running post-completion script on agent %s", a.Name)
		s.wake(a.ID)
	}
}

// buildHookTask builds wake and hook tasks from the parent run's configuration.
func (s *Server) buildHookTask(run *store.Run) (*proto.Task, error) {
	parent, err := s.st.GetRun(run.Parent)
	if err != nil {
		return nil, fmt.Errorf("the run this step belongs to no longer exists")
	}
	h := s.hooksForRun(parent)
	t := &proto.Task{RunID: run.ID, Kind: run.Kind, JobID: parent.JobID}
	switch run.Kind {
	case proto.KindWake:
		if h == nil || h.Wake == nil {
			return nil, fmt.Errorf("the wake configuration was removed")
		}
		spec := h.Wake.WakeSpec
		t.Wake = &spec
	case proto.KindHook:
		if h == nil || h.Post == nil {
			return nil, fmt.Errorf("the script was removed")
		}
		name := s.runName(parent)
		t.Hook = &proto.HookSpec{Script: h.Post.Script, TimeoutSec: h.Post.TimeoutSec, Env: map[string]string{
			"VK_JOB_NAME": name, "VK_JOB_ID": parent.JobID, "VK_RUN_ID": parent.ID, "VK_RUN_KIND": parent.Kind,
			"VK_STATUS": parent.Status, "VK_MESSAGE": parent.Message, "VK_TARGET": targetKey(h),
			"VK_STARTED": fmt.Sprint(parent.Started), "VK_FINISHED": fmt.Sprint(parent.Finished),
		}}
	}
	return t, nil
}

// validateHooks checks and normalises a job's hooks. defAgent fills in an empty agent.
func (s *Server) validateHooks(l *lookups, h *store.Hooks, defAgent string) (*store.Hooks, error) {
	if h == nil {
		return nil, nil
	}
	h.Target = strings.TrimSpace(h.Target)
	scripts := map[string]string{} // agent id -> what needs scripts
	agent := func(id string) (*store.Agent, error) {
		if id == "" {
			id = defAgent
		}
		a := l.agents[id]
		if a == nil {
			return nil, fmt.Errorf("choose an agent for the wake / script step")
		}
		return a, nil
	}
	if w := h.Wake; w != nil && w.Enabled {
		a, err := agent(w.Agent)
		if err != nil {
			return nil, err
		}
		w.Agent = a.ID
		switch w.Method {
		case "wol":
			hw, err := netutil.ParseMAC(w.MAC)
			if err != nil {
				return nil, err
			}
			w.MAC = hw.String()
			w.Broadcast = strings.TrimSpace(w.Broadcast)
			if !netutil.ValidBroadcast(w.Broadcast) {
				return nil, fmt.Errorf("the broadcast address must be an IPv4 address, optionally with :port")
			}
		case "command":
			if strings.TrimSpace(w.Command) == "" || len(w.Command) > 4096 {
				return nil, fmt.Errorf("enter the wake command (up to 4096 characters)")
			}
			scripts[a.ID] = "the custom wake command"
		default:
			return nil, fmt.Errorf("choose how to wake the target")
		}
		switch w.Ready {
		case "browse":
			if !absPath(w.Path) {
				return nil, fmt.Errorf("enter the absolute folder path (on the %s agent) that becomes browseable when the target is up", a.Name)
			}
			if strings.ContainsAny(w.Marker, "/\\") {
				return nil, fmt.Errorf("the marker must be a file name inside that folder")
			}
		case "ping":
			if !netutil.ValidHost(w.Host) {
				return nil, fmt.Errorf("enter the host name or IP address to ping")
			}
		case "tcp":
			if !netutil.ValidHost(w.Host) || w.Port < 1 || w.Port > 65535 {
				return nil, fmt.Errorf("enter the host and TCP port to check (e.g. 445 for SMB, 2049 for NFS)")
			}
		case "command":
			if strings.TrimSpace(w.ReadyCommand) == "" || len(w.ReadyCommand) > 4096 {
				return nil, fmt.Errorf("enter the readiness command (exit status 0 = ready)")
			}
			scripts[a.ID] = "the custom readiness command"
		default:
			return nil, fmt.Errorf("choose how to tell when the target is ready")
		}
		if w.SettleSec < 0 || w.SettleSec > 3600 {
			return nil, fmt.Errorf("the extra wait must be 0-3600 seconds")
		}
		if w.TimeoutMin == 0 {
			w.TimeoutMin = 10
		}
		if w.TimeoutMin < 1 || w.TimeoutMin > 240 {
			return nil, fmt.Errorf("give up after 1-240 minutes")
		}
	}
	if p := h.Post; p != nil && p.Enabled {
		a, err := agent(p.Agent)
		if err != nil {
			return nil, err
		}
		p.Agent = a.ID
		if strings.TrimSpace(p.Script) == "" || len(p.Script) > 8192 {
			return nil, fmt.Errorf("enter the script to run (up to 8192 characters)")
		}
		if p.On == "" {
			p.On = "success"
		}
		if p.On != "success" && p.On != "always" {
			return nil, fmt.Errorf("run the script after 'success' or 'always'")
		}
		if p.TimeoutSec == 0 {
			p.TimeoutSec = 120
		}
		if p.TimeoutSec < 5 || p.TimeoutSec > 3600 {
			return nil, fmt.Errorf("script timeout must be 5-3600 seconds")
		}
		if _, ok := scripts[a.ID]; !ok {
			scripts[a.ID] = "the post-completion script"
		}
	}
	for id, what := range scripts {
		if a := l.agents[id]; a != nil && !a.AllowScripts {
			return nil, fmt.Errorf("%s needs scripts enabled on agent %q: restart that agent with --allow-scripts (a security opt-in that can't be switched on remotely)", what, a.Name)
		}
	}
	return h, nil
}
