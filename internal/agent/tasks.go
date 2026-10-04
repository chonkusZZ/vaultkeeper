package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vaultkeeper/internal/mount"
	"vaultkeeper/internal/proto"
	"vaultkeeper/internal/restic"
)

// taskCtx collects logs for one running task and streams them to the manager.
type taskCtx struct {
	a        *Agent
	t        proto.Task
	cancel   context.CancelFunc
	mu       sync.Mutex
	buf      []proto.LogLine
	lastWarn string // most recent warning-level line (restic stderr), used to explain failures
}

func (c *taskCtx) logf(level, msg string) {
	c.mu.Lock()
	c.buf = append(c.buf, proto.LogLine{Time: time.Now().UTC(), Level: level, Message: msg})
	if level == proto.LevelWarning {
		c.lastWarn = msg
	}
	c.mu.Unlock()
}

func (c *taskCtx) info(f string, a ...any) { c.logf(proto.LevelInfo, fmt.Sprintf(f, a...)) }

func (c *taskCtx) flush() {
	c.mu.Lock()
	lines := c.buf
	c.buf = nil
	c.mu.Unlock()
	var resp struct {
		Cancel bool `json:"cancel"`
	}
	if err := c.a.call("POST", "/api/agent/tasks/"+c.t.RunID+"/log", proto.LogBatch{Lines: lines}, &resp, true); err != nil {
		// Put lines back so they're retried next flush.
		c.mu.Lock()
		c.buf = append(lines, c.buf...)
		c.mu.Unlock()
		return
	}
	if resp.Cancel {
		c.cancel()
	}
}

func (a *Agent) execute(parent context.Context, t proto.Task) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	c := &taskCtx{a: a, t: t, cancel: cancel}
	stop := make(chan struct{})
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		tk := time.NewTicker(2 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-tk.C:
				c.flush()
			case <-stop:
				return
			}
		}
	}()

	var res proto.Result
	func() {
		defer func() {
			if r := recover(); r != nil {
				res = proto.Result{Status: proto.StatusFailed, Message: fmt.Sprint("agent panic: ", r)}
			}
		}()
		if a.bin == "" && t.Kind != proto.KindMirror && t.Kind != proto.KindPurge {
			if err := a.ensureRestic(); err != nil {
				res = proto.Result{Status: proto.StatusFailed, Message: err.Error()}
				return
			}
		}
		tmp, _ := os.MkdirTemp(a.cfg.DataDir, "task-")
		defer os.RemoveAll(tmp)
		switch t.Kind {
		case proto.KindBackup:
			res = a.doBackup(ctx, c, tmp)
		case proto.KindTest:
			res = a.doTest(ctx, c, tmp)
		case proto.KindCopy:
			res = a.doCopy(ctx, c, tmp)
		case proto.KindSnapshots:
			res = a.doSnapshots(ctx, c, tmp)
		case proto.KindMirror:
			res = a.doMirror(ctx, c, tmp)
		case proto.KindForget:
			res = a.doForget(ctx, c, tmp)
		case proto.KindPurge:
			res = a.doPurge(ctx, c, tmp)
		case proto.KindPrune:
			res = a.doPrune(ctx, c, tmp)
		case proto.KindLs:
			res = a.doLs(ctx, c, tmp)
		case proto.KindFind:
			res = a.doFind(ctx, c, tmp)
		case proto.KindDump:
			res = a.doDump(ctx, c, tmp)
		case proto.KindRestore:
			res = a.doRestore(ctx, c, tmp)
		default:
			res = proto.Result{Status: proto.StatusFailed, Message: "unknown task kind " + t.Kind}
		}
	}()
	if ctx.Err() != nil {
		res = proto.Result{Status: proto.StatusFailed, Message: "cancelled by operator"}
	}
	if res.Status == proto.StatusFailed && c.lastWarn != "" && !strings.Contains(res.Message, c.lastWarn) && res.Message != "cancelled by operator" {
		res.Message += " — " + c.lastWarn
	}
	if res.Status == proto.StatusFailed && res.Message != "" {
		c.logf(proto.LevelError, res.Message)
	}
	close(stop)
	<-flushed
	c.flush()
	for i := 0; i < 5; i++ {
		if err := a.call("POST", "/api/agent/tasks/"+t.RunID+"/result", res, nil, true); err == nil {
			return
		}
		time.Sleep(3 * time.Second)
	}
}

func fail(format string, a ...any) proto.Result {
	return proto.Result{Status: proto.StatusFailed, Message: fmt.Sprintf(format, a...)}
}

func jobTag(id string) string { return "bm-job-" + id }

