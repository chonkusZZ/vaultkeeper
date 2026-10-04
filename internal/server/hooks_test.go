package server

import (
	"strings"
	"testing"
	"time"

	"vaultkeeper/internal/proto"
	"vaultkeeper/internal/store"
)

func putAgent(t *testing.T, s *Server, id string, roles []string, online, scripts bool) {
	a := store.Agent{ID: id, Name: id, Roles: roles, AllowScripts: scripts, DataDir: "/d"}
	if online {
		a.LastSeen = time.Now().Unix()
	}
	if err := s.st.Put(store.KindAgent, id, a); err != nil {
		t.Fatal(err)
	}
}

func wakeHooks(agent, mac string) *store.Hooks {
	return &store.Hooks{
		Wake: &store.Wake{Enabled: true, Agent: agent, WakeSpec: proto.WakeSpec{Method: "wol", MAC: mac, Ready: "tcp", Host: "nas.lan", Port: 445, TimeoutMin: 5}},
		Post: &store.Post{Enabled: true, Agent: agent, Script: "ssh nas poweroff", On: "success", TimeoutSec: 60},
	}
}

func TestValidateHooks(t *testing.T) {
	s := testServer(t)
	putAgent(t, s, "ok", []string{"source", "dest"}, true, true)
	putAgent(t, s, "noscripts", []string{"dest"}, true, false)
	l := s.lookups()

	h, err := s.validateHooks(l, wakeHooks("ok", "AA-BB-CC-DD-EE-FF"), "ok")
	if err != nil {
		t.Fatal(err)
	}
	if h.Wake.MAC != "aa:bb:cc:dd:ee:ff" || h.Wake.TimeoutMin != 5 || h.Post.TimeoutSec != 60 {
		t.Fatalf("not normalised: %+v", h.Wake)
	}
	// Defaults are applied.
	h2, err := s.validateHooks(l, &store.Hooks{Wake: &store.Wake{Enabled: true, WakeSpec: proto.WakeSpec{Method: "wol", MAC: "aabbccddeeff", Ready: "browse"}}}, "ok")
	if err == nil {
		t.Fatalf("browse without a path must be rejected: %+v", h2)
	}
	h3, err := s.validateHooks(l, &store.Hooks{Wake: &store.Wake{Enabled: true, WakeSpec: proto.WakeSpec{Method: "wol", MAC: "aabbccddeeff", Ready: "browse", Path: "/mnt/nas"}}}, "ok")
	if err != nil || h3.Wake.Agent != "ok" || h3.Wake.TimeoutMin != 10 {
		t.Fatalf("defaults: %v %+v", err, h3)
	}

	bad := map[string]*store.Hooks{
		"bad MAC":        {Wake: &store.Wake{Enabled: true, Agent: "ok", WakeSpec: proto.WakeSpec{Method: "wol", MAC: "nope", Ready: "tcp", Host: "h", Port: 1}}},
		"bad port":       {Wake: &store.Wake{Enabled: true, Agent: "ok", WakeSpec: proto.WakeSpec{Method: "wol", MAC: "aabbccddeeff", Ready: "tcp", Host: "h", Port: 70000}}},
		"option host":    {Wake: &store.Wake{Enabled: true, Agent: "ok", WakeSpec: proto.WakeSpec{Method: "wol", MAC: "aabbccddeeff", Ready: "ping", Host: "-f"}}},
		"bad broadcast":  {Wake: &store.Wake{Enabled: true, Agent: "ok", WakeSpec: proto.WakeSpec{Method: "wol", MAC: "aabbccddeeff", Broadcast: "not-an-ip", Ready: "ping", Host: "h"}}},
		"timeout":        {Wake: &store.Wake{Enabled: true, Agent: "ok", WakeSpec: proto.WakeSpec{Method: "wol", MAC: "aabbccddeeff", Ready: "ping", Host: "h", TimeoutMin: 999}}},
		"marker path":    {Wake: &store.Wake{Enabled: true, Agent: "ok", WakeSpec: proto.WakeSpec{Method: "wol", MAC: "aabbccddeeff", Ready: "browse", Path: "/x", Marker: "../etc"}}},
		"empty script":   {Post: &store.Post{Enabled: true, Agent: "ok", Script: "  "}},
		"unknown agent":  {Post: &store.Post{Enabled: true, Agent: "ghost", Script: "x"}},
		"bad when":       {Post: &store.Post{Enabled: true, Agent: "ok", Script: "x", On: "sometimes"}},
		"script timeout": {Post: &store.Post{Enabled: true, Agent: "ok", Script: "x", TimeoutSec: 1}},
	}
	for name, hk := range bad {
		if _, err := s.validateHooks(l, hk, "ok"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Scripts need the agent's explicit opt-in.
	for name, hk := range map[string]*store.Hooks{
		"post script":   {Post: &store.Post{Enabled: true, Agent: "noscripts", Script: "poweroff"}},
		"wake command":  {Wake: &store.Wake{Enabled: true, Agent: "noscripts", WakeSpec: proto.WakeSpec{Method: "command", Command: "curl x", Ready: "ping", Host: "h"}}},
		"ready command": {Wake: &store.Wake{Enabled: true, Agent: "noscripts", WakeSpec: proto.WakeSpec{Method: "wol", MAC: "aabbccddeeff", Ready: "command", ReadyCommand: "test -d /x"}}},
	} {
		if _, err := s.validateHooks(l, hk, "ok"); err == nil || !strings.Contains(err.Error(), "--allow-scripts") {
			t.Errorf("%s must be refused for an agent without --allow-scripts: %v", name, err)
		}
	}
	// Plain Wake-on-LAN needs no script permission.
	if _, err := s.validateHooks(l, &store.Hooks{Wake: &store.Wake{Enabled: true, Agent: "noscripts", WakeSpec: proto.WakeSpec{Method: "wol", MAC: "aabbccddeeff", Ready: "ping", Host: "h"}}}, "ok"); err != nil {
		t.Fatalf("WoL alone must not require scripts: %v", err)
	}
}

func TestTargetKeyGroupsJobsOnTheSameNAS(t *testing.T) {
	a := wakeHooks("x", "AA-BB-CC-DD-EE-FF")
	b := wakeHooks("y", "aa:bb:cc:dd:ee:ff")
	if targetKey(a) == "" || targetKey(a) != targetKey(b) {
		t.Fatalf("same MAC in different formats must be the same target: %q %q", targetKey(a), targetKey(b))
	}
	c := wakeHooks("x", "11:22:33:44:55:66")
	if targetKey(a) == targetKey(c) {
		t.Fatal("different MACs are different targets")
	}
	if targetKey(&store.Hooks{Target: "Main NAS"}) != "name:main nas" || targetKey(nil) != "" {
		t.Fatal("explicit label / no hooks")
	}
}

// A run behind a wake step must stay queued even though its agents are offline
// (the NAS is asleep), must not be handed out, and must start once the wake step succeeds.
func TestWakeGateHoldsAndReleasesTheRun(t *testing.T) {
	s := testServer(t)
	putAgent(t, s, "wakeragent", []string{"source"}, true, false)
	putAgent(t, s, "naspc", []string{"dest"}, false, false) // asleep: offline
	job := store.Job{ID: "j1", Name: "Docs", SourceAgent: "wakeragent", DestAgent: "naspc", Initialized: true,
		Hooks: &store.Hooks{Wake: &store.Wake{Enabled: true, Agent: "wakeragent", WakeSpec: proto.WakeSpec{Method: "wol", MAC: "aabbccddeeff", Ready: "ping", Host: "nas", TimeoutMin: 5}}}}
	s.st.Put(store.KindJob, "j1", job)

	run, err := s.enqueue(proto.KindBackup, &job, nil, "test", false)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != proto.StatusQueued || run.WaitFor == "" {
		t.Fatalf("with the NAS asleep the run must wait, not fail: %+v", run)
	}
	// The source agent (which would run the backup) gets nothing yet.
	if got, _ := s.st.ClaimRun("wakeragent"); got == nil || got.Kind != proto.KindWake {
		t.Fatalf("the waking agent should receive the wake step first, got %+v", got)
	} else if got.Parent != run.ID || !got.Hidden {
		t.Fatalf("wake step must be a hidden child of the run: %+v", got)
	}
	if got, _ := s.st.ClaimRun("wakeragent"); got != nil {
		t.Fatalf("the gated run must not be claimable while waiting: %+v", got)
	}
	// Wake succeeds -> the run is released.
	wake, _ := s.st.GetRun(run.WaitFor)
	s.finishWake(wake, proto.StatusSuccess, "")
	got, _ := s.st.ClaimRun("wakeragent")
	if got == nil || got.ID != run.ID {
		t.Fatalf("run should start after the wake step: %+v", got)
	}
}

func TestWakeFailureFailsTheRunWithAReason(t *testing.T) {
	s := testServer(t)
	putAgent(t, s, "w", []string{"source", "dest"}, true, false)
	job := store.Job{ID: "j1", Name: "Docs", SourceAgent: "w", DestAgent: "w", Hooks: &store.Hooks{Wake: &store.Wake{Enabled: true, Agent: "w", WakeSpec: proto.WakeSpec{Method: "wol", MAC: "aabbccddeeff", Ready: "ping", Host: "nas"}}}}
	s.st.Put(store.KindJob, "j1", job)
	run, _ := s.enqueue(proto.KindBackup, &job, nil, "test", false)
	wake, _ := s.st.GetRun(run.WaitFor)
	s.finishWake(wake, proto.StatusFailed, "the target did not become ready within 5 minute(s)")
	got, _ := s.st.GetRun(run.ID)
	if got.Status != proto.StatusFailed || !strings.Contains(got.Message, "could not wake") || !strings.Contains(got.Message, "5 minute") {
		t.Fatalf("%+v", got)
	}
	if c, _ := s.st.ClaimRun("w"); c != nil && c.Kind == proto.KindBackup {
		t.Fatal("a failed wake must never let the backup run")
	}
}

func TestWakeAgentOfflineFailsFast(t *testing.T) {
	s := testServer(t)
	putAgent(t, s, "w", []string{"source"}, false, false)
	job := store.Job{ID: "j1", Name: "Docs", SourceAgent: "w", DestAgent: "w", Hooks: &store.Hooks{Wake: &store.Wake{Enabled: true, Agent: "w", WakeSpec: proto.WakeSpec{Method: "wol", MAC: "aabbccddeeff", Ready: "ping", Host: "nas"}}}}
	run, _ := s.enqueue(proto.KindBackup, &job, nil, "test", false)
	if run.Status != proto.StatusFailed || !strings.Contains(run.Message, "offline") {
		t.Fatalf("nobody can send the wake-up: %+v", run)
	}
}

func finished(t *testing.T, s *Server, id, kind, jobID, status string, started bool) *store.Run {
	r := &store.Run{ID: id, Kind: kind, JobID: jobID, AgentID: "w", Status: status, Created: time.Now().Unix()}
	if err := s.st.InsertRun(r); err != nil {
		t.Fatal(err)
	}
	if started {
		s.st.DB.Exec(`UPDATE runs SET started=? WHERE id=?`, time.Now().Unix(), id)
		r.Started = time.Now().Unix()
	}
	return r
}

func hookRuns(s *Server) []*store.Run {
	var out []*store.Run
	rs, _ := s.st.ActiveRuns()
	for _, r := range rs {
		if r.Kind == proto.KindHook {
			out = append(out, r)
		}
	}
	return out
}

func TestPostScriptRunsAfterTheLastJobOnTheTarget(t *testing.T) {
	s := testServer(t)
	putAgent(t, s, "w", []string{"source", "dest"}, true, true)
	h := func() *store.Hooks { return wakeHooks("w", "aa:bb:cc:dd:ee:ff") }
	s.st.Put(store.KindJob, "a", store.Job{ID: "a", Name: "Docs", Hooks: h()})
	s.st.Put(store.KindJob, "b", store.Job{ID: "b", Name: "Photos", Hooks: h()}) // same NAS

	// Job A finishes while job B is still running on the same NAS -> no shutdown yet.
	ra := finished(t, s, "ra", proto.KindBackup, "a", proto.StatusSuccess, true)
	rb := finished(t, s, "rb", proto.KindBackup, "b", proto.StatusRunning, true)
	s.maybePostHook(ra)
	if len(hookRuns(s)) != 0 {
		t.Fatal("the NAS must not be shut down while another job still uses it")
	}
	// Job B finishes -> now the script runs, with the right environment.
	s.st.FinishRun("rb", proto.StatusSuccess, "", nil)
	rb.Status = proto.StatusSuccess
	s.maybePostHook(rb)
	hr := hookRuns(s)
	if len(hr) != 1 || hr[0].Parent != "rb" || hr[0].AgentID != "w" || !hr[0].Hidden {
		t.Fatalf("expected one hook run for the last job: %+v", hr)
	}
	task, err := s.buildHookTask(hr[0])
	if err != nil || task.Hook == nil || task.Hook.Script != "ssh nas poweroff" || task.Hook.Env["VK_JOB_NAME"] != "Photos" || task.Hook.Env["VK_RUN_KIND"] != "backup" || !strings.HasPrefix(task.Hook.Env["VK_TARGET"], "mac:") {
		t.Fatalf("hook task: %v %+v", err, task)
	}
}

func TestPostScriptConditions(t *testing.T) {
	s := testServer(t)
	putAgent(t, s, "w", []string{"source", "dest"}, true, true)
	hk := wakeHooks("w", "aa:bb:cc:dd:ee:ff")
	s.st.Put(store.KindJob, "a", store.Job{ID: "a", Name: "Docs", Hooks: hk})

	if s.maybePostHook(finished(t, s, "r1", proto.KindBackup, "a", proto.StatusFailed, true)); len(hookRuns(s)) != 0 {
		t.Fatal("On=success must not run after a failed backup")
	}
	if s.maybePostHook(finished(t, s, "r2", proto.KindBackup, "a", proto.StatusSuccess, false)); len(hookRuns(s)) != 0 {
		t.Fatal("a run that never started (e.g. wake failed) must not trigger the script")
	}
	if s.maybePostHook(finished(t, s, "r3", proto.KindLs, "a", proto.StatusSuccess, true)); len(hookRuns(s)) != 0 {
		t.Fatal("browsing alone must not shut the NAS down")
	}
	if s.maybePostHook(finished(t, s, "r4", proto.KindBackup, "a", proto.StatusWarning, true)); len(hookRuns(s)) != 1 {
		t.Fatal("a warning still counts as completed")
	}
	// 'always' runs after failures too.
	hk.Post.On = "always"
	s.st.Put(store.KindJob, "a", store.Job{ID: "a", Name: "Docs", Hooks: hk})
	s.st.DB.Exec(`DELETE FROM runs WHERE kind='hook'`)
	s.maybePostHook(finished(t, s, "r5", proto.KindBackup, "a", proto.StatusFailed, true))
	if len(hookRuns(s)) != 1 {
		t.Fatal("On=always must run after a failure")
	}
	// Disabled script, or the script agent offline: nothing queued.
	s.st.DB.Exec(`DELETE FROM runs WHERE kind='hook'`)
	hk.Post.Enabled = false
	s.st.Put(store.KindJob, "a", store.Job{ID: "a", Name: "Docs", Hooks: hk})
	s.maybePostHook(finished(t, s, "r6", proto.KindBackup, "a", proto.StatusSuccess, true))
	hk.Post.Enabled = true
	putAgent(t, s, "w", []string{"source", "dest"}, false, true)
	s.st.Put(store.KindJob, "a", store.Job{ID: "a", Name: "Docs", Hooks: hk})
	s.maybePostHook(finished(t, s, "r7", proto.KindBackup, "a", proto.StatusSuccess, true))
	if len(hookRuns(s)) != 0 {
		t.Fatal("disabled / offline-agent scripts must not be queued")
	}
}

func TestHooksResolvedPerJobType(t *testing.T) {
	s := testServer(t)
	s.st.Put(store.KindJob, "j", store.Job{ID: "j", Name: "J", Hooks: &store.Hooks{Target: "job"}})
	s.st.Put(store.KindCopy, "c", store.CopyJob{ID: "c", Name: "C", JobID: "j", Hooks: &store.Hooks{Target: "copy"}})
	s.st.Put(store.KindMirrorJob, "m", store.MirrorJob{ID: "m", Name: "M", Hooks: &store.Hooks{Target: "mirror"}})
	cases := map[string]*store.Run{
		"job":    {Kind: proto.KindBackup, JobID: "j"},
		"copy":   {Kind: proto.KindCopy, JobID: "j", CopyID: "c"},
		"mirror": {Kind: proto.KindMirror, JobID: "m"},
	}
	for want, r := range cases {
		if h := s.hooksForRun(r); h == nil || h.Target != want {
			t.Errorf("%s: got %+v", want, h)
		}
	}
	// Browsing a copy's repository uses the copy's target, not the primary's.
	if h := s.hooksForRun(&store.Run{Kind: proto.KindLs, JobID: "j", CopyID: "c"}); h == nil || h.Target != "copy" {
		t.Fatalf("explorer on a copy repo: %+v", h)
	}
}
