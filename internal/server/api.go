package server

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"golang.org/x/crypto/bcrypt"

	"vaultkeeper/internal/notify"
	"vaultkeeper/internal/proto"
	"vaultkeeper/internal/store"
)

func genPassword() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func validCron(spec string) error {
	if spec == "" {
		return nil
	}
	_, err := cron.ParseStandard(spec)
	return err
}

func cronNext(spec string) int64 {
	if spec == "" {
		return 0
	}
	sc, err := cron.ParseStandard(spec)
	if err != nil {
		return 0
	}
	return sc.Next(time.Now()).Unix()
}

// ---------- views ----------

type runView struct {
	*store.Run
	JobName   string `json:"job_name"`
	CopyName  string `json:"copy_name,omitempty"`
	AgentName string `json:"agent_name"`
}

type lookups struct {
	agents  map[string]*store.Agent
	jobs    map[string]*store.Job
	copies  map[string]*store.CopyJob
	mirrors map[string]*store.MirrorJob
}

func (s *Server) lookups() *lookups {
	l := &lookups{agents: map[string]*store.Agent{}, jobs: map[string]*store.Job{}, copies: map[string]*store.CopyJob{}, mirrors: map[string]*store.MirrorJob{}}
	as, _ := store.List[store.Agent](s.st, store.KindAgent)
	for i := range as {
		as[i].Online = online(&as[i])
		l.agents[as[i].ID] = &as[i]
	}
	js, _ := store.List[store.Job](s.st, store.KindJob)
	for i := range js {
		l.jobs[js[i].ID] = &js[i]
	}
	cs, _ := store.List[store.CopyJob](s.st, store.KindCopy)
	for i := range cs {
		l.copies[cs[i].ID] = &cs[i]
	}
	ms, _ := store.List[store.MirrorJob](s.st, store.KindMirrorJob)
	for i := range ms {
		l.mirrors[ms[i].ID] = &ms[i]
	}
	return l
}

func (l *lookups) agentName(id string) string {
	if a := l.agents[id]; a != nil {
		return a.Name
	}
	return "(removed)"
}

func (l *lookups) jobName(id string) string {
	if j := l.jobs[id]; j != nil {
		return j.Name
	}
	if m := l.mirrors[id]; m != nil {
		return m.Name
	}
	return ""
}

func (l *lookups) view(r *store.Run) runView {
	v := runView{Run: r, AgentName: l.agentName(r.AgentID)}
	if v.JobName = l.jobName(r.JobID); v.JobName == "" {
		v.JobName = "(deleted)"
		if r.Kind == proto.KindPurge && r.Params.Name != "" {
			v.JobName = r.Params.Name + " (deleted)"
		}
	}
	if c := l.copies[r.CopyID]; c != nil {
		v.CopyName = c.Name
	}
	return v
}

func worst(statuses ...string) string {
	rank := map[string]int{"": 0, proto.StatusSuccess: 1, proto.StatusWarning: 2, proto.StatusFailed: 3}
	w := ""
	for _, s := range statuses {
		if rank[s] > rank[w] {
			w = s
		}
	}
	return w
}

func runStatus(r *store.Run) string {
	if r == nil {
		return ""
	}
	return r.Status
}

func health(runs ...*store.Run) string {
	var sts []string
	for _, r := range runs {
		if r != nil && r.Status == proto.StatusRunning {
			return "running"
		}
		if r != nil && r.Status == proto.StatusQueued {
			return "queued"
		}
		sts = append(sts, runStatus(r))
	}
	if w := worst(sts...); w != "" {
		return w
	}
	return "unknown"
}