func (a *Agent) doBackup(ctx context.Context, c *taskCtx, tmp string) proto.Result {
	t := c.t
	r := restic.NewRunner(a.bin, t.Repo, tmp, c.logf)
	defer r.Close()
	r.LimitKB = t.BandwidthKB

	paths := t.Paths
	if t.Mount != nil {
		dir := mount.Dir(a.cfg.DataDir, t.JobID)
		c.info("mounting %s share %s", t.Mount.Type, t.Mount.Remote)
		un, err := mount.Mount(*t.Mount, dir)
		if err != nil {
			return fail("%v", err)
		}
		defer un()
		if dir != "" && !isUNC(paths) {
			paths = nil
			for _, p := range t.Paths {
				paths = append(paths, filepath.Join(dir, p))
			}
		}
	}

	if err := reachable(t.Repo); err != nil {
		return fail("destination unreachable: %v", err)
	}
	if !r.RepoExists(ctx) {
		c.info("repository not initialised; creating (encrypted)")
		if out, code, err := r.Output(ctx, "init"); err != nil || code != 0 {
			return fail("repository init failed (exit %d): %s %v", code, strings.TrimSpace(string(out)), err)
		}
	} else {
		r.UnlockStale(ctx)
	}

	args := []string{"backup", "--json", "--tag", jobTag(t.JobID)}
	if t.Compression != "" {
		args = append(args, "--compression", t.Compression)
	}
	for _, e := range t.Excludes {
		args = append(args, "--exclude", e)
	}
	args = append(args, paths...)
	c.info("backing up %s", strings.Join(paths, ", "))

	summary := map[string]any{}
	var lastStatus time.Time
	code, err := r.Run(ctx, func(line string) {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			return
		}
		mt := m["message_type"]
		if mt == nil {
			mt = m["type"] // restic < 0.16
		}
		switch mt {
		case "status":
			if time.Since(lastStatus) > 20*time.Second {
				lastStatus = time.Now()
				c.info("progress: %.0f%% (%v/%v files)", num(m["percent_done"])*100, m["files_done"], m["total_files"])
			}
		case "error":
			msg := line
			if e, ok := m["error"].(map[string]any); ok {
				msg = fmt.Sprint(e["message"])
			}
			c.logf(proto.LevelWarning, fmt.Sprintf("%v: %s", m["item"], msg))
		case "summary":
			summary["snapshot_id"] = m["snapshot_id"]
			summary["files_new"] = m["files_new"]
			summary["files_changed"] = m["files_changed"]
			summary["files_unmodified"] = m["files_unmodified"]
			summary["data_added"] = m["data_added"]
			summary["total_files"] = m["total_files_processed"]
			summary["total_bytes"] = m["total_bytes_processed"]
			summary["duration_s"] = m["total_duration"]
		}
	}, args...)
	if err != nil {
		return fail("restic: %v", err)
	}
	res := proto.Result{Status: proto.StatusSuccess, Summary: summary}
	switch code {
	case 0:
	case 3:
		res.Status = proto.StatusWarning
		res.Message = "backup completed but some files could not be read"
	default:
		return fail("restic backup exited with code %d", code)
	}
	c.info("backup complete: snapshot %v, %v bytes added", summary["snapshot_id"], summary["data_added"])

	if t.KeepLast > 0 {
		c.info("applying retention: keep last %d restore points", t.KeepLast)
		_, code, err := r.Output(ctx, "forget", "--tag", jobTag(t.JobID), "--group-by", "tags", "--keep-last", fmt.Sprint(t.KeepLast), "--prune")
		if err != nil || code != 0 {
			res.Status = proto.StatusWarning
			res.Message = strings.TrimSpace(res.Message + " retention/prune failed")
			c.logf(proto.LevelWarning, fmt.Sprintf("retention prune failed (exit %d)", code))
		}
	}

	if t.PickTest {
		f, err := a.pickRandomFile(ctx, r, t.JobID)
		if err != nil {
			c.logf(proto.LevelWarning, "could not choose a test file: "+err.Error())
			if res.Status == proto.StatusSuccess {
				res.Status = proto.StatusWarning
			}
		} else {
			res.TestFile = f
			c.info("selected random backup-test file: %s", f)
		}
	}
	return res
}

func isUNC(paths []string) bool {
	return len(paths) > 0 && strings.HasPrefix(paths[0], `\\`)
}

func num(v any) float64 { f, _ := v.(float64); return f }

