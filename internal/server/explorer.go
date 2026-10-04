package server

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"vaultkeeper/internal/proto"
	"vaultkeeper/internal/store"
)

var repoDirRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var snapRe = regexp.MustCompile(`^([0-9a-f]{8,64}|latest)$`)

// validSnapPath accepts absolute snapshot paths (unix "/x" or windows "C:\x" / "C:/x").
func validSnapPath(p string) bool {
	if p == "" || strings.ContainsAny(p, "\x00\r\n") {
		return false
	}
	return p[0] == '/' || (len(p) >= 2 && p[1] == ':')
}

// explorerRepoLenient resolves the job and (optionally) the copy-job repository
// being explored, from the {id} path value and ?copy= query. It returns the
// agent that holds that repository.
func (s *Server) explorerRepoLenient(w http.ResponseWriter, r *http.Request) (*store.Job, *store.CopyJob, string, bool) {
	var job store.Job
	if s.st.Get(store.KindJob, r.PathValue("id"), &job) != nil {
		httpErr(w, 404, "no such job")
		return nil, nil, "", false
	}
	var cp *store.CopyJob
	if cid := r.URL.Query().Get("copy"); cid != "" {
		cp = &store.CopyJob{}
		if s.st.Get(store.KindCopy, cid, cp) != nil || cp.JobID != job.ID {
			httpErr(w, 404, "no such copy job")
			return nil, nil, "", false
		}
	}
	execID, _, _ := repoTarget(&job, cp)
	return &job, cp, execID, true
}

// explorerRepo is explorerRepoLenient plus the requirement that a backup exists.
func (s *Server) explorerRepo(w http.ResponseWriter, r *http.Request) (*store.Job, *store.CopyJob, string, bool) {
	job, cp, exec, ok := s.explorerRepoLenient(w, r)
	if ok && !job.Initialized {
		httpErr(w, 409, "this job has no backups yet")
		return nil, nil, "", false
	}
	return job, cp, exec, ok
}