type jobView struct {
	store.Job
	RepoPassword string     `json:"repo_password,omitempty"` // shadowed: never sent
	SourceName   string     `json:"source_name"`
	DestName     string     `json:"dest_name"`
	SourceOnline bool       `json:"source_online"`
	DestOnline   bool       `json:"dest_online"`
	LastBackup   *store.Run `json:"last_backup"`
	LastTest     *store.Run `json:"last_test"`
	History      []string   `json:"history"`
	TestHistory  []string   `json:"test_history"`
	NextBackup   int64      `json:"next_backup"`
	NextTest     int64      `json:"next_test"`
	Health       string     `json:"health"`
	RepoSize     int64      `json:"repo_size"`
}

func (s *Server) jobView(l *lookups, j *store.Job) jobView {
	v := jobView{Job: *j}
	v.Job.RepoPassword = ""
	v.SourceName, v.DestName = l.agentName(j.SourceAgent), l.agentName(j.DestAgent)
	if a := l.agents[j.SourceAgent]; a != nil {
		v.SourceOnline = a.Online
	}
	if a := l.agents[j.DestAgent]; a != nil {
		v.DestOnline = a.Online
		v.RepoSize = a.RepoSizes[j.RepoName]
	}
	v.LastBackup, v.LastTest = s.st.LastRun(j.ID, proto.KindBackup), s.st.LastRun(j.ID, proto.KindTest)
	v.History = s.st.RecentStatuses(j.ID, proto.KindBackup, "", 14)
	v.TestHistory = s.st.RecentStatuses(j.ID, proto.KindTest, "", 14)
	if j.Enabled {
		v.NextBackup = s.nextRun("backup:" + j.ID)
		if j.TestMode != "off" {
			v.NextTest = s.nextRun("test:" + j.ID)
		}
	}
	tr := v.LastTest
	if j.TestMode == "off" {
		tr = nil
	}
	v.Health = health(v.LastBackup, tr)
	return v
}

// ---------- dashboard ----------

func (s *Server) apiDashboard(w http.ResponseWriter, r *http.Request) {
	l := s.lookups()
	jobs := []jobView{}
	ids := make([]string, 0, len(l.jobs))
	for id := range l.jobs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return l.jobs[ids[i]].Name < l.jobs[ids[j]].Name })
	for _, id := range ids {
		jobs = append(jobs, s.jobView(l, l.jobs[id]))
	}
	copies := []map[string]any{}
	for _, c := range l.copies {
		lr := s.st.LastCopyRun(c.ID)
		src := ""
		if j := l.jobs[c.JobID]; j != nil {
			src = j.Name
		}
		copies = append(copies, map[string]any{
			"id": c.ID, "name": c.Name, "enabled": c.Enabled, "source_job": src, "dest_name": l.agentName(c.DestAgent),
			"schedule": c.Schedule, "next_run": s.nextRun("copy:" + c.ID), "last_run": lr,
			"history": s.st.RecentStatuses(c.JobID, proto.KindCopy, c.ID, 14), "health": health(lr),
		})
	}
	sort.Slice(copies, func(i, j int) bool { return copies[i]["name"].(string) < copies[j]["name"].(string) })

	var total, onl, dtot, dfree = 0, 0, uint64(0), uint64(0)
	for _, a := range l.agents {
		total++
		if a.Online {
			onl++
		}
		if a.Has("dest") {
			dtot += a.DiskTotal
			dfree += a.DiskFree
		}
	}
	counts := map[string]int{}
	for _, j := range jobs {
		counts[j.Health]++
	}
	mirrors := []mirrorView{}
	for _, m := range l.mirrors {
		mv := s.mirrorView(l, m)
		mirrors = append(mirrors, mv)
		counts[mv.Health]++
	}
	sort.Slice(mirrors, func(i, j int) bool { return mirrors[i].Name < mirrors[j].Name })
	problems := s.st.SearchLogs([]string{proto.LevelWarning, proto.LevelError}, "", "", 0, 8)
	active, _ := s.st.ActiveRuns()
	writeJSON(w, 200, map[string]any{
		"jobs": jobs, "copies": copies, "mirrors": mirrors, "counts": counts, "problems": problems, "active": len(active),
		"agents": map[string]any{"total": total, "online": onl, "dest_total": dtot, "dest_free": dfree},
	})
}

