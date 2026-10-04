package server

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"vaultkeeper/internal/notify"
	"vaultkeeper/internal/proto"
	"vaultkeeper/internal/store"
)

const onlineWindow = 90 * time.Second

func online(a *store.Agent) bool {
	return time.Since(time.Unix(a.LastSeen, 0)) < onlineWindow
}

func (s *Server) agent(id string) (*store.Agent, error) {
	var a store.Agent
	if err := s.st.Get(store.KindAgent, id, &a); err != nil {
		return nil, err
	}
	a.Online = online(&a)
	return &a, nil
}

// Start launches the scheduler and background watchdog.
func (s *Server) Start(ctx context.Context) {
	s.cron = cron.New()
	s.cron.Start()
	s.reloadSchedules()
	go s.watchdog(ctx)
	go func() { <-ctx.Done(); s.cron.Stop() }()
}

// reloadSchedules rebuilds all cron entries from the stored jobs.
func (s *Server) reloadSchedules() {
	if s.cron == nil {
		return
	}
	s.cronMu.Lock()
	defer s.cronMu.Unlock()
	for k, id := range s.entries {
		s.cron.Remove(id)
		delete(s.entries, k)
	}
	add := func(key, spec string, fn func()) {
		if spec == "" {
			return
		}
		id, err := s.cron.AddFunc(spec, fn)
		if err != nil {
			log.Printf("bad schedule %q for %s: %v", spec, key, err)
			return
		}
		s.entries[key] = id
	}
	jobs, _ := store.List[store.Job](s.st, store.KindJob)
	for _, j := range jobs {
		j := j
		if !j.Enabled {
			continue
		}
		add("backup:"+j.ID, j.Schedule, func() { s.scheduled(j.ID, "", proto.KindBackup) })
		if j.TestMode != "off" {
			add("test:"+j.ID, j.TestSchedule, func() { s.scheduled(j.ID, "", proto.KindTest) })
		}
	}
	mirrors, _ := store.List[store.MirrorJob](s.st, store.KindMirrorJob)
	for _, m := range mirrors {
		m := m
		if m.Enabled {
			add("mirror:"+m.ID, m.Schedule, func() { s.scheduledMirror(m.ID) })
		}
	}
	copies, _ := store.List[store.CopyJob](s.st, store.KindCopy)
	for _, c := range copies {
		c := c
		if c.Enabled {
			add("copy:"+c.ID, c.Schedule, func() { s.scheduled(c.JobID, c.ID, proto.KindCopy) })
		}
	}
}

// nextRun returns the next scheduled time (unix) or 0.
func (s *Server) nextRun(key string) int64 {
	s.cronMu.Lock()
	defer s.cronMu.Unlock()
	if id, ok := s.entries[key]; ok && s.cron != nil {
		return s.cron.Entry(id).Next.Unix()
	}
	return 0
}

func (s *Server) scheduledMirror(id string) {
	var m store.MirrorJob
	if s.st.Get(store.KindMirrorJob, id, &m) != nil {
		return
	}
	if _, err := s.enqueueMirror(&m, "schedule", proto.Explore{}); err != nil {
		s.st.AddLog(store.LogEntry{Level: proto.LevelWarning, JobID: id, Source: "manager", Message: fmt.Sprintf("scheduled mirror skipped: %v", err)})
	}
}

// enqueueMirror queues a mirror run (optionally a dry-run preview).
func (s *Server) enqueueMirror(m *store.MirrorJob, trigger string, p proto.Explore) (*store.Run, error) {
	o := enqueueOpts{kind: proto.KindMirror, job: &store.Job{ID: m.ID, Name: m.Name}, trigger: trigger, exec: m.SourceAgent, params: p}
	if m.DestAgent != m.SourceAgent {
		o.need = []string{m.DestAgent}
	}
	return s.enqueueWith(o)
}

// jobLike returns the backup job with this id, or a name-only stand-in for a
// mirror job, so notifications and history can treat both alike.
func (s *Server) jobLike(id string) *store.Job {
	var j store.Job
	if s.st.Get(store.KindJob, id, &j) == nil {
		return &j
	}
	var m store.MirrorJob
	if s.st.Get(store.KindMirrorJob, id, &m) == nil {
		return &store.Job{ID: m.ID, Name: m.Name}
	}
	return nil
}