// runSync runs a short read-only task and waits for its result. The run is
// transient (deleted afterwards) so browsing doesn't pollute history.
func (s *Server) runSync(o enqueueOpts, timeout time.Duration) (*store.Run, error) {
	o.hidden = true
	run, err := s.enqueueWith(o)
	if err != nil {
		return nil, err
	}
	defer s.st.DeleteRun(run.ID)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cur, err := s.st.GetRun(run.ID)
		if err == nil && cur.Status != proto.StatusQueued && cur.Status != proto.StatusRunning {
			if cur.Status == proto.StatusFailed {
				return nil, fmt.Errorf("%s", cur.Message)
			}
			return cur, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = s.st.RequestCancel(run.ID)
	return nil, fmt.Errorf("the agent did not answer in time")
}

func (s *Server) apiJobSnapshots(w http.ResponseWriter, r *http.Request) {
	job, cp, exec, ok := s.explorerRepoLenient(w, r)
	if !ok {
		return
	}
	if !job.Initialized {
		writeJSON(w, 200, []any{})
		return
	}
	run, err := s.runSync(enqueueOpts{kind: proto.KindSnapshots, job: job, cp: cp, trigger: "ui", exec: exec}, 45*time.Second)
	if err != nil {
		httpErr(w, 502, "%v", err)
		return
	}
	writeJSON(w, 200, run.Summary["snapshots"])
}

func (s *Server) apiBrowse(w http.ResponseWriter, r *http.Request) {
	job, cp, exec, ok := s.explorerRepo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	snap, dir := q.Get("snapshot"), q.Get("path")
	if dir == "" {
		dir = "/"
	}
	if !snapRe.MatchString(snap) || !validSnapPath(dir) {
		httpErr(w, 400, "invalid snapshot or path")
		return
	}
	run, err := s.runSync(enqueueOpts{kind: proto.KindLs, job: job, cp: cp, trigger: "explorer", exec: exec,
		params: proto.Explore{Snapshot: snap, Path: dir}}, 60*time.Second)
	if err != nil {
		httpErr(w, 502, "%v", err)
		return
	}
	writeJSON(w, 200, run.Summary)
}

func (s *Server) apiSearch(w http.ResponseWriter, r *http.Request) {
	job, cp, exec, ok := s.explorerRepo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	snap, pat := q.Get("snapshot"), strings.TrimSpace(q.Get("q"))
	if !snapRe.MatchString(snap) || len(pat) < 2 || len(pat) > 200 || strings.ContainsAny(pat, "\x00\r\n") {
		httpErr(w, 400, "enter at least 2 characters to search")
		return
	}
	run, err := s.runSync(enqueueOpts{kind: proto.KindFind, job: job, cp: cp, trigger: "explorer", exec: exec,
		params: proto.Explore{Snapshot: snap, Query: pat}}, 120*time.Second)
	if err != nil {
		httpErr(w, 502, "%v", err)
		return
	}
	writeJSON(w, 200, run.Summary)
}

// ---------- browser download (agent -> manager -> browser, no temp file) ----------

type download struct {
	pr        *io.PipeReader
	pw        *io.PipeWriter
	connected chan struct{}
	once      sync.Once
}

func (s *Server) apiDownload(w http.ResponseWriter, r *http.Request) {
	job, cp, exec, ok := s.explorerRepo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	snap, p := q.Get("snapshot"), q.Get("path")
	isDir := q.Get("type") == "dir"
	if !snapRe.MatchString(snap) || !validSnapPath(p) {
		httpErr(w, 400, "invalid snapshot or path")
		return
	}
	d := &download{connected: make(chan struct{})}
	d.pr, d.pw = io.Pipe()
	id := rid(8)
	s.dls.Store(id, d)
	defer s.dls.Delete(id)

	run, err := s.enqueueWith(enqueueOpts{id: id, kind: proto.KindDump, job: job, cp: cp, trigger: "browser download", exec: exec,
		params: proto.Explore{Snapshot: snap, Path: p, IsDir: isDir}})
	if err != nil {
		httpErr(w, 409, "%v", err)
		return
	}
	if run.Status == proto.StatusFailed {
		httpErr(w, 502, "%s", run.Message)
		return
	}
	s.st.AddLog(store.LogEntry{Level: proto.LevelInfo, RunID: run.ID, JobID: job.ID, Source: "manager", Message: fmt.Sprintf("download of %s from snapshot %s", p, snap)})

	select {
	case <-d.connected:
	case <-time.After(90 * time.Second):
		_ = s.st.RequestCancel(run.ID)
		httpErr(w, 504, "the destination agent did not start the transfer in time")
		return
	case <-r.Context().Done():
		_ = s.st.RequestCancel(run.ID)
		return
	}
	// Wait for the first byte before committing headers, so an agent-side
	// failure (bad path, wrong key…) becomes a clear error rather than a
	// dropped connection. EOF with no error means a legitimately empty file.
	br := bufio.NewReaderSize(d.pr, 64<<10)
	if _, err := br.Peek(1); err != nil && err != io.EOF {
		msg := err.Error()
		for i := 0; i < 20; i++ {
			if cur, e := s.st.GetRun(run.ID); e == nil && cur.Status == proto.StatusFailed && cur.Message != "" {
				msg = cur.Message
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		httpErr(w, 502, "%s", msg)
		return
	}
	name := path.Base(strings.ReplaceAll(p, `\`, "/"))
	if name == "/" || name == "." || len(name) == 0 || name[len(name)-1] == ':' {
		name = "backup"
	}
	ctype := "application/octet-stream"
	if isDir {
		name += ".zip"
		ctype = "application/zip"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, asciiName(name), url.PathEscape(name)))
	if _, err := io.Copy(w, br); err != nil {
		d.pr.CloseWithError(err)
		_ = s.st.RequestCancel(run.ID)
		panic(http.ErrAbortHandler) // truncate so the browser shows the download as failed
	}
}

func asciiName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			b.WriteByte('_')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// agentData receives the streamed bytes of a dump task from the agent.
func (s *Server) agentData(w http.ResponseWriter, r *http.Request, a *store.Agent) {
	run := s.ownedRun(w, r, a)
	if run == nil {
		return
	}
	v, ok := s.dls.Load(run.ID)
	if !ok {
		httpErr(w, 404, "no download is waiting for this task")
		return
	}
	d := v.(*download)
	d.once.Do(func() { close(d.connected) })
	if _, err := io.Copy(d.pw, r.Body); err != nil {
		d.pw.CloseWithError(err)
		httpErr(w, 500, "%v", err)
		return
	}
	d.pw.Close()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- restore ----------

func (s *Server) apiRestore(w http.ResponseWriter, r *http.Request) {
	job, cp, repoAgent, ok := s.explorerRepo(w, r)
	if !ok {
		return
	}
	var in struct {
		Snapshot  string   `json:"snapshot"`
		Paths     []string `json:"paths"`
		Mode      string   `json:"mode"` // original | new
		Agent     string   `json:"agent"`
		Target    string   `json:"target"`
		Overwrite string   `json:"overwrite"`
	}
	if readJSON(r, &in) != nil {
		httpErr(w, 400, "bad request")
		return
	}
	if !snapRe.MatchString(in.Snapshot) {
		httpErr(w, 400, "choose a restore point")
		return
	}
	if len(in.Paths) == 0 || len(in.Paths) > 500 {
		httpErr(w, 400, "select between 1 and 500 items to restore")
		return
	}
	for _, p := range in.Paths {
		if !validSnapPath(p) {
			httpErr(w, 400, "invalid path %q", p)
			return
		}
	}
	switch in.Overwrite {
	case "":
		in.Overwrite = "if-changed"
	case "always", "if-changed", "if-newer", "never":
	default:
		httpErr(w, 400, "invalid overwrite option")
		return
	}
	exec := in.Agent
	p := proto.Explore{Snapshot: in.Snapshot, Paths: in.Paths, Overwrite: in.Overwrite}
	switch in.Mode {
	case "original":
		if job.Mount != nil {
			httpErr(w, 400, "this job backs up a network share; restoring to the original location isn't supported — restore to a new location instead")
			return
		}
		exec, p.Original = job.SourceAgent, true
	case "new":
		if !validSnapPath(in.Target) || strings.Contains(in.Target, "..") {
			httpErr(w, 400, "enter an absolute target folder on the chosen agent")
			return
		}
		p.Target = in.Target
	default:
		httpErr(w, 400, "mode must be original or new")
		return
	}
	ag, err := s.agent(exec)
	if err != nil {
		httpErr(w, 400, "choose an agent to restore onto")
		return
	}
	if p.Original && ag.OS == "windows" {
		httpErr(w, 400, "restoring to the original location isn't supported on Windows agents yet; use a new location")
		return
	}
	o := enqueueOpts{kind: proto.KindRestore, job: job, cp: cp, trigger: "explorer restore", exec: exec, params: p}
	if repoAgent != exec {
		o.need = []string{repoAgent}
	}
	run, err := s.enqueueWith(o)
	if err != nil {
		httpErr(w, 409, "%v", err)
		return
	}
	where := p.Target
	if p.Original {
		where = "original location"
	}
	s.st.AddLog(store.LogEntry{Level: proto.LevelWarning, RunID: run.ID, JobID: job.ID, Source: "manager",
		Message: fmt.Sprintf("restore requested: %d item(s) from snapshot %s to %s on %s (overwrite: %s)", len(in.Paths), in.Snapshot, where, ag.Name, p.Overwrite)})
	writeJSON(w, 200, map[string]string{"run_id": run.ID, "status": run.Status})
}

// apiSnapshotDelete permanently deletes one restore point (and prunes its data).
func (s *Server) apiSnapshotDelete(w http.ResponseWriter, r *http.Request) {
	job, cp, exec, ok := s.explorerRepo(w, r)
	if !ok {
		return
	}
	snap := r.PathValue("snap")
	if !snapRe.MatchString(snap) || snap == "latest" {
		httpErr(w, 400, "invalid restore point id")
		return
	}
	run, err := s.enqueueWith(enqueueOpts{kind: proto.KindForget, job: job, cp: cp, trigger: "delete restore point", exec: exec,
		params: proto.Explore{Snapshot: snap, Name: job.Name}})
	if err != nil {
		httpErr(w, 409, "%v", err)
		return
	}
	if run.Status == proto.StatusFailed {
		httpErr(w, 502, "%s", run.Message)
		return
	}
	s.st.AddLog(store.LogEntry{Level: proto.LevelWarning, RunID: run.ID, JobID: job.ID, Source: "manager", Message: fmt.Sprintf("deleting restore point %s", snap)})
	writeJSON(w, 200, map[string]string{"run_id": run.ID, "status": run.Status})
}

// referencedRepos lists repository directory names that jobs/copy jobs use.
func (l *lookups) referencedRepos() map[string]bool {
	ref := map[string]bool{}
	for _, j := range l.jobs {
		ref[j.RepoName] = true
	}
	for _, c := range l.copies {
		ref[c.RepoName] = true
	}
	return ref
}

// apiPurgeRepo deletes a repository directory that no job refers to.
func (s *Server) apiPurgeRepo(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string `json:"name"`
		Confirm string `json:"confirm"`
	}
	if readJSON(r, &in) != nil || !repoDirRe.MatchString(in.Name) {
		httpErr(w, 400, "invalid repository name")
		return
	}
	if in.Confirm != in.Name {
		httpErr(w, 400, "type the repository name to confirm")
		return
	}
	l := s.lookups()
	a := l.agents[r.PathValue("id")]
	if a == nil || !a.Online {
		httpErr(w, 409, "that agent is offline")
		return
	}
	if l.referencedRepos()[in.Name] {
		httpErr(w, 409, "this repository is still used by a job; delete the job (with its data) instead")
		return
	}
	run, err := s.enqueuePurge(a.ID, "", in.Name, in.Name, false)
	if err != nil {
		httpErr(w, 409, "%v", err)
		return
	}
	s.st.AddLog(store.LogEntry{Level: proto.LevelWarning, RunID: run.ID, Source: "manager", Message: "deleting unreferenced repository " + in.Name + " on " + a.Name})
	writeJSON(w, 200, map[string]string{"run_id": run.ID})
}