// ---------- agents ----------

func (s *Server) apiAgents(w http.ResponseWriter, r *http.Request) {
	l := s.lookups()
	type repo struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
		Size int64  `json:"size"`
		Repo string `json:"repo"`
	}
	out := []map[string]any{}
	ids := []string{}
	for id := range l.agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a := l.agents[id]
		repos := []repo{}
		var used int64
		for _, j := range l.jobs {
			if j.DestAgent == a.ID {
				repos = append(repos, repo{j.Name, "backup", a.RepoSizes[j.RepoName], j.RepoName})
				used += a.RepoSizes[j.RepoName]
			}
		}
		for _, c := range l.copies {
			if c.DestAgent == a.ID {
				repos = append(repos, repo{c.Name, "copy", a.RepoSizes[c.RepoName], c.RepoName})
				used += a.RepoSizes[c.RepoName]
			}
		}
		for _, m := range l.mirrors {
			if m.DestAgent == a.ID {
				repos = append(repos, repo{m.Name, "mirror", 0, m.DestPath})
			}
		}
		sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
		srcJobs, destRepos := 0, 0
		for _, j := range l.jobs {
			if j.SourceAgent == a.ID {
				srcJobs++
			}
		}
		destRepos = len(repos)
		type orphan struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		}
		orphans := []orphan{}
		ref := l.referencedRepos()
		for name, size := range a.RepoSizes {
			if !ref[name] {
				orphans = append(orphans, orphan{name, size})
			}
		}
		sort.Slice(orphans, func(i, j int) bool { return orphans[i].Name < orphans[j].Name })
		out = append(out, map[string]any{"agent": a, "repos": repos, "used": used, "source_jobs": srcJobs, "dest_repos": destRepos, "orphans": orphans})
	}
	writeJSON(w, 200, out)
}

