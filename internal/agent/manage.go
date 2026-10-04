package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"vaultkeeper/internal/proto"
	"vaultkeeper/internal/restic"
)

var repoDirRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// doForget deletes one restore point and reclaims its space. It only ever
// deletes a snapshot that belongs to this job (matched by tag).
func (a *Agent) doForget(ctx context.Context, c *taskCtx, tmp string) proto.Result {
	e := c.t.Explore
	r := restic.NewRunner(a.bin, c.t.Repo, tmp, c.logf)
	defer r.Close()
	r.UnlockStale(ctx)
	list := func() ([]proto.Snapshot, error) {
		out, code, err := r.Output(ctx, "snapshots", "--json", "--tag", jobTag(c.t.JobID))
		if err != nil || code != 0 {
			return nil, fmt.Errorf("could not list restore points (exit %d) %v", code, err)
		}
		var snaps []proto.Snapshot
		return snaps, json.Unmarshal(out, &snaps)
	}
	snaps, err := list()
	if err != nil {
		return fail("%v", err)
	}
	var match []proto.Snapshot
	for _, s := range snaps {
		if strings.HasPrefix(s.ID, e.Snapshot) {
			match = append(match, s)
		}
	}
	if len(match) != 1 {
		return fail("restore point %s was not found in this job (or the id is ambiguous)", e.Snapshot)
	}
	id := match[0].ID
	c.logf(proto.LevelWarning, fmt.Sprintf("deleting restore point %s taken %s", id[:8], match[0].Time.Format("2006-01-02 15:04")))
	out, code, err := r.Output(ctx, "forget", id, "--prune")
	if err != nil || code != 0 {
		return fail("forget failed (exit %d): %s %v", code, lastLine(string(out)), err)
	}
	remaining := -1
	if after, err := list(); err == nil {
		remaining = len(after)
	}
	c.info("restore point deleted and its unused data pruned; %d restore point(s) remain", remaining)
	return proto.Result{Status: proto.StatusSuccess, Summary: map[string]any{"deleted_snapshot": id[:8], "remaining": remaining}}
}

// doPurge permanently removes stored data: a restic repository directory or a
// mirror destination folder.
func (a *Agent) doPurge(ctx context.Context, c *taskCtx, tmp string) proto.Result {
	e := c.t.Explore
	if e == nil || e.Path == "" {
		return fail("nothing to remove")
	}
	if e.Mirror {
		if a.mirror == nil {
			return fail("this agent does not store mirrors")
		}
		c.logf(proto.LevelWarning, fmt.Sprintf("permanently deleting mirror folder %s under %s", e.Path, a.cfg.MirrorRoot))
		if err := a.mirror.PurgeDest(e.Path); err != nil {
			return fail("%v", err)
		}
		c.info("mirror folder %s deleted", e.Path)
		return proto.Result{Status: proto.StatusSuccess, Summary: map[string]any{"removed": e.Path}}
	}
	if !repoDirRe.MatchString(e.Path) {
		return fail("invalid repository name %q", e.Path)
	}
	dir := filepath.Join(a.cfg.DataDir, "repos", e.Path)
	fi, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		c.info("repository %s is already gone", e.Path)
		return proto.Result{Status: proto.StatusSuccess, Summary: map[string]any{"removed": e.Path}}
	}
	if err != nil || !fi.IsDir() {
		return fail("%s is not a repository directory", dir)
	}
	c.logf(proto.LevelWarning, fmt.Sprintf("permanently deleting repository %s", e.Path))
	if err := os.RemoveAll(dir); err != nil {
		return fail("%v", err)
	}
	c.info("repository %s deleted", e.Path)
	return proto.Result{Status: proto.StatusSuccess, Summary: map[string]any{"removed": e.Path}}
}
