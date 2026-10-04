// Package restic wraps the restic CLI: locating/installing the binary and
// running commands against local or agent-served (REST) repositories.
package restic

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"vaultkeeper/internal/proto"
)

func binName() string {
	if runtime.GOOS == "windows" {
		return "restic.exe"
	}
	return "restic"
}

// Find returns the path to a usable restic binary, or "".
func Find(dataDir string) string {
	if p := os.Getenv("RESTIC_BIN"); p != "" {
		return p
	}
	local := filepath.Join(dataDir, "bin", binName())
	if _, err := os.Stat(local); err == nil {
		return local
	}
	if p, err := exec.LookPath("restic"); err == nil {
		return p
	}
	return ""
}

func Version(bin string) string {
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	if len(f) >= 2 {
		return f[1]
	}
	return strings.TrimSpace(string(out))
}

// Install downloads the latest restic release into dataDir/bin, verifying the
// published SHA256 checksum.
func Install(dataDir string) (string, error) {
	cl := &http.Client{Timeout: 5 * time.Minute}
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	resp, err := cl.Get("https://api.github.com/repos/restic/restic/releases/latest")
	if err != nil {
		return "", err
	}
	err = json.NewDecoder(resp.Body).Decode(&rel)
	resp.Body.Close()
	if err != nil {
		return "", err
	}
	ver := strings.TrimPrefix(rel.Tag, "v")
	ext := ".bz2"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	want := fmt.Sprintf("restic_%s_%s_%s%s", ver, runtime.GOOS, runtime.GOARCH, ext)
	var assetURL, sumsURL string
	for _, a := range rel.Assets {
		if a.Name == want {
			assetURL = a.URL
		}
		if a.Name == "SHA256SUMS" {
			sumsURL = a.URL
		}
	}
	if assetURL == "" || sumsURL == "" {
		return "", fmt.Errorf("no restic release asset %s", want)
	}
	get := func(u string) ([]byte, error) {
		r, err := cl.Get(u)
		if err != nil {
			return nil, err
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			return nil, fmt.Errorf("GET %s: %s", u, r.Status)
		}
		return io.ReadAll(r.Body)
	}
	sums, err := get(sumsURL)
	if err != nil {
		return "", err
	}
	var expect string
	for _, ln := range strings.Split(string(sums), "\n") {
		f := strings.Fields(ln)
		if len(f) == 2 && f[1] == want {
			expect = f[0]
		}
	}
	data, err := get(assetURL)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	if expect == "" || hex.EncodeToString(h[:]) != expect {
		return "", fmt.Errorf("checksum mismatch for %s", want)
	}
	var bin []byte
	if ext == ".zip" {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return "", err
		}
		for _, f := range zr.File {
			if strings.HasSuffix(f.Name, ".exe") {
				rc, _ := f.Open()
				bin, _ = io.ReadAll(rc)
				rc.Close()
			}
		}
	} else {
		bin, err = io.ReadAll(bzip2.NewReader(bytes.NewReader(data)))
		if err != nil {
			return "", err
		}
	}
	dir := filepath.Join(dataDir, "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, binName())
	if err := os.WriteFile(dst, bin, 0o755); err != nil {
		return "", err
	}
	return dst, nil
}

// Runner executes restic against one repository.
type Runner struct {
	Bin      string
	Repo     proto.Repo
	From     *proto.Repo // for copy / init --from-repo
	LimitKB  int
	TmpDir   string
	cleanups []func()
	logf     func(level, msg string)
	quiet    bool // suppress stderr (used for probes that are expected to fail)
}

var errLine = regexp.MustCompile(`(?i)fatal|error|warning|unable|cannot|failed|denied|unauthorized`)

func NewRunner(bin string, repo proto.Repo, tmpDir string, logf func(level, msg string)) *Runner {
	return &Runner{Bin: bin, Repo: repo, TmpDir: tmpDir, logf: logf}
}

func (r *Runner) Close() {
	for _, c := range r.cleanups {
		c()
	}
}

// repoSpec returns "-r" value, extra flags and env for the repo.
func (r *Runner) spec(repo proto.Repo, fromSide bool) (string, []string, []string, error) {
	pwVar := "RESTIC_PASSWORD"
	if fromSide {
		pwVar = "RESTIC_FROM_PASSWORD"
	}
	env := []string{pwVar + "=" + repo.Password}
	if repo.Local != "" {
		return repo.Local, nil, env, nil
	}
	u, err := url.Parse(repo.URL)
	if err != nil {
		return "", nil, nil, err
	}
	u.User = url.UserPassword("restic", repo.Token)
	var flags []string
	if repo.CertPEM != "" && !fromSide {
		f, err := os.CreateTemp(r.TmpDir, "cert-*.pem")
		if err != nil {
			return "", nil, nil, err
		}
		f.WriteString(repo.CertPEM)
		f.Close()
		r.cleanups = append(r.cleanups, func() { os.Remove(f.Name()) })
		flags = append(flags, "--cacert", f.Name())
	}
	return "rest:" + u.String(), flags, env, nil
}