func (s *Server) apiAgentRename(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if readJSON(r, &in) != nil || strings.TrimSpace(in.Name) == "" {
		httpErr(w, 400, "name required")
		return
	}
	err := store.Update(s.st, store.KindAgent, r.PathValue("id"), func(a *store.Agent) error { a.Name = strings.TrimSpace(in.Name); return nil })
	if err != nil {
		httpErr(w, 404, "no such agent")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) apiAgentDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	l := s.lookups()
	for _, j := range l.jobs {
		if j.SourceAgent == id || j.DestAgent == id {
			httpErr(w, 409, "agent is used by job %q", j.Name)
			return
		}
	}
	for _, c := range l.copies {
		if c.DestAgent == id {
			httpErr(w, 409, "agent is used by copy job %q", c.Name)
			return
		}
	}
	for _, m := range l.mirrors {
		if m.SourceAgent == id || m.DestAgent == id {
			httpErr(w, 409, "agent is used by mirror job %q", m.Name)
			return
		}
	}
	_ = s.st.Delete(store.KindAgent, id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- jobs ----------

func (s *Server) apiJobs(w http.ResponseWriter, r *http.Request) {
	l := s.lookups()
	out := []jobView{}
	for _, j := range l.jobs {
		out = append(out, s.jobView(l, j))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, 200, out)
}

func (s *Server) apiJobGet(w http.ResponseWriter, r *http.Request) {
	l := s.lookups()
	j := l.jobs[r.PathValue("id")]
	if j == nil {
		httpErr(w, 404, "no such job")
		return
	}
	writeJSON(w, 200, s.jobView(l, j))
}

var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func cleanList(in []string) []string {
	out := []string{}
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (s *Server) apiJobSave(w http.ResponseWriter, r *http.Request) {
	var in store.Job
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, "bad request: %v", err)
		return
	}
	l := s.lookups()
	id := r.PathValue("id")
	var old *store.Job
	if id != "" {
		if old = l.jobs[id]; old == nil {
			httpErr(w, 404, "no such job")
			return
		}
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Paths, in.Excludes = cleanList(in.Paths), cleanList(in.Excludes)
	switch {
	case in.Name == "":
		httpErr(w, 400, "name is required")
		return
	case l.agents[in.SourceAgent] == nil || !l.agents[in.SourceAgent].Has("source"):
		httpErr(w, 400, "choose a source agent")
		return
	case l.agents[in.DestAgent] == nil || !l.agents[in.DestAgent].Has("dest"):
		httpErr(w, 400, "choose a destination agent")
		return
	case len(in.Paths) == 0:
		httpErr(w, 400, "at least one path to back up is required")
		return
	case in.KeepLast < 0:
		httpErr(w, 400, "restore points must be 0 (unlimited) or more")
		return
	case in.Compression != "auto" && in.Compression != "max" && in.Compression != "off":
		httpErr(w, 400, "compression must be auto, max or off")
		return
	case in.TestMode != "random" && in.TestMode != "manual" && in.TestMode != "off":
		httpErr(w, 400, "test mode must be random, manual or off")
		return
	case in.TestMode == "manual" && strings.TrimSpace(in.TestPath) == "":
		httpErr(w, 400, "a test file path is required in manual mode")
		return
	case in.CheckDataPct < 0 || in.CheckDataPct > 100:
		httpErr(w, 400, "data check percentage must be 0-100")
		return
	}
	if in.Mount != nil && in.Mount.Remote == "" {
		in.Mount = nil
	}
	if in.Mount != nil && in.Mount.Type != "smb" && in.Mount.Type != "nfs" {
		httpErr(w, 400, "mount type must be smb or nfs")
		return
	}
	if err := validCron(in.Schedule); err != nil {
		httpErr(w, 400, "backup schedule: %v", err)
		return
	}
	if err := validCron(in.TestSchedule); err != nil {
		httpErr(w, 400, "test schedule: %v", err)
		return
	}
	if old == nil {
		in.ID = rid(6)
		in.Created = time.Now().Unix()
		in.RepoName = "job-" + in.ID
		in.RepoPassword = strings.TrimSpace(in.RepoPassword)
		if in.RepoPassword == "" {
			in.RepoPassword = genPassword()
		}
		in.Initialized = false
		if in.TestMode == "manual" {
			in.TestPath = strings.TrimSpace(in.TestPath)
		} else if in.TestMode == "random" {
			in.TestPath = ""
		}
	} else {
		if old.Initialized && old.DestAgent != in.DestAgent {
			httpErr(w, 409, "destination can't be changed after the first backup (the repository lives there)")
			return
		}
		in.ID, in.Created, in.RepoName, in.RepoPassword, in.Initialized = old.ID, old.Created, old.RepoName, old.RepoPassword, old.Initialized
		if in.TestMode == "manual" {
			in.TestPath = strings.TrimSpace(in.TestPath)
		} else if in.TestMode == "random" && old.TestMode == "random" {
			in.TestPath = old.TestPath
		} else {
			in.TestPath = ""
		}
		// Keep stored mount password if the UI left it blank.
		if in.Mount != nil && in.Mount.Password == "" && old.Mount != nil {
			in.Mount.Password = old.Mount.Password
		}
	}
	if err := s.st.Put(store.KindJob, in.ID, in); err != nil {
		httpErr(w, 500, "%v", err)
		return
	}
	s.reloadSchedules()
	writeJSON(w, 200, map[string]string{"id": in.ID})
}

// deleteWithData is shared by the delete endpoints: when ?delete_data=1 it
// requires the item's name as confirmation, and queues the removal of the
// stored data before the configuration is deleted. It returns the purge run
// id ("" if data is kept) and false if the request was refused.
func (s *Server) deleteWithData(w http.ResponseWriter, r *http.Request, jobID, name, agentID, path string, mirrorFolder bool) (string, bool) {
	if s.activeFor(jobID) {
		httpErr(w, 409, "a run for %q is queued or running; cancel it or wait for it to finish first", name)
		return "", false
	}
	if r.URL.Query().Get("delete_data") != "1" {
		return "", true
	}
	if r.URL.Query().Get("confirm") != name {
		httpErr(w, 400, "type the exact name %q to confirm deleting its data", name)
		return "", false
	}
	a, err := s.agent(agentID)
	if err != nil || !a.Online {
		httpErr(w, 409, "the destination agent is offline, so its data can't be deleted now; delete without data, or try again when it is back")
		return "", false
	}
	run, err := s.enqueuePurge(agentID, jobID, name, path, mirrorFolder)
	if err != nil {
		httpErr(w, 409, "%v", err)
		return "", false
	}
	return run.ID, true
}

func (s *Server) apiJobDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	l := s.lookups()
	job := l.jobs[id]
	if job == nil {
		httpErr(w, 404, "no such job")
		return
	}
	for _, c := range l.copies {
		if c.JobID == id {
			httpErr(w, 409, "copy job %q depends on this job; delete it first", c.Name)
			return
		}
	}
	runID, ok := s.deleteWithData(w, r, id, job.Name, job.DestAgent, job.RepoName, false)
	if !ok {
		return
	}
	_ = s.st.Delete(store.KindJob, id)
	s.reloadSchedules()
	msg := "job deleted (backup data on the destination was left in place)"
	if runID != "" {
		msg = "job deleted; deleting its backup data from the destination"
	}
	s.st.AddLog(store.LogEntry{Level: proto.LevelWarning, Source: "manager", Message: fmt.Sprintf("%s: %s", job.Name, msg)})
	writeJSON(w, 200, map[string]string{"ok": "true", "run_id": runID})
}

func (s *Server) apiJobRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind string `json:"kind"`
	}
	_ = readJSON(r, &in)
	if in.Kind == "" {
		in.Kind = proto.KindBackup
	}
	var job store.Job
	if s.st.Get(store.KindJob, r.PathValue("id"), &job) != nil {
		httpErr(w, 404, "no such job")
		return
	}
	if in.Kind == proto.KindTest && !job.Initialized {
		httpErr(w, 409, "run a backup first")
		return
	}
	if in.Kind != proto.KindBackup && in.Kind != proto.KindTest && in.Kind != proto.KindPrune {
		httpErr(w, 400, "kind must be backup, test or prune")
		return
	}
	if in.Kind == proto.KindPrune {
		if !job.Initialized {
			httpErr(w, 409, "run a backup first")
			return
		}
		if job.KeepLast == 0 {
			httpErr(w, 409, "no restore-point limit is set for this job (unlimited), so there is nothing to prune")
			return
		}
	}
	run, err := s.enqueue(in.Kind, &job, nil, "manual", false)
	if err != nil {
		httpErr(w, 409, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]string{"run_id": run.ID, "status": run.Status})
}

