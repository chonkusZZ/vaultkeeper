package agent

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"vaultkeeper/internal/netutil"
	"vaultkeeper/internal/proto"
)

var isDarwin = runtime.GOOS == "darwin"

// Tunable so tests don't have to wait real minutes.
var (
	wakePoll   = 2 * time.Second
	wakeResend = 30 * time.Second
	wakeNote   = 30 * time.Second
)

const errScriptsOff = "scripts are disabled on this agent: start vk-agent with --allow-scripts (or VK_ALLOW_SCRIPTS=1) to let the manager run commands here"

// runScript runs a shell script, streaming its output to onLine, and returns
// its exit code. The whole process tree is killed when ctx ends.
func runScript(ctx context.Context, script string, env map[string]string, onLine func(level, line string)) (int, error) {
	name, args := shellArgs(script)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	configureProcGroup(cmd)
	out, _ := cmd.StdoutPipe()
	errp, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	var wg sync.WaitGroup
	pump := func(r interface{ Read([]byte) (int, error) }, level string) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		n := 0
		for sc.Scan() {
			if n++; n <= 500 {
				onLine(level, sc.Text())
			}
		}
	}
	wg.Add(2)
	go pump(out, proto.LevelInfo)
	go pump(errp, proto.LevelWarning)
	wg.Wait()
	err := cmd.Wait()
	if err == nil {
		return 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	return -1, err
}

// checkReady reports whether the target is ready, and a short reason when it isn't.
func (a *Agent) checkReady(ctx context.Context, w *proto.WakeSpec, c *taskCtx) (bool, string) {
	switch w.Ready {
	case "ping":
		if !netutil.ValidHost(w.Host) {
			return false, "invalid host"
		}
		pc, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if exec.CommandContext(pc, "ping", pingArgs(w.Host)...).Run() == nil {
			return true, ""
		}
		return false, w.Host + " does not answer ping"
	case "tcp":
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(w.Host, strconv.Itoa(w.Port)), 3*time.Second)
		if err != nil {
			return false, fmt.Sprintf("%s:%d is not accepting connections", w.Host, w.Port)
		}
		conn.Close()
		return true, ""
	case "command":
		code, err := runScript(ctx, w.ReadyCommand, nil, func(string, string) {})
		if err == nil && code == 0 {
			return true, ""
		}
		return false, "readiness command is not succeeding yet"
	default: // browse
		if w.TryMount {
			mc, cancel := context.WithTimeout(ctx, 20*time.Second)
			_ = exec.CommandContext(mc, "mount", w.Path).Run() // best effort; needs an fstab entry
			cancel()
		}
		es, err := os.ReadDir(w.Path)
		if err != nil {
			return false, w.Path + " is not accessible yet"
		}
		if w.Marker != "" {
			if _, err := os.Stat(strings.TrimRight(w.Path, "/\\") + string(os.PathSeparator) + w.Marker); err != nil {
				return false, "marker file " + w.Marker + " not visible yet"
			}
			return true, ""
		}
		// An unmounted mount point is an empty, listable directory: require a real mount or content.
		if isMountPoint(w.Path) || len(es) > 0 {
			return true, ""
		}
		return false, w.Path + " is listable but looks unmounted (empty, not a mount point)"
	}
}

// doWake wakes a backup target and waits until it is ready.
func (a *Agent) doWake(ctx context.Context, c *taskCtx, tmp string) proto.Result {
	w := c.t.Wake
	if w == nil {
		return fail("no wake configuration")
	}
	if (w.Method == "command" || w.Ready == "command") && !a.cfg.AllowScripts {
		return fail("%s", errScriptsOff)
	}
	timeout := time.Duration(w.TimeoutMin) * time.Minute
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	start := time.Now()
	if ok, why := a.checkReady(ctx, w, c); ok {
		c.info("target is already ready; no wake-up needed")
		return proto.Result{Status: proto.StatusSuccess, Summary: map[string]any{"waited_s": 0, "already_awake": true}}
	} else {
		c.info("target is not ready (%s); waking it", why)
	}

	sendWake := func() error {
		switch w.Method {
		case "command":
			code, err := runScript(ctx, w.Command, nil, func(l, line string) { c.logf(l, "wake: "+line) })
			if err != nil || code != 0 {
				return fmt.Errorf("wake command failed (exit %d) %v", code, err)
			}
			c.info("wake command completed")
		default:
			n, err := SendWOL(w.MAC, w.Broadcast)
			if err != nil {
				return err
			}
			c.info("sent Wake-on-LAN to %s via %d destination(s)", w.MAC, n)
		}
		return nil
	}
	if err := sendWake(); err != nil {
		return fail("%v", err)
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := time.NewTicker(wakePoll)
	defer poll.Stop()
	lastNote, lastWake := time.Now(), time.Now()
	lastWhy := ""
	for {
		select {
		case <-ctx.Done():
			return fail("cancelled")
		case <-deadline.C:
			return fail("the target did not become ready within %d minute(s) (%s)", int(timeout.Minutes()), lastWhy)
		case <-poll.C:
			ok, why := a.checkReady(ctx, w, c)
			if ok {
				waited := time.Since(start).Round(time.Second)
				c.info("target is ready after %s", waited)
				if w.SettleSec > 0 {
					c.info("waiting %ds more as configured", w.SettleSec)
					select {
					case <-time.After(time.Duration(w.SettleSec) * time.Second):
					case <-ctx.Done():
						return fail("cancelled")
					}
				}
				return proto.Result{Status: proto.StatusSuccess, Summary: map[string]any{"waited_s": int(waited.Seconds())}}
			}
			lastWhy = why
			if time.Since(lastNote) >= wakeNote {
				c.info("still waiting (%s) — %s elapsed", why, time.Since(start).Round(time.Second))
				lastNote = time.Now()
			}
			if w.Method != "command" && time.Since(lastWake) >= wakeResend {
				_ = sendWake() // WoL is fire-and-forget; repeat in case a packet was missed
				lastWake = time.Now()
			}
		}
	}
}

// doHook runs the post-completion script.
func (a *Agent) doHook(ctx context.Context, c *taskCtx, tmp string) proto.Result {
	h := c.t.Hook
	if h == nil || strings.TrimSpace(h.Script) == "" {
		return fail("no script configured")
	}
	if !a.cfg.AllowScripts {
		return fail("%s", errScriptsOff)
	}
	timeout := time.Duration(h.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c.info("running post-completion script (timeout %s)", timeout)
	code, err := runScript(sctx, h.Script, h.Env, func(l, line string) { c.logf(l, "script: "+line) })
	switch {
	case sctx.Err() == context.DeadlineExceeded:
		return fail("script timed out after %s and was killed", timeout)
	case err != nil:
		return fail("could not run the script: %v", err)
	case code != 0:
		return fail("script exited with status %d", code)
	}
	c.info("script finished successfully")
	return proto.Result{Status: proto.StatusSuccess}
}
