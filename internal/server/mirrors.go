package server

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"vaultkeeper/internal/mirror"
	"vaultkeeper/internal/proto"
	"vaultkeeper/internal/store"
)

type mirrorView struct {
	store.MirrorJob
	SourceName   string     `json:"source_name"`
	DestName     string     `json:"dest_name"`
	SourceOnline bool       `json:"source_online"`
	DestOnline   bool       `json:"dest_online"`
	DestFolder   string     `json:"dest_folder"` // full path on the destination agent
	LastRun      *store.Run `json:"last_run"`
	History      []string   `json:"history"`
	NextRun      int64      `json:"next_run"`
	Health       string     `json:"health"`
}

func (s *Server) mirrorView(l *lookups, m *store.MirrorJob) mirrorView {
	v := mirrorView{MirrorJob: *m, SourceName: l.agentName(m.SourceAgent), DestName: l.agentName(m.DestAgent)}
	if a := l.agents[m.SourceAgent]; a != nil {
		v.SourceOnline = a.Online
	}
	if a := l.agents[m.DestAgent]; a != nil {
		v.DestOnline = a.Online
		root := strings.TrimRight(a.MirrorRoot, `/\`)
		sep := "/"
		if strings.Contains(root, `\`) {
			sep = `\`
		}
		v.DestFolder = root + sep + strings.ReplaceAll(m.DestPath, "/", sep)
	}
	v.LastRun = s.st.LastSyncRun(m.ID)
	v.History = s.st.RecentSyncStatuses(m.ID, 14)
	if m.Enabled {
		v.NextRun = s.nextRun("mirror:" + m.ID)
	}
	v.Health = health(v.LastRun)
	return v
}

func (s *Server) apiMirrors(w http.ResponseWriter, r *http.Request) {
	l := s.lookups()
	out := []mirrorView{}
	for _, m := range l.mirrors {
		out = append(out, s.mirrorView(l, m))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, 200, out)
}

func normDest(p string) string {
	return strings.Trim(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"), "/")
}

func (s *Server) apiMirrorSave(w http.ResponseWriter, r *http.Request) {
	var in store.MirrorJob
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, "bad request: %v", err)
		return
	}
	l := s.lookups()
	id := r.PathValue("id")
	var old *store.MirrorJob
	if id != "" {
		if old = l.mirrors[id]; old == nil {
			httpErr(w, 404, "no such mirror job")
			return
		}
	}
	in.Name = strings.TrimSpace(in.Name)
	in.SourcePath = strings.TrimSpace(in.SourcePath)
	in.DestPath = normDest(in.DestPath)
	in.Excludes = cleanList(in.Excludes)
	if in.Mount != nil && in.Mount.Remote == "" {
		in.Mount = nil
	}
	if in.Compare == "" {
		in.Compare = "mtime"
	}
	if in.Workers == 0 {
		in.Workers = 4
	}
	if in.OwnerMap == "" {
		in.OwnerMap = "numeric"
	}
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
	case in.SourcePath == "":
		httpErr(w, 400, "the source folder is required")
		return
	case in.Mount == nil && !(strings.HasPrefix(in.SourcePath, "/") || strings.HasPrefix(in.SourcePath, `\\`) || (len(in.SourcePath) > 2 && in.SourcePath[1] == ':')):
		httpErr(w, 400, "the source folder must be an absolute path on the source agent")
		return
	case !mirror.ValidDest(in.DestPath):
		httpErr(w, 400, "the destination folder must be a relative path (no '..'), e.g. projects/alpha")
		return
	case in.Compare != "mtime" && in.Compare != "checksum":
		httpErr(w, 400, "compare must be mtime or checksum")
		return
	case in.Workers < 1 || in.Workers > 32:
		httpErr(w, 400, "parallel transfers must be 1-32")
		return
	case in.OwnerMap != "numeric" && in.OwnerMap != "names":
		httpErr(w, 400, "owner mapping must be numeric or names")
		return
	case in.MaxDeletePct < 0 || in.MaxDeletePct > 100 || in.BandwidthKB < 0:
		httpErr(w, 400, "invalid safety limit or bandwidth")
		return
	}
	if err := validCron(in.Schedule); err != nil {
		httpErr(w, 400, "schedule: %v", err)
		return
	}
	// Two mirrors must never write into each other's folders.
	for _, o := range l.mirrors {
		if o.DestAgent != in.DestAgent || (old != nil && o.ID == old.ID) {
			continue
		}
		if o.DestPath == in.DestPath || strings.HasPrefix(o.DestPath, in.DestPath+"/") || strings.HasPrefix(in.DestPath, o.DestPath+"/") {
			httpErr(w, 409, "destination folder overlaps mirror job %q (%s)", o.Name, o.DestPath)
			return
		}
	}
	if old == nil {
		in.ID, in.Created = rid(6), time.Now().Unix()
	} else {
		in.ID, in.Created = old.ID, old.Created
		if in.Mount != nil && in.Mount.Password == "" && old.Mount != nil {
			in.Mount.Password = old.Mount.Password
		}
	}
	if err := s.st.Put(store.KindMirrorJob, in.ID, in); err != nil {
		httpErr(w, 500, "%v", err)
		return
	}
	s.reloadSchedules()
	writeJSON(w, 200, map[string]string{"id": in.ID})
}

func (s *Server) apiMirrorDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var m store.MirrorJob
	if s.st.Get(store.KindMirrorJob, id, &m) != nil {
		httpErr(w, 404, "no such mirror job")
		return
	}
	runID, ok := s.deleteWithData(w, r, id, m.Name, m.DestAgent, m.DestPath, true)
	if !ok {
		return
	}
	_ = s.st.Delete(store.KindMirrorJob, id)
	s.reloadSchedules()
	msg := "mirror job deleted (the mirrored files were left in place)"
	if runID != "" {
		msg = "mirror job deleted; deleting the mirrored files from the destination"
	}
	s.st.AddLog(store.LogEntry{Level: proto.LevelWarning, Source: "manager", Message: m.Name + ": " + msg})
	writeJSON(w, 200, map[string]string{"ok": "true", "run_id": runID})
}

func (s *Server) apiMirrorRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DryRun      bool `json:"dry_run"`
		ForceDelete bool `json:"force_delete"`
	}
	_ = readJSON(r, &in)
	var m store.MirrorJob
	if s.st.Get(store.KindMirrorJob, r.PathValue("id"), &m) != nil {
		httpErr(w, 404, "no such mirror job")
		return
	}
	trigger := "manual"
	if in.DryRun {
		trigger = "manual preview"
	} else if in.ForceDelete {
		trigger = "manual (large deletions allowed)"
	}
	run, err := s.enqueueMirror(&m, trigger, proto.Explore{DryRun: in.DryRun, Force: in.ForceDelete && !in.DryRun})
	if err != nil {
		httpErr(w, 409, "%v", err)
		return
	}
	if in.ForceDelete && !in.DryRun {
		s.st.AddLog(store.LogEntry{Level: proto.LevelWarning, RunID: run.ID, JobID: m.ID, Source: "manager", Message: "run started with the large-deletion safety guard bypassed"})
	}
	writeJSON(w, 200, map[string]string{"run_id": run.ID, "status": run.Status})
}