func (s *Server) scheduled(jobID, copyID, kind string) {
	var job store.Job
	if err := s.st.Get(store.KindJob, jobID, &job); err != nil {
		return
	}
	var cp *store.CopyJob
	if copyID != "" {
		cp = &store.CopyJob{}
		if err := s.st.Get(store.KindCopy, copyID, cp); err != nil {
			return
		}
	}
	if _, err := s.enqueue(kind, &job, cp, "schedule", false); err != nil {
		s.st.AddLog(store.LogEntry{Level: proto.LevelWarning, JobID: jobID, Source: "manager", Message: fmt.Sprintf("scheduled %s skipped: %v", kind, err)})
	}
}

// enqueue creates a run for the standard job tasks.
func (s *Server) enqueue(kind string, job *store.Job, cp *store.CopyJob, trigger string, hidden bool) (*store.Run, error) {
	o := enqueueOpts{kind: kind, job: job, cp: cp, trigger: trigger, hidden: hidden}
	switch kind {
	case proto.KindBackup:
		o.exec, o.need = job.SourceAgent, []string{job.DestAgent}
	case proto.KindTest:
		o.exec = job.DestAgent
	case proto.KindSnapshots:
		o.exec = job.DestAgent
	case proto.KindCopy:
		o.exec, o.need = job.DestAgent, []string{cp.DestAgent}
	case proto.KindPrune:
		o.exec, _, _ = repoTarget(job, cp) // runs where the repository lives
	default:
		return nil, fmt.Errorf("unknown kind")
	}
	return s.enqueueWith(o)
}

type enqueueOpts struct {
	id      string // optional preset run id
	kind    string
	job     *store.Job
	cp      *store.CopyJob // copy job being run (copy) or repo being explored (explorer kinds)
	trigger string
	hidden  bool
	exec    string   // agent that executes the task
	need    []string // other agents that must be online
	params  proto.Explore
}

// Kinds that only read a repository and may overlap with each other.
func readOnlyKind(k string) bool {
	switch k {
	case proto.KindSnapshots, proto.KindLs, proto.KindFind, proto.KindDump, proto.KindRestore:
		return true
	}
	return false
}

// repoTarget resolves which repository (destination agent, name, key) a run addresses.
func repoTarget(job *store.Job, cp *store.CopyJob) (agentID, name, pass string) {
	if cp != nil {
		return cp.DestAgent, cp.RepoName, cp.RepoPassword
	}
	return job.DestAgent, job.RepoName, job.RepoPassword
}

// enqueueWith creates a run. If a required agent is offline the run is created
// already failed (so it shows up and alerts the operator).
func (s *Server) enqueueWith(o enqueueOpts) (*store.Run, error) {
	job, cp, kind := o.job, o.cp, o.kind
	copyID := ""
	if cp != nil {
		copyID = cp.ID
	}
	if !readOnlyKind(kind) && kind != proto.KindForget && kind != proto.KindPurge {
		active, _ := s.st.ActiveRuns()
		for _, r := range active {
			if r.JobID == job.ID && r.Kind == kind && r.CopyID == copyID {
				return nil, fmt.Errorf("a %s run is already %s", kind, r.Status)
			}
		}
	}
	if o.id == "" {
		o.id = rid(8)
	}
	run := &store.Run{ID: o.id, Kind: kind, JobID: job.ID, CopyID: copyID, AgentID: o.exec,
		Status: proto.StatusQueued, Trigger: o.trigger, Created: time.Now().Unix(), Hidden: o.hidden, Params: o.params}
	if err := s.st.InsertRun(run); err != nil {
		return nil, err
	}
	if !o.hidden {
		s.st.AddLog(store.LogEntry{Level: proto.LevelInfo, RunID: run.ID, JobID: job.ID, Source: "manager", Message: fmt.Sprintf("%s queued (%s)", kind, o.trigger)})
	}

	var problem string
	for i, id := range append([]string{o.exec}, o.need...) {
		a, err := s.agent(id)
		if err != nil {
			problem = "agent no longer exists"
		} else if !a.Online {
			role := "executing"
			if i > 0 {
				role = "required"
			}
			problem = fmt.Sprintf("%s agent %q is offline", role, a.Name)
		}
		if problem != "" {
			break
		}
	}
	if problem != "" {
		run.Status = proto.StatusFailed
		_ = s.st.FinishRun(run.ID, proto.StatusFailed, problem, nil)
		s.st.AddLog(store.LogEntry{Level: proto.LevelError, RunID: run.ID, JobID: job.ID, Source: "manager", Message: problem})
		run.Message = problem
		if !o.hidden {
			s.notifyRun(run, job, cp)
		}
		return run, nil
	}
	s.wake(o.exec)
	return run, nil
}

