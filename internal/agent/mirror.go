package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"vaultkeeper/internal/mirror"
	"vaultkeeper/internal/mount"
	"vaultkeeper/internal/proto"
)

// within reports whether child is the same as, or inside, parent.
func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func canon(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// doMirror keeps a destination folder an exact copy of a source folder.
func (a *Agent) doMirror(ctx context.Context, c *taskCtx, tmp string) proto.Result {
	spec := c.t.Mirror
	if spec == nil {
		return fail("mirror task has no configuration")
	}
	src := spec.SourcePath
	if spec.Mount != nil {
		dir := mount.Dir(a.cfg.DataDir, c.t.JobID)
		c.info("mounting %s share %s", spec.Mount.Type, spec.Mount.Remote)
		un, err := mount.Mount(*spec.Mount, dir)
		if err != nil {
			return fail("%v", err)
		}
		defer un()
		src = filepath.Join(dir, spec.SourcePath)
	}
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return fail("source folder %q was not found on this agent (is it mounted?)", src)
	}

	var target mirror.Target
	if spec.Dest.Local {
		if a.mirror == nil {
			return fail("this agent is not configured as a destination")
		}
		dst := canon(filepath.Join(a.cfg.MirrorRoot, filepath.FromSlash(spec.DestPath)))
		if within(canon(src), dst) || within(dst, canon(src)) {
			return fail("source and destination overlap (%s ↔ %s); a mirror can't contain itself", src, dst)
		}
		target = &mirror.FSTarget{FS: a.mirror, Dest: spec.DestPath}
	} else {
		if err := reachable(proto.Repo{URL: spec.Dest.URL}); err != nil {
			return fail("destination unreachable: %v", err)
		}
		cl, err := mirror.NewClient(spec.Dest.URL, spec.Dest.CertPEM, spec.Dest.Token, spec.DestPath, spec.Workers)
		if err != nil {
			return fail("%v", err)
		}
		target = cl
	}

	if spec.PreserveOwner || spec.PreserveACLs {
		f := mirror.Capabilities()
		if spec.PreserveOwner && !f.Owner {
			c.logf(proto.LevelWarning, "ownership can't be read on this (source) platform; it will not be preserved")
		}
		if spec.PreserveACLs && !f.XAttr {
			c.logf(proto.LevelWarning, "ACLs/extended attributes can't be read on this (source) platform; they will not be preserved")
		}
		if spec.PreserveOwner && spec.Dest.Local && os.Geteuid() != 0 {
			c.logf(proto.LevelWarning, "this agent is not running as root, so it can't change file ownership on the destination")
		}
		c.info("preserving: %s%s%s (%s)", map[bool]string{true: "ownership", false: ""}[spec.PreserveOwner],
			map[bool]string{true: " + ", false: ""}[spec.PreserveOwner && spec.PreserveACLs], map[bool]string{true: "ACLs/extended attributes", false: ""}[spec.PreserveACLs], f.Note)
	}
	mode := "mirror"
	if spec.DryRun {
		mode = "PREVIEW (no changes will be made)"
	}
	c.info("%s: %s → %s (compare: %s, %d parallel, deletes %s)", mode, src, spec.DestPath, spec.Compare, spec.Workers, map[bool]string{true: "propagated", false: "off"}[spec.PropagateDeletes])

	r, err := mirror.Sync(ctx, target, mirror.Options{
		Src: src, Excludes: spec.Excludes, Compare: spec.Compare, Workers: spec.Workers,
		PropagateDeletes: spec.PropagateDeletes, MaxDeletePct: spec.MaxDeletePct, ForceDelete: spec.ForceDelete,
		DryRun: spec.DryRun, LimitKB: spec.BandwidthKB, TmpDir: tmp, Log: c.logf,
		PreserveOwner: spec.PreserveOwner, OwnerByName: spec.OwnerByName, PreserveACLs: spec.PreserveACLs,
	})
	sum := map[string]any{}
	if r != nil {
		sum = map[string]any{
			"source_entries": r.SourceEntries, "source_bytes": r.SourceBytes, "destination_entries": r.DestEntries,
			"new_files": r.New, "changed_files": r.Changed, "unchanged_files": r.Unchanged,
			"folders_created": r.DirsCreated, "symlinks_synced": r.Links, "metadata_fixed": r.MetaFixed,
			"files_copied": r.FilesCopied, "bytes_copied": r.BytesCopied,
			"resumed_files": r.ResumedFiles, "resumed_bytes": r.ResumedBytes,
			"extra_on_destination": r.ToDelete + r.KeptExtra, "deleted": r.Deleted,
			"errors": r.Errors, "source_read_errors": r.SourceErrors,
			"vanished_during_run": r.Vanished, "changed_during_copy": r.ChangedDuring, "metadata_warnings": r.MetaWarnings,
		}
		if spec.DryRun {
			sum["dry_run"] = true
			sum["bytes_to_copy"] = r.BytesPlanned
			sum["would_delete"] = r.ToDelete
		}
	}
	if err != nil {
		return proto.Result{Status: proto.StatusFailed, Summary: sum, Message: err.Error()}
	}
	switch {
	case r.DeleteBlocked != "" && !spec.DryRun:
		return proto.Result{Status: proto.StatusFailed, Summary: sum, Message: r.DeleteBlocked}
	case r.DeleteBlocked != "":
		return proto.Result{Status: proto.StatusWarning, Summary: sum, Message: "preview: " + r.DeleteBlocked}
	case r.MetaWarnings > 0:
		return proto.Result{Status: proto.StatusWarning, Summary: sum, Message: fmt.Sprintf("files were copied, but ownership/ACLs could not be applied on %d item(s) — the destination agent usually needs to run as root", r.MetaWarnings)}
	case r.Errors > 0:
		return proto.Result{Status: proto.StatusWarning, Summary: sum, Message: fmt.Sprintf("%d item(s) could not be synced; the next run will retry them", r.Errors)}
	}
	if spec.DryRun {
		c.info("preview: %d new, %d changed, %d to delete (%d bytes to copy)", r.New, r.Changed, r.ToDelete, r.BytesPlanned)
	} else {
		c.info("mirror in sync: %d copied (%d bytes), %d deleted, %d unchanged", r.FilesCopied, r.BytesCopied, r.Deleted, r.Unchanged)
	}
	return proto.Result{Status: proto.StatusSuccess, Summary: sum}
}
