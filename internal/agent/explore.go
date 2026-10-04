package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"runtime"
	"strings"
	"time"

	"vaultkeeper/internal/proto"
	"vaultkeeper/internal/restic"
)

const maxEntries = 5000

type entry struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Size  int64  `json:"size"`
	MTime string `json:"mtime"`
	Path  string `json:"path"`
}

func snapPath(p string) string { return strings.ReplaceAll(p, `\`, "/") }

// doLs lists the immediate children of a directory inside a restore point.
func (a *Agent) doLs(ctx context.Context, c *taskCtx, tmp string) proto.Result {
	e := c.t.Explore
	r := restic.NewRunner(a.bin, c.t.Repo, tmp, c.logf)
	defer r.Close()
	dir := path.Clean(snapPath(e.Path))
	entries := []entry{}
	truncated := false
	code, err := r.Run(ctx, func(line string) {
		var n entry
		if json.Unmarshal([]byte(line), &n) != nil || n.Path == "" {
			return // snapshot header or noise
		}
		// restic echoes the directory itself and may include deeper levels.
		if n.Path == dir || path.Dir(n.Path) != dir {
			return
		}
		if len(entries) >= maxEntries {
			truncated = true
			return
		}
		entries = append(entries, n)
	}, "ls", "--json", e.Snapshot, dir)
	if err != nil || code != 0 {
		return fail("could not read restore point (exit %d) %v", code, err)
	}
	return proto.Result{Status: proto.StatusSuccess, Summary: map[string]any{"entries": entries, "truncated": truncated, "path": dir}}
}

// doFind searches a restore point for file/folder names.
func (a *Agent) doFind(ctx context.Context, c *taskCtx, tmp string) proto.Result {
	e := c.t.Explore
	r := restic.NewRunner(a.bin, c.t.Repo, tmp, c.logf)
	defer r.Close()
	pat := e.Query
	if !strings.ContainsAny(pat, "*?[") {
		pat = "*" + pat + "*"
	}
	out, code, err := r.Output(ctx, "find", "--json", "--ignore-case", "-s", e.Snapshot, pat)
	if err != nil || code != 0 {
		return fail("search failed (exit %d) %v", code, err)
	}
	var groups []struct {
		Matches []struct {
			Path  string `json:"path"`
			Type  string `json:"type"`
			Size  int64  `json:"size"`
			MTime string `json:"mtime"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &groups); err != nil {
		return fail("unexpected search output: %v", err)
	}
	entries := []entry{}
	truncated := false
	for _, g := range groups {
		for _, m := range g.Matches {
			if len(entries) >= 500 {
				truncated = true
				break
			}
			entries = append(entries, entry{Name: path.Base(m.Path), Type: m.Type, Size: m.Size, MTime: m.MTime, Path: m.Path})
		}
	}
	return proto.Result{Status: proto.StatusSuccess, Summary: map[string]any{"entries": entries, "truncated": truncated}}
}

// waitReader turns a failed restic exit into a read error, so a failed dump
// aborts the upload instead of looking like a (truncated) success.
type waitReader struct {
	r    io.Reader
	wait func() error
	done bool
}

func (w *waitReader) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if err == io.EOF && !w.done {
		w.done = true
		if werr := w.wait(); werr != nil {
			return n, werr
		}
	}
	return n, err
}