// enqueuePurge queues deletion of stored data on the agent holding it.
func (s *Server) enqueuePurge(agentID, jobID, name, path string, mirrorFolder bool) (*store.Run, error) {
	return s.enqueueWith(enqueueOpts{kind: proto.KindPurge, job: &store.Job{ID: jobID, Name: name}, trigger: "delete data", exec: agentID,
		params: proto.Explore{Path: path, Mirror: mirrorFolder, Name: name}})
}

// activeFor reports whether a run for this job id is queued or running.
func (s *Server) activeFor(jobID string) bool {
	active, _ := s.st.ActiveRuns()
	for _, r := range active {
		if r.JobID == jobID {
			return true
		}
	}
	return false
}

// wake nudges a long-polling agent so new work is dispatched immediately.
func (s *Server) wake(agentID string) {
	select {
	case s.wakeCh(agentID) <- struct{}{}:
	default:
	}
}

func (s *Server) wakeCh(agentID string) chan struct{} {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	ch, ok := s.wakes[agentID]
	if !ok {
		ch = make(chan struct{}, 1)
		s.wakes[agentID] = ch
	}
	return ch
}

func joinPath(dir, name string) string {
	sep := "/"
	if strings.Contains(dir, `\`) {
		sep = `\`
	}
	return strings.TrimRight(dir, `/\`) + sep + "repos" + sep + name
}

func remoteRepo(dest *store.Agent, repoName, password string) proto.Repo {
	return proto.Repo{URL: strings.TrimRight(dest.Advertise, "/") + "/" + repoName, CertPEM: dest.CertPEM, Token: dest.DataToken, Password: password}
}

func localRepo(dest *store.Agent, repoName, password string) proto.Repo {
	return proto.Repo{Local: joinPath(dest.DataDir, repoName), Password: password}
}

// buildTask turns a claimed run into a full task for the agent.
func (s *Server) buildTask(run *store.Run) (*proto.Task, error) {
	if run.Kind == proto.KindMirror {
		return s.buildMirrorTask(run)
	}
	if run.Kind == proto.KindPurge { // stored data of a (possibly already deleted) job
		e := run.Params
		return &proto.Task{RunID: run.ID, Kind: run.Kind, JobID: run.JobID, Explore: &e}, nil
	}
	var job store.Job
	if err := s.st.Get(store.KindJob, run.JobID, &job); err != nil {
		return nil, fmt.Errorf("job no longer exists")
	}
	var cp *store.CopyJob
	if run.CopyID != "" {
		cp = &store.CopyJob{}
		if err := s.st.Get(store.KindCopy, run.CopyID, cp); err != nil {
			return nil, fmt.Errorf("copy job no longer exists")
		}
	}
	// Repository the explorer / test / snapshot tasks address.
	repoAgent, repoName, repoPass := repoTarget(&job, nil)
	if cp != nil && run.Kind != proto.KindCopy {
		repoAgent, repoName, repoPass = repoTarget(&job, cp)
	}
	dest, err := s.agent(repoAgent)
	if err != nil {
		return nil, fmt.Errorf("destination agent no longer exists")
	}
	// repoFor returns the repo as seen from the executing agent.
	repoFor := func(exec string) proto.Repo {
		if exec == dest.ID {
			return localRepo(dest, repoName, repoPass)
		}
		return remoteRepo(dest, repoName, repoPass)
	}
	t := &proto.Task{RunID: run.ID, Kind: run.Kind, JobID: job.ID}
	switch run.Kind {
	case proto.KindBackup:
		t.Paths, t.Excludes, t.Mount = job.Paths, job.Excludes, job.Mount
		t.Compression, t.KeepLast, t.BandwidthKB = job.Compression, job.KeepLast, job.BandwidthKB
		t.PickTest = job.TestMode == "random" && job.TestPath == ""
		t.Repo = repoFor(job.SourceAgent)
	case proto.KindTest:
		t.Repo = repoFor(dest.ID)
		t.TestPath, t.TestRandom, t.CheckDataPct = job.TestPath, job.TestMode == "random", job.CheckDataPct
	case proto.KindPrune:
		t.Repo = repoFor(dest.ID)
		t.KeepLast = job.KeepLast
		if cp != nil {
			t.KeepLast = cp.KeepLast
		}
	case proto.KindSnapshots, proto.KindLs, proto.KindFind, proto.KindDump, proto.KindForget:
		t.Repo = repoFor(dest.ID)
		e := run.Params
		t.Explore = &e
	case proto.KindRestore:
		t.Repo = repoFor(run.AgentID)
		e := run.Params
		t.Explore = &e
	case proto.KindCopy:
		sec, err := s.agent(cp.DestAgent)
		if err != nil {
			return nil, fmt.Errorf("secondary agent no longer exists")
		}
		from := localRepo(dest, job.RepoName, job.RepoPassword)
		t.From = &from
		if cp.DestAgent == job.DestAgent {
			t.Repo = localRepo(sec, cp.RepoName, cp.RepoPassword)
		} else {
			t.Repo = remoteRepo(sec, cp.RepoName, cp.RepoPassword)
		}
		t.KeepLast = cp.KeepLast
	}
	return t, nil
}

func (s *Server) buildMirrorTask(run *store.Run) (*proto.Task, error) {
	var m store.MirrorJob
	if err := s.st.Get(store.KindMirrorJob, run.JobID, &m); err != nil {
		return nil, fmt.Errorf("mirror job no longer exists")
	}
	dest, err := s.agent(m.DestAgent)
	if err != nil {
		return nil, fmt.Errorf("destination agent no longer exists")
	}
	spec := &proto.MirrorSpec{
		SourcePath: m.SourcePath, Mount: m.Mount, Excludes: m.Excludes, DestPath: m.DestPath,
		Compare: m.Compare, Workers: m.Workers, PropagateDeletes: m.PropagateDeletes, MaxDeletePct: m.MaxDeletePct,
		BandwidthKB: m.BandwidthKB, DryRun: run.Params.DryRun, ForceDelete: run.Params.Force,
		PreserveOwner: m.PreserveOwner, OwnerByName: m.OwnerMap == "names", PreserveACLs: m.PreserveACLs,
	}
	if m.SourceAgent == m.DestAgent {
		spec.Dest.Local = true
	} else {
		spec.Dest = proto.MirrorDest{URL: strings.TrimRight(dest.Advertise, "/"), CertPEM: dest.CertPEM, Token: dest.DataToken}
	}
	return &proto.Task{RunID: run.ID, Kind: run.Kind, JobID: m.ID, Mirror: spec}, nil
}

// finishRun records a task result, applies side effects and notifies.
func (s *Server) finishRun(run *store.Run, res proto.Result) {
	status := res.Status
	if status != proto.StatusSuccess && status != proto.StatusWarning {
		status = proto.StatusFailed
	}
	if res.Summary == nil {
		res.Summary = map[string]any{}
	}
	if res.TestFile != "" {
		res.Summary["test_file"] = res.TestFile
	}
	_ = s.st.FinishRun(run.ID, status, res.Message, res.Summary)
	run.Status, run.Message, run.Summary = status, res.Message, res.Summary
	lvl := map[string]string{proto.StatusSuccess: proto.LevelInfo, proto.StatusWarning: proto.LevelWarning, proto.StatusFailed: proto.LevelError}[status]
	msg := fmt.Sprintf("%s finished: %s", run.Kind, status)
	if res.Message != "" {
		msg += " — " + res.Message
	}
	if !run.Hidden {
		s.st.AddLog(store.LogEntry{Level: lvl, RunID: run.ID, JobID: run.JobID, Source: "manager", Message: msg})
	}

	if run.Kind == proto.KindPurge {
		name := run.Params.Name
		if name == "" {
			name = "stored data"
		}
		if !run.Hidden {
			s.notifyRun(run, &store.Job{ID: run.JobID, Name: name}, nil)
		}
		return
	}
	if run.Kind == proto.KindMirror {
		if j := s.jobLike(run.JobID); j != nil && !run.Hidden {
			s.notifyRun(run, j, nil)
		}
		return
	}
	var job store.Job
	if err := s.st.Get(store.KindJob, run.JobID, &job); err != nil {
		return
	}
	firstBackup := false
	if status != proto.StatusFailed {
		_ = store.Update(s.st, store.KindJob, job.ID, func(j *store.Job) error {
			switch run.Kind {
			case proto.KindBackup:
				firstBackup = !j.Initialized
				j.Initialized = true
				if res.TestFile != "" && j.TestMode == "random" && j.TestPath == "" {
					j.TestPath = res.TestFile
				}
			case proto.KindTest:
				if res.TestFile != "" && j.TestMode == "random" {
					j.TestPath = res.TestFile
				}
			}
			job = *j
			return nil
		})
	}
	var cp *store.CopyJob
	if run.CopyID != "" {
		cp = &store.CopyJob{}
		if s.st.Get(store.KindCopy, run.CopyID, cp) != nil {
			cp = nil
		}
	}
	if !run.Hidden {
		s.notifyRun(run, &job, cp)
	}
	// Prove the chain straight after the very first backup.
	if run.Kind == proto.KindBackup && firstBackup && job.TestMode != "off" && job.TestPath != "" {
		if _, err := s.enqueue(proto.KindTest, &job, nil, "after initial backup", false); err != nil {
			log.Printf("post-backup test: %v", err)
		}
	}
}

// ---------- notifications ----------

func (s *Server) smtpCfg() (notify.Config, store.Settings) {
	c := s.settings()
	return notify.Config{Host: c.SMTPHost, Port: c.SMTPPort, Security: c.SMTPSecurity, User: c.SMTPUser, Pass: c.SMTPPass, From: c.SMTPFrom, To: c.SMTPTo}, c
}

// emit sends an email if severity meets the operator's threshold.
func (s *Server) emit(level, subject, body string) {
	nc, cfg := s.smtpCfg()
	min := cfg.EmailLevel
	if min == "never" || !nc.Ready() {
		return
	}
	if proto.LevelRank(level) < proto.LevelRank(min) {
		return
	}
	go func() {
		if err := notify.Send(nc, "[Vaultkeeper] "+subject, body); err != nil {
			s.st.AddLog(store.LogEntry{Level: proto.LevelError, Source: "manager", Message: "email failed: " + err.Error()})
		}
	}()
}

func (s *Server) notifyRun(run *store.Run, job *store.Job, cp *store.CopyJob) {
	level := map[string]string{proto.StatusSuccess: proto.LevelInfo, proto.StatusWarning: proto.LevelWarning}[run.Status]
	if level == "" {
		level = proto.LevelError
	}
	name := job.Name
	if cp != nil {
		name = cp.Name + " (copy of " + job.Name + ")"
	}
	subject := fmt.Sprintf("%s: %s — %s", strings.ToUpper(run.Status), name, run.Kind)
	var b strings.Builder
	fmt.Fprintf(&b, "Job:     %s\nTask:    %s\nResult:  %s\nTrigger: %s\n", name, run.Kind, run.Status, run.Trigger)
	if run.Message != "" {
		fmt.Fprintf(&b, "Message: %s\n", run.Message)
	}
	for k, v := range run.Summary {
		fmt.Fprintf(&b, "  %s: %v\n", k, v)
	}
	logs := s.st.RunLogs(run.ID)
	if len(logs) > 20 {
		logs = logs[len(logs)-20:]
	}
	b.WriteString("\nRecent log:\n")
	for _, l := range logs {
		fmt.Fprintf(&b, "  %s [%s] %s\n", time.UnixMilli(l.TS).Format("15:04:05"), l.Level, l.Message)
	}
	s.emit(level, subject, b.String())
}

// ---------- watchdog ----------

func (s *Server) watchdog(ctx context.Context) {
	tk := time.NewTicker(30 * time.Second)
	defer tk.Stop()
	purge := time.NewTicker(6 * time.Hour)
	defer purge.Stop()
	s.checkAgents()
	for {
		select {
		case <-ctx.Done():
			return
		case <-purge.C:
			s.st.Purge(max(s.settings().LogRetentionDays, 7))
		case <-tk.C:
			s.checkAgents()
			s.reapRuns()
			s.maybeAutoBackup()
		}
	}
}

func (s *Server) checkAgents() {
	agents, _ := store.List[store.Agent](s.st, store.KindAgent)
	for _, a := range agents {
		isOn := online(&a)
		switch {
		case !isOn && !a.OfflineNotified:
			_ = store.Update(s.st, store.KindAgent, a.ID, func(x *store.Agent) error { x.OfflineNotified = true; return nil })
			s.st.AddLog(store.LogEntry{Level: proto.LevelError, Source: "manager", Message: fmt.Sprintf("agent %q went offline", a.Name)})
			s.emit(proto.LevelError, fmt.Sprintf("agent offline: %s", a.Name), fmt.Sprintf("Agent %q (%s) has not contacted the manager since %s.", a.Name, a.Hostname, time.Unix(a.LastSeen, 0).Format(time.RFC1123)))
		case isOn && a.OfflineNotified:
			_ = store.Update(s.st, store.KindAgent, a.ID, func(x *store.Agent) error { x.OfflineNotified = false; return nil })
			s.st.AddLog(store.LogEntry{Level: proto.LevelInfo, Source: "manager", Message: fmt.Sprintf("agent %q is back online", a.Name)})
			s.emit(proto.LevelInfo, fmt.Sprintf("agent back online: %s", a.Name), fmt.Sprintf("Agent %q is online again.", a.Name))
		}
		s.checkDisk(&a, isOn)
	}
}

// checkDisk emails once when a destination runs low on space, and again when it recovers.
func (s *Server) checkDisk(a *store.Agent, isOn bool) {
	pct := s.settings().DiskAlertPct
	if pct <= 0 || !isOn || !a.Has("dest") {
		return
	}
	free := func(total, avail uint64) float64 {
		if total == 0 {
			return 100
		}
		return float64(avail) / float64(total) * 100
	}
	dataFree, mirrorFree := free(a.DiskTotal, a.DiskFree), free(a.MirrorTotal, a.MirrorFree)
	low := dataFree < float64(pct) || mirrorFree < float64(pct)
	ok := dataFree >= float64(pct)+3 && mirrorFree >= float64(pct)+3
	switch {
	case low && !a.LowDiskNotified:
		_ = store.Update(s.st, store.KindAgent, a.ID, func(x *store.Agent) error { x.LowDiskNotified = true; return nil })
		msg := fmt.Sprintf("destination %q is running low on space: %.1f%% free of the data disk (%s free)", a.Name, dataFree, fmtBytesSrv(a.DiskFree))
		if a.MirrorTotal > 0 {
			msg += fmt.Sprintf(", %.1f%% free where mirrors are stored (%s free)", mirrorFree, fmtBytesSrv(a.MirrorFree))
		}
		s.st.AddLog(store.LogEntry{Level: proto.LevelWarning, Source: "manager", Message: msg})
		s.emit(proto.LevelWarning, "low disk space: "+a.Name, msg+".\nAlert threshold: less than "+fmt.Sprint(pct)+"% free.")
	case ok && a.LowDiskNotified:
		_ = store.Update(s.st, store.KindAgent, a.ID, func(x *store.Agent) error { x.LowDiskNotified = false; return nil })
		s.st.AddLog(store.LogEntry{Level: proto.LevelInfo, Source: "manager", Message: fmt.Sprintf("destination %q has enough free space again", a.Name)})
	}
}

func fmtBytesSrv(n uint64) string {
	f, u := float64(n), []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for f >= 1024 && i < len(u)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", f, u[i])
}

func (s *Server) reapRuns() {
	active, _ := s.st.ActiveRuns()
	now := time.Now().Unix()
	for _, r := range active {
		var reason string
		if r.Status == proto.StatusRunning && now-r.Activity > 600 {
			reason = "lost contact with agent while running"
		} else if r.Status == proto.StatusQueued && now-r.Created > 1800 {
			reason = "agent did not pick up the task within 30 minutes"
		}
		if reason == "" {
			continue
		}
		_ = s.st.FinishRun(r.ID, proto.StatusFailed, reason, nil)
		s.st.AddLog(store.LogEntry{Level: proto.LevelError, RunID: r.ID, JobID: r.JobID, Source: "manager", Message: reason})
		r.Status, r.Message = proto.StatusFailed, reason
		if job := s.jobLike(r.JobID); job != nil && !r.Hidden {
			var cp *store.CopyJob
			if r.CopyID != "" {
				cp = &store.CopyJob{}
				_ = s.st.Get(store.KindCopy, r.CopyID, cp)
			}
			s.notifyRun(r, job, cp)
		}
	}
}