func (s *Server) apiJobKey(w http.ResponseWriter, r *http.Request) {
	var job store.Job
	if s.st.Get(store.KindJob, r.PathValue("id"), &job) != nil {
		httpErr(w, 404, "no such job")
		return
	}
	writeJSON(w, 200, map[string]string{"repo": job.RepoName, "password": job.RepoPassword})
}

// ---------- copy jobs ----------

type copyView struct {
	store.CopyJob
	RepoPassword string     `json:"repo_password,omitempty"`
	SourceJob    string     `json:"source_job"`
	DestName     string     `json:"dest_name"`
	LastRun      *store.Run `json:"last_run"`
	NextRun      int64      `json:"next_run"`
	History      []string   `json:"history"`
	Health       string     `json:"health"`
	RepoSize     int64      `json:"repo_size"`
}

func (s *Server) apiCopies(w http.ResponseWriter, r *http.Request) {
	l := s.lookups()
	out := []copyView{}
	for _, c := range l.copies {
		v := copyView{CopyJob: *c}
		v.CopyJob.RepoPassword = ""
		if j := l.jobs[c.JobID]; j != nil {
			v.SourceJob = j.Name
		}
		v.DestName = l.agentName(c.DestAgent)
		if a := l.agents[c.DestAgent]; a != nil {
			v.RepoSize = a.RepoSizes[c.RepoName]
		}
		v.LastRun = s.st.LastCopyRun(c.ID)
		v.History = s.st.RecentStatuses(c.JobID, proto.KindCopy, c.ID, 14)
		v.Health = health(v.LastRun)
		if c.Enabled {
			v.NextRun = s.nextRun("copy:" + c.ID)
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, 200, out)
}

func (s *Server) apiCopySave(w http.ResponseWriter, r *http.Request) {
	var in store.CopyJob
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, "bad request: %v", err)
		return
	}
	l := s.lookups()
	id := r.PathValue("id")
	var old *store.CopyJob
	if id != "" {
		if old = l.copies[id]; old == nil {
			httpErr(w, 404, "no such copy job")
			return
		}
	}
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "":
		httpErr(w, 400, "name is required")
		return
	case l.jobs[in.JobID] == nil:
		httpErr(w, 400, "choose the backup job to copy")
		return
	case l.agents[in.DestAgent] == nil || !l.agents[in.DestAgent].Has("dest"):
		httpErr(w, 400, "choose a destination agent")
		return
	case in.KeepLast < 0:
		httpErr(w, 400, "restore points must be 0 or more")
		return
	}
	if err := validCron(in.Schedule); err != nil {
		httpErr(w, 400, "schedule: %v", err)
		return
	}
	if old == nil {
		in.ID, in.Created = rid(6), time.Now().Unix()
		in.RepoName = "copy-" + in.ID
		in.RepoPassword = genPassword()
	} else {
		if old.JobID != in.JobID || old.DestAgent != in.DestAgent {
			httpErr(w, 409, "source job and destination can't be changed; create a new copy job instead")
			return
		}
		in.ID, in.Created, in.RepoName, in.RepoPassword = old.ID, old.Created, old.RepoName, old.RepoPassword
	}
	if err := s.st.Put(store.KindCopy, in.ID, in); err != nil {
		httpErr(w, 500, "%v", err)
		return
	}
	s.reloadSchedules()
	writeJSON(w, 200, map[string]string{"id": in.ID})
}