// Cmd builds the exec.Cmd for `restic <args>` (without -r; added here).
func (r *Runner) Cmd(ctx context.Context, args ...string) (*exec.Cmd, error) {
	spec, flags, env, err := r.spec(r.Repo, false)
	if err != nil {
		return nil, err
	}
	// --retry-lock: wait briefly for a live lock to clear rather than failing (restic >= 0.16).
	full := append([]string{"-r", spec, "--no-cache", "--retry-lock", "2m"}, flags...)
	if r.LimitKB > 0 {
		full = append(full, "--limit-upload", fmt.Sprint(r.LimitKB), "--limit-download", fmt.Sprint(r.LimitKB))
	}
	if r.From != nil && (args[0] == "copy" || args[0] == "init") {
		fspec, fflags, fenv, err := r.spec(*r.From, true)
		if err != nil {
			return nil, err
		}
		_ = fflags
		env = append(env, fenv...)
		// --from-repo is a subcommand flag: insert it right after the subcommand.
		args = append([]string{args[0], "--from-repo", fspec}, args[1:]...)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, r.Bin, full...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(cmd.Env, "RESTIC_PROGRESS_FPS=0.2")
	return cmd, nil
}

// Run executes restic, calling onOut for each stdout line and logging stderr.
// Returns the exit code (0 ok, 3 = backup finished with unreadable files).
func (r *Runner) Run(ctx context.Context, onOut func(string), args ...string) (int, error) {
	cmd, err := r.Cmd(ctx, args...)
	if err != nil {
		return -1, err
	}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			t := unwrapJSONError(strings.TrimSpace(sc.Text()))
			if t != "" && !r.quiet {
				lvl := proto.LevelInfo
				if errLine.MatchString(t) {
					lvl = proto.LevelWarning
				}
				r.logf(lvl, t)
			}
		}
		close(done)
	}()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 16<<20), 16<<20)
	for sc.Scan() {
		if onOut != nil {
			onOut(sc.Text())
		}
	}
	<-done
	err = cmd.Wait()
	if err == nil {
		return 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	return -1, err
}

// Output runs restic and returns stdout (stderr is logged).
func (r *Runner) Output(ctx context.Context, args ...string) ([]byte, int, error) {
	var buf bytes.Buffer
	code, err := r.Run(ctx, func(s string) { buf.WriteString(s); buf.WriteByte('\n') }, args...)
	return buf.Bytes(), code, err
}

// RepoExists checks whether the repository has been initialised.
func (r *Runner) RepoExists(ctx context.Context) bool {
	r.quiet = true
	defer func() { r.quiet = false }()
	_, code, err := r.Output(ctx, "cat", "config")
	return err == nil && code == 0
}

// unwrapJSONError turns restic's {"message_type":"exit_error","message":"…"}
// stderr lines into the plain message.
func unwrapJSONError(s string) string {
	if !strings.HasPrefix(s, "{") {
		return s
	}
	var m struct {
		Type    string `json:"message_type"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(s), &m) == nil && m.Message != "" && (m.Type == "exit_error" || m.Type == "error") {
		return m.Message
	}
	return s
}

// UnlockStale removes locks restic itself considers stale (dead process on this
// host, or older than 30 minutes) — e.g. left by a crashed or killed agent.
// It never removes a live lock. Returns how many were cleared.
func (r *Runner) UnlockStale(ctx context.Context) int {
	r.quiet = true
	defer func() { r.quiet = false }()
	out, code, err := r.Output(ctx, "unlock")
	if err != nil || code != 0 {
		return 0
	}
	var n int
	if _, e := fmt.Sscanf(strings.TrimSpace(string(out)), "successfully removed %d", &n); e != nil {
		// restic prints e.g. "successfully removed 2 locks" or "successfully removed 1 lock"
		for _, ln := range strings.Split(string(out), "\n") {
			if _, e := fmt.Sscanf(strings.TrimSpace(ln), "successfully removed %d", &n); e == nil {
				break
			}
		}
	}
	if n > 0 {
		r.logf(proto.LevelInfo, fmt.Sprintf("cleared %d stale repository lock(s) left by an earlier interrupted run", n))
	}
	return n
}

// TooOld reports whether a restic version string is older than 0.16.
func TooOld(v string) bool {
	var maj, min int
	if _, err := fmt.Sscanf(v, "%d.%d", &maj, &min); err != nil {
		return false
	}
	return maj == 0 && min < 16
}