// doDump streams a file (raw) or folder (zip) to the manager for browser download.
func (a *Agent) doDump(ctx context.Context, c *taskCtx, tmp string) proto.Result {
	e := c.t.Explore
	r := restic.NewRunner(a.bin, c.t.Repo, tmp, c.logf)
	defer r.Close()
	args := []string{"dump"}
	if e.IsDir {
		args = append(args, "-a", "zip")
	}
	args = append(args, e.Snapshot, e.Path)
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd, err := r.Cmd(sctx, args...)
	if err != nil {
		return fail("%v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return fail("%v", err)
	}
	c.info("streaming %s from restore point %s", e.Path, e.Snapshot)
	body := &waitReader{r: stdout, wait: func() error {
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("restic dump failed: %v: %s", err, lastLine(stderr.String()))
		}
		return nil
	}}
	req, _ := http.NewRequestWithContext(sctx, "POST", a.cfg.Manager+"/api/agent/tasks/"+c.t.RunID+"/data", body)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Agent-ID", a.st.AgentID)
	req.Header.Set("X-Agent-Secret", a.st.Secret)
	resp, err := a.stream.Do(req)
	if err != nil {
		cancel()
		_ = cmd.Wait()
		if s := lastLine(stderr.String()); s != "" {
			return fail("%s", s)
		}
		return fail("transfer failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		cancel()
		return fail("manager rejected the transfer: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	c.info("transfer complete")
	return proto.Result{Status: proto.StatusSuccess}
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return unwrapRestic(s)
}

func unwrapRestic(s string) string {
	var m struct {
		Message string `json:"message"`
	}
	if strings.HasPrefix(s, "{") && json.Unmarshal([]byte(s), &m) == nil && m.Message != "" {
		return m.Message
	}
	return s
}

// escapeInclude makes a literal path usable as a restic --include pattern.
func escapeInclude(p string) string {
	if runtime.GOOS == "windows" {
		return p
	}
	return strings.NewReplacer(`\`, `\\`, "*", `\*`, "?", `\?`, "[", `\[`).Replace(p)
}

// doRestore restores selected paths to disk on this agent.
func (a *Agent) doRestore(ctx context.Context, c *taskCtx, tmp string) proto.Result {
	e := c.t.Explore
	r := restic.NewRunner(a.bin, c.t.Repo, tmp, c.logf)
	defer r.Close()
	if err := reachable(c.t.Repo); err != nil {
		return fail("destination unreachable: %v", err)
	}
	r.UnlockStale(ctx)
	target := e.Target
	if e.Original {
		if runtime.GOOS == "windows" {
			return fail("restoring to the original location isn't supported on Windows")
		}
		target = "/"
		c.logf(proto.LevelWarning, "restoring to the ORIGINAL location (overwrite: "+e.Overwrite+")")
	} else if err := os.MkdirAll(target, 0o755); err != nil {
		return fail("cannot create target folder: %v", err)
	}
	args := []string{"restore", e.Snapshot, "--target", target, "--overwrite", e.Overwrite, "--verify", "--json"}
	for _, p := range e.Paths {
		args = append(args, "--include", escapeInclude(p))
	}
	c.info("restoring %d item(s) from restore point %s to %s", len(e.Paths), e.Snapshot, target)
	summary := map[string]any{"target": target, "items": len(e.Paths)}
	var lastStatus time.Time
	code, err := r.Run(ctx, func(line string) {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			return
		}
		switch m["message_type"] {
		case "status":
			if time.Since(lastStatus) > 10*time.Second {
				lastStatus = time.Now()
				c.info("progress: %.0f%%", num(m["percent_done"])*100)
			}
		case "error":
			msg := line
			if er, ok := m["error"].(map[string]any); ok {
				msg = fmt.Sprint(er["message"])
			}
			c.logf(proto.LevelWarning, fmt.Sprintf("%v: %s", m["item"], msg))
		case "summary":
			// restic's "files_restored" counts folders as well as files.
			if v, ok := m["files_restored"]; ok {
				summary["items_restored"] = v
			}
			for _, k := range []string{"files_skipped", "bytes_restored", "bytes_skipped"} {
				if v, ok := m[k]; ok {
					summary[k] = v
				}
			}
		}
	}, args...)
	if err != nil {
		return fail("restic: %v", err)
	}
	if code != 0 {
		return proto.Result{Status: proto.StatusFailed, Summary: summary, Message: fmt.Sprintf("restore failed (exit %d)", code)}
	}
	c.info("restore complete: %v items restored, %v skipped, %v bytes written", orZero(summary["items_restored"]), orZero(summary["files_skipped"]), orZero(summary["bytes_restored"]))
	return proto.Result{Status: proto.StatusSuccess, Summary: summary}
}

func orZero(v any) any {
	if v == nil {
		return 0
	}
	return v
}