func (s *Server) apiCopyDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var cp store.CopyJob
	if s.st.Get(store.KindCopy, id, &cp) != nil {
		httpErr(w, 404, "no such copy job")
		return
	}
	runID, ok := s.deleteWithData(w, r, cp.JobID, cp.Name, cp.DestAgent, cp.RepoName, false)
	if !ok {
		return
	}
	_ = s.st.Delete(store.KindCopy, id)
	s.reloadSchedules()
	s.st.AddLog(store.LogEntry{Level: proto.LevelWarning, Source: "manager", Message: "copy job deleted: " + cp.Name})
	writeJSON(w, 200, map[string]string{"ok": "true", "run_id": runID})
}

func (s *Server) apiCopyRun(w http.ResponseWriter, r *http.Request) {
	var cp store.CopyJob
	if s.st.Get(store.KindCopy, r.PathValue("id"), &cp) != nil {
		httpErr(w, 404, "no such copy job")
		return
	}
	var job store.Job
	if s.st.Get(store.KindJob, cp.JobID, &job) != nil || !job.Initialized {
		httpErr(w, 409, "the source job has no backup to copy yet")
		return
	}
	run, err := s.enqueue(proto.KindCopy, &job, &cp, "manual", false)
	if err != nil {
		httpErr(w, 409, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]string{"run_id": run.ID, "status": run.Status})
}