type lsNode struct {
	StructType string `json:"struct_type"`
	Type       string `json:"type"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
}

// pickRandomFile reservoir-samples a non-empty regular file from the latest snapshot.
func (a *Agent) pickRandomFile(ctx context.Context, r *restic.Runner, jobID string) (string, error) {
	var pick string
	n := 0
	code, err := r.Run(ctx, func(line string) {
		var nd lsNode
		if json.Unmarshal([]byte(line), &nd) != nil || nd.Type != "file" || nd.Size == 0 {
			return
		}
		n++
		if rand.Intn(n) == 0 {
			pick = nd.Path
		}
	}, "ls", "latest", "--json", "--tag", jobTag(jobID))
	if err != nil || code != 0 {
		return "", fmt.Errorf("restic ls failed (exit %d) %v", code, err)
	}
	if pick == "" {
		return "", fmt.Errorf("snapshot contains no non-empty files")
	}
	return pick, nil
}

func (a *Agent) doTest(ctx context.Context, c *taskCtx, tmp string) proto.Result {
	t := c.t
	start := time.Now()
	r := restic.NewRunner(a.bin, t.Repo, tmp, c.logf)
	defer r.Close()
	summary := map[string]any{}
	res := proto.Result{Status: proto.StatusSuccess, Summary: summary}

	if !r.RepoExists(ctx) {
		return fail("repository not found or cannot be opened (wrong password?)")
	}
	r.UnlockStale(ctx)
	tag := jobTag(t.JobID)

	// Locate the test file in the latest snapshot.
	testPath := t.TestPath
	findNode := func(p string) (*lsNode, error) {
		var found *lsNode
		code, err := r.Run(ctx, func(line string) {
			var nd lsNode
			if json.Unmarshal([]byte(line), &nd) == nil && nd.Type == "file" && nd.Path == p {
				found = &nd
			}
		}, "ls", "latest", "--json", "--tag", tag, p)
		if err != nil || code != 0 {
			return nil, fmt.Errorf("restic ls failed (exit %d) %v", code, err)
		}
		return found, nil
	}
	var node *lsNode
	if testPath != "" {
		n, err := findNode(testPath)
		if err != nil {
			return fail("%v", err)
		}
		node = n
	}
	if node == nil {
		if !t.TestRandom {
			return fail("test file %q was not found in the latest restore point", testPath)
		}
		f, err := a.pickRandomFile(ctx, r, t.JobID)
		if err != nil {
			return fail("test file missing and no replacement could be chosen: %v", err)
		}
		if testPath != "" {
			c.logf(proto.LevelWarning, fmt.Sprintf("previous test file %q no longer exists in the latest snapshot; choosing a new random file", testPath))
			res.Status = proto.StatusWarning
		}
		testPath = f
		res.TestFile = f
		if node, err = findNode(f); err != nil || node == nil {
			return fail("could not locate replacement test file")
		}
	}
	c.info("restoring test file %s (%d bytes) from latest restore point", testPath, node.Size)

	target := filepath.Join(tmp, "restore")
	code, err := r.Run(ctx, func(s string) {}, "restore", "latest", "--tag", tag, "--include", testPath, "--target", target, "--verify")
	if err != nil || code != 0 {
		return fail("restore failed (exit %d) %v", code, err)
	}
	// Find the restored file irrespective of OS path mapping.
	var restored string
	_ = filepath.Walk(target, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() && restored == "" {
			restored = p
		}
		return nil
	})
	if restored == "" {
		return fail("restore reported success but no file was produced")
	}
	f, err := os.Open(restored)
	if err != nil {
		return fail("%v", err)
	}
	h := sha256.New()
	n, _ := io.Copy(h, f)
	f.Close()
	if n != node.Size {
		return fail("restored size %d does not match snapshot size %d", n, node.Size)
	}
	summary["file"] = testPath
	summary["size"] = n
	summary["sha256"] = hex.EncodeToString(h.Sum(nil))
	c.info("restore verified: %d bytes, sha256 %s", n, summary["sha256"])

	// Chain consistency.
	checkArgs := []string{"check"}
	if t.CheckDataPct > 0 {
		checkArgs = append(checkArgs, fmt.Sprintf("--read-data-subset=%d%%", t.CheckDataPct))
		c.info("running repository check (verifying %d%% of pack data)", t.CheckDataPct)
	} else {
		c.info("running repository check (structure/index)")
	}
	out, code, err := r.Output(ctx, checkArgs...)
	if err != nil || code != 0 {
		return fail("repository check failed (exit %d): %s", code, strings.TrimSpace(string(out)))
	}
	summary["check"] = "ok"

	var snaps []proto.Snapshot
	if out, code, _ := r.Output(ctx, "snapshots", "--json", "--tag", tag); code == 0 {
		_ = json.Unmarshal(out, &snaps)
	}
	summary["snapshots"] = len(snaps)
	summary["duration_s"] = time.Since(start).Seconds()
	c.info("backup test passed (%d restore points present)", len(snaps))
	return res
}

func (a *Agent) doCopy(ctx context.Context, c *taskCtx, tmp string) proto.Result {
	t := c.t
	if t.From == nil {
		return fail("copy task has no source repository")
	}
	r := restic.NewRunner(a.bin, t.Repo, tmp, c.logf)
	defer r.Close()
	r.From = t.From
	tag := jobTag(t.JobID)

	if err := reachable(t.Repo); err != nil {
		return fail("secondary destination unreachable: %v", err)
	}
	r.From = nil
	exists := r.RepoExists(ctx)
	if exists {
		r.UnlockStale(ctx)
	}
	src := restic.NewRunner(a.bin, *t.From, tmp, c.logf)
	src.UnlockStale(ctx)
	src.Close()
	r.From = t.From
	if !exists {
		c.info("secondary repository not initialised; creating with matching chunker parameters")
		out, code, err := r.Output(ctx, "init", "--copy-chunker-params")
		if err != nil || code != 0 {
			return fail("init secondary repo failed (exit %d): %s %v", code, strings.TrimSpace(string(out)), err)
		}
	}
	c.info("copying restore points to secondary repository")
	summary := map[string]any{}
	code, err := r.Run(ctx, func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			c.info("%s", s)
		}
	}, "copy", "--tag", tag)
	if err != nil || code != 0 {
		return fail("restic copy failed (exit %d) %v", code, err)
	}
	res := proto.Result{Status: proto.StatusSuccess, Summary: summary}
	r.From = nil
	if t.KeepLast > 0 {
		c.info("applying retention on copy: keep last %d", t.KeepLast)
		_, code, err := r.Output(ctx, "forget", "--tag", tag, "--group-by", "tags", "--keep-last", fmt.Sprint(t.KeepLast), "--prune")
		if err != nil || code != 0 {
			res.Status = proto.StatusWarning
			res.Message = "copy ok but retention prune failed"
		}
	}
	var snaps []proto.Snapshot
	if out, code, _ := r.Output(ctx, "snapshots", "--json", "--tag", tag); code == 0 {
		_ = json.Unmarshal(out, &snaps)
	}
	summary["snapshots"] = len(snaps)
	c.info("copy complete: secondary holds %d restore points", len(snaps))
	return res
}

func (a *Agent) doSnapshots(ctx context.Context, c *taskCtx, tmp string) proto.Result {
	r := restic.NewRunner(a.bin, c.t.Repo, tmp, c.logf)
	defer r.Close()
	out, code, err := r.Output(ctx, "snapshots", "--json", "--tag", jobTag(c.t.JobID))
	if err != nil || code != 0 {
		return fail("restic snapshots failed (exit %d) %v", code, err)
	}
	var snaps []proto.Snapshot
	_ = json.Unmarshal(out, &snaps)
	return proto.Result{Status: proto.StatusSuccess, Summary: map[string]any{"snapshots": snaps}}
}

// reachable fails fast if a remote destination agent can't be contacted,
// rather than letting restic retry for many minutes.
func reachable(repo proto.Repo) error {
	if repo.URL == "" {
		return nil
	}
	u, err := url.Parse(repo.URL)
	if err != nil {
		return err
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	conn, err := net.DialTimeout("tcp", host, 8*time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}

// doPrune applies the restore-point limit on demand and reclaims the space.
func (a *Agent) doPrune(ctx context.Context, c *taskCtx, tmp string) proto.Result {
	t := c.t
	if t.KeepLast <= 0 {
		return fail("no restore-point limit is set; nothing to prune")
	}
	r := restic.NewRunner(a.bin, t.Repo, tmp, c.logf)
	defer r.Close()
	r.UnlockStale(ctx)
	count := func() int {
		var snaps []proto.Snapshot
		if out, code, _ := r.Output(ctx, "snapshots", "--json", "--tag", jobTag(t.JobID)); code == 0 {
			_ = json.Unmarshal(out, &snaps)
			return len(snaps)
		}
		return -1
	}
	before := count()
	c.info("pruning: keeping the newest %d restore points (currently %d)", t.KeepLast, before)
	out, code, err := r.Output(ctx, "forget", "--tag", jobTag(t.JobID), "--group-by", "tags", "--keep-last", fmt.Sprint(t.KeepLast), "--prune")
	if err != nil || code != 0 {
		return fail("prune failed (exit %d): %s %v", code, lastLine(string(out)), err)
	}
	after := count()
	removed := 0
	if before >= 0 && after >= 0 {
		removed = before - after
	}
	c.info("prune complete: %d restore point(s) removed, %d remain", removed, after)
	return proto.Result{Status: proto.StatusSuccess, Summary: map[string]any{"snapshots_before": before, "snapshots_after": after, "removed": removed}}
}