func (s *Server) apiCopyPrune(w http.ResponseWriter, r *http.Request) {
	var cp store.CopyJob
	if s.st.Get(store.KindCopy, r.PathValue("id"), &cp) != nil {
		httpErr(w, 404, "no such copy job")
		return
	}
	if cp.KeepLast == 0 {
		httpErr(w, 409, "no restore-point limit is set for this copy (unlimited), so there is nothing to prune")
		return
	}
	var job store.Job
	if s.st.Get(store.KindJob, cp.JobID, &job) != nil {
		httpErr(w, 404, "source job missing")
		return
	}
	run, err := s.enqueue(proto.KindPrune, &job, &cp, "manual", false)
	if err != nil {
		httpErr(w, 409, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]string{"run_id": run.ID, "status": run.Status})
}

func (s *Server) apiCopyKey(w http.ResponseWriter, r *http.Request) {
	var cp store.CopyJob
	if s.st.Get(store.KindCopy, r.PathValue("id"), &cp) != nil {
		httpErr(w, 404, "no such copy job")
		return
	}
	writeJSON(w, 200, map[string]string{"repo": cp.RepoName, "password": cp.RepoPassword})
}

// ---------- runs & logs ----------

func (s *Server) apiRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	runs, err := s.st.ListRuns(q.Get("job_id"), q.Get("kind"), q.Get("status"), limit)
	if err != nil {
		httpErr(w, 500, "%v", err)
		return
	}
	l := s.lookups()
	out := make([]runView, 0, len(runs))
	for _, r := range runs {
		out = append(out, l.view(r))
	}
	writeJSON(w, 200, out)
}

func (s *Server) apiRunGet(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.GetRun(r.PathValue("id"))
	if err != nil {
		httpErr(w, 404, "no such run")
		return
	}
	writeJSON(w, 200, map[string]any{"run": s.lookups().view(run), "logs": s.st.RunLogs(run.ID)})
}

func (s *Server) apiRunCancel(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.GetRun(r.PathValue("id"))
	if err != nil {
		httpErr(w, 404, "no such run")
		return
	}
	if run.Status == proto.StatusQueued {
		_ = s.st.FinishRun(run.ID, proto.StatusFailed, "cancelled before start", nil)
	} else {
		_ = s.st.RequestCancel(run.ID)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) apiLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	var levels []string
	if min := q.Get("level"); min != "" {
		for _, lv := range []string{proto.LevelDebug, proto.LevelInfo, proto.LevelWarning, proto.LevelError} {
			if proto.LevelRank(lv) >= proto.LevelRank(min) {
				levels = append(levels, lv)
			}
		}
	}
	logs := s.st.SearchLogs(levels, q.Get("job_id"), q.Get("q"), before, limit)
	l := s.lookups()
	type lv struct {
		store.LogEntry
		JobName string `json:"job_name"`
	}
	out := make([]lv, 0, len(logs))
	for _, e := range logs {
		v := lv{LogEntry: e, JobName: l.jobName(e.JobID)}
		out = append(out, v)
	}
	writeJSON(w, 200, out)
}

// ---------- settings ----------

func (s *Server) apiSettingsGet(w http.ResponseWriter, r *http.Request) {
	c := s.settings()
	writeJSON(w, 200, map[string]any{
		"smtp_host": c.SMTPHost, "smtp_port": c.SMTPPort, "smtp_security": c.SMTPSecurity, "smtp_user": c.SMTPUser,
		"smtp_pass_set": c.SMTPPass != "", "smtp_from": c.SMTPFrom, "smtp_to": c.SMTPTo, "email_level": c.EmailLevel,
		"log_retention_days": c.LogRetentionDays, "enroll_token": c.EnrollToken,
		"disk_alert_pct": c.DiskAlertPct, "auto_backup": c.AutoBackup, "auto_backup_dir": c.AutoBackupDir, "auto_backup_keep": c.AutoBackupKeep,
		"auto_backup_pass_set": c.AutoBackupPass != "", "last_auto_backup": c.LastAutoBackup, "last_auto_backup_msg": c.LastAutoBackupMsg,
	})
}

func (s *Server) apiSettingsPut(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Host     string `json:"smtp_host"`
		Port     int    `json:"smtp_port"`
		Security string `json:"smtp_security"`
		User     string `json:"smtp_user"`
		Pass     string `json:"smtp_pass"`
		From     string `json:"smtp_from"`
		To       string `json:"smtp_to"`
		Level    string `json:"email_level"`
		Days     int    `json:"log_retention_days"`
		DiskPct  int    `json:"disk_alert_pct"`
		AutoOn   bool   `json:"auto_backup"`
		AutoDir  string `json:"auto_backup_dir"`
		AutoKeep int    `json:"auto_backup_keep"`
		AutoPass string `json:"auto_backup_pass"`
	}
	if readJSON(r, &in) != nil {
		httpErr(w, 400, "bad request")
		return
	}
	switch in.Level {
	case "info", "warning", "error", "never":
	default:
		httpErr(w, 400, "invalid email level")
		return
	}
	if in.Days < 7 {
		in.Days = 7
	}
	if in.DiskPct < 0 || in.DiskPct > 90 {
		httpErr(w, 400, "the low-disk alert threshold must be 0-90 (percent free; 0 = off)")
		return
	}
	if in.AutoKeep < 1 || in.AutoKeep > 365 {
		in.AutoKeep = 14
	}
	if in.AutoPass != "" && len(in.AutoPass) < 10 {
		httpErr(w, 400, "the automatic-backup passphrase must be at least 10 characters")
		return
	}
	if in.AutoOn && in.AutoPass == "" && s.settings().AutoBackupPass == "" {
		httpErr(w, 400, "set a passphrase to enable automatic configuration backups")
		return
	}
	_ = s.saveSettings(func(c *store.Settings) {
		c.SMTPHost, c.SMTPPort, c.SMTPSecurity, c.SMTPUser = strings.TrimSpace(in.Host), in.Port, in.Security, in.User
		if in.Pass != "" {
			c.SMTPPass = in.Pass
		}
		c.SMTPFrom, c.SMTPTo, c.EmailLevel, c.LogRetentionDays = in.From, in.To, in.Level, in.Days
		c.DiskAlertPct, c.AutoBackup, c.AutoBackupDir, c.AutoBackupKeep = in.DiskPct, in.AutoOn, strings.TrimSpace(in.AutoDir), in.AutoKeep
		if in.AutoPass != "" {
			c.AutoBackupPass = in.AutoPass
		}
	})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) apiTestEmail(w http.ResponseWriter, r *http.Request) {
	nc, _ := s.smtpCfg()
	if err := notify.Send(nc, "[Vaultkeeper] test email", "SMTP is configured correctly. You will be emailed for events at or above your chosen level."); err != nil {
		httpErr(w, 502, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) apiPassword(w http.ResponseWriter, r *http.Request) {
	var in struct{ Current, New string }
	if readJSON(r, &in) != nil || len(in.New) < 8 {
		httpErr(w, 400, "new password must be at least 8 characters")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(s.settings().AdminHash), []byte(in.Current)) != nil {
		httpErr(w, 403, "current password is incorrect")
		return
	}
	h, _ := bcrypt.GenerateFromPassword([]byte(in.New), bcrypt.DefaultCost)
	// Rotating the session secret also logs out other sessions.
	_ = s.saveSettings(func(c *store.Settings) { c.AdminHash = string(h); c.SessionSecret = rid(32) })
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) apiRotateEnroll(w http.ResponseWriter, r *http.Request) {
	tok := rid(16)
	_ = s.saveSettings(func(c *store.Settings) { c.EnrollToken = tok })
	writeJSON(w, 200, map[string]string{"enroll_token": tok})
}

var _ = fmt.Sprint
