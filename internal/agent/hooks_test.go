package agent

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"vaultkeeper/internal/netutil"
	"vaultkeeper/internal/proto"
)

func testTask(a *Agent, t proto.Task) *taskCtx {
	return &taskCtx{a: a, t: t, cancel: func() {}}
}

func logText(c *taskCtx) string {
	var b strings.Builder
	for _, l := range c.buf {
		b.WriteString(l.Level + ": " + l.Message + "\n")
	}
	return b.String()
}

func TestMagicPacketLayout(t *testing.T) {
	mac, err := netutil.ParseMAC("AA-BB-CC-DD-EE-FF")
	if err != nil {
		t.Fatal(err)
	}
	p := MagicPacket(mac)
	if len(p) != 102 || !bytes.Equal(p[:6], bytes.Repeat([]byte{0xff}, 6)) {
		t.Fatalf("bad header/length %d", len(p))
	}
	for i := 0; i < 16; i++ {
		if !bytes.Equal(p[6+i*6:12+i*6], mac) {
			t.Fatalf("MAC repetition %d wrong", i)
		}
	}
}

func TestParseMACForms(t *testing.T) {
	for _, in := range []string{"aa:bb:cc:dd:ee:ff", "AA-BB-CC-DD-EE-FF", "aabb.ccdd.eeff", "aabbccddeeff"} {
		hw, err := netutil.ParseMAC(in)
		if err != nil || hw.String() != "aa:bb:cc:dd:ee:ff" {
			t.Errorf("%q → %v %v", in, hw, err)
		}
	}
	for _, bad := range []string{"", "aa:bb:cc", "zz:bb:cc:dd:ee:ff", "aa:bb:cc:dd:ee:ff:00:11"} {
		if _, err := netutil.ParseMAC(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, bad := range []string{"-f", "--help", "a b", "host;rm", ""} {
		if netutil.ValidHost(bad) {
			t.Errorf("host %q accepted", bad)
		}
	}
}

// The packet really goes out over UDP to the configured broadcast address.
func TestSendWOLDelivers(t *testing.T) {
	l, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Skip("no UDP loopback")
	}
	defer l.Close()
	got := make(chan []byte, 4)
	go func() {
		buf := make([]byte, 256)
		for {
			n, _, err := l.ReadFromUDP(buf)
			if err != nil {
				return
			}
			got <- append([]byte(nil), buf[:n]...)
		}
	}()
	n, err := SendWOL("11:22:33:44:55:66", l.LocalAddr().String())
	if err != nil || n != 1 {
		t.Fatalf("send: %d %v", n, err)
	}
	select {
	case p := <-got:
		mac, _ := netutil.ParseMAC("11:22:33:44:55:66")
		if !bytes.Equal(p, MagicPacket(mac)) {
			t.Fatal("packet content wrong")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no packet received")
	}
}

func TestReadyTCP(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	a := &Agent{}
	c := testTask(a, proto.Task{})
	w := &proto.WakeSpec{Ready: "tcp", Host: "127.0.0.1", Port: port}
	if ok, _ := a.checkReady(context.Background(), w, c); !ok {
		t.Fatal("open port should be ready")
	}
	l.Close()
	if ok, _ := a.checkReady(context.Background(), w, c); ok {
		t.Fatal("closed port must not be ready")
	}
}

func TestReadyBrowse(t *testing.T) {
	a := &Agent{}
	c := testTask(a, proto.Task{})
	dir := t.TempDir()
	// An empty directory on the same device looks exactly like an unmounted mount point.
	empty := filepath.Join(dir, "nas")
	os.Mkdir(empty, 0o755)
	w := &proto.WakeSpec{Ready: "browse", Path: empty}
	if ok, why := a.checkReady(context.Background(), w, c); ok {
		t.Fatalf("an empty, unmounted folder must NOT count as browseable (%s)", why)
	}
	os.WriteFile(filepath.Join(empty, "share-content.txt"), []byte("x"), 0o644)
	if ok, _ := a.checkReady(context.Background(), w, c); !ok {
		t.Fatal("a folder with content is browseable")
	}
	// Marker mode: ready only when the marker file is visible.
	m := filepath.Join(dir, "marked")
	os.Mkdir(m, 0o755)
	os.WriteFile(filepath.Join(m, "other.txt"), []byte("x"), 0o644)
	wm := &proto.WakeSpec{Ready: "browse", Path: m, Marker: ".ready"}
	if ok, _ := a.checkReady(context.Background(), wm, c); ok {
		t.Fatal("marker missing: not ready")
	}
	os.WriteFile(filepath.Join(m, ".ready"), nil, 0o644)
	if ok, _ := a.checkReady(context.Background(), wm, c); !ok {
		t.Fatal("marker present: ready")
	}
	if ok, _ := a.checkReady(context.Background(), &proto.WakeSpec{Ready: "browse", Path: filepath.Join(dir, "missing")}, c); ok {
		t.Fatal("missing path must not be ready")
	}
}

func TestWakeAlreadyAwakeSendsNothing(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	a := &Agent{}
	c := testTask(a, proto.Task{Wake: &proto.WakeSpec{Method: "wol", MAC: "aa:bb:cc:dd:ee:ff", Ready: "tcp", Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port, TimeoutMin: 1}})
	res := a.doWake(context.Background(), c, t.TempDir())
	if res.Status != proto.StatusSuccess || !strings.Contains(logText(c), "no wake-up needed") || strings.Contains(logText(c), "Wake-on-LAN") {
		t.Fatalf("%+v\n%s", res, logText(c))
	}
}

// Full wake: the target is down, a magic packet is received, the "NAS" then
// boots (starts listening) a moment later, and the wake step returns as soon as it is up.
func TestWakeWaitsUntilTargetIsUp(t *testing.T) {
	wakePoll, wakeResend = 100*time.Millisecond, time.Hour
	defer func() { wakePoll, wakeResend = 2*time.Second, 30*time.Second }()

	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skip("no UDP loopback")
	}
	defer udp.Close()
	probe, _ := net.Listen("tcp", "127.0.0.1:0")
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close() // the "NAS" is down: nothing listens on this port yet

	booted := make(chan net.Listener, 1)
	go func() {
		buf := make([]byte, 256)
		if n, _, err := udp.ReadFromUDP(buf); err == nil && n == 102 {
			time.Sleep(700 * time.Millisecond) // boot time
			l, _ := net.Listen("tcp", net.JoinHostPort("127.0.0.1", itoa(port)))
			booted <- l
		}
	}()
	defer func() {
		select {
		case l := <-booted:
			if l != nil {
				l.Close()
			}
		case <-time.After(2 * time.Second):
		}
	}()

	a := &Agent{}
	c := testTask(a, proto.Task{Wake: &proto.WakeSpec{Method: "wol", MAC: "aa:bb:cc:dd:ee:ff", Broadcast: udp.LocalAddr().String(), Ready: "tcp", Host: "127.0.0.1", Port: port, TimeoutMin: 1}})
	start := time.Now()
	res := a.doWake(context.Background(), c, t.TempDir())
	if res.Status != proto.StatusSuccess {
		t.Fatalf("%+v\n%s", res, logText(c))
	}
	if el := time.Since(start); el < 500*time.Millisecond || el > 10*time.Second {
		t.Fatalf("should wait for the boot, not return instantly or hang (took %s)", el)
	}
	if !strings.Contains(logText(c), "Wake-on-LAN") || !strings.Contains(logText(c), "target is ready after") {
		t.Fatalf("log:\n%s", logText(c))
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestWakeTimesOutClearly(t *testing.T) {
	wakePoll = 50 * time.Millisecond
	defer func() { wakePoll = 2 * time.Second }()
	probe, _ := net.Listen("tcp", "127.0.0.1:0")
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	a := &Agent{}
	// TimeoutMin is whole minutes; shorten via a cancelled context to keep the test fast.
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	c := testTask(a, proto.Task{Wake: &proto.WakeSpec{Method: "wol", MAC: "aa:bb:cc:dd:ee:ff", Broadcast: "127.0.0.1:9", Ready: "tcp", Host: "127.0.0.1", Port: port, TimeoutMin: 1}})
	res := a.doWake(ctx, c, t.TempDir())
	if res.Status != proto.StatusFailed {
		t.Fatalf("an unreachable target must fail the wake step: %+v", res)
	}
}

// --- scripts ---

func TestScriptsAreOffByDefault(t *testing.T) {
	a := &Agent{} // AllowScripts false
	c := testTask(a, proto.Task{Hook: &proto.HookSpec{Script: "touch /tmp/should-not-exist-vk-test"}})
	res := a.doHook(context.Background(), c, t.TempDir())
	if res.Status != proto.StatusFailed || !strings.Contains(res.Message, "--allow-scripts") {
		t.Fatalf("scripts must be refused unless enabled: %+v", res)
	}
	if _, err := os.Stat("/tmp/should-not-exist-vk-test"); err == nil {
		t.Fatal("the script ran anyway!")
	}
	cw := testTask(a, proto.Task{Wake: &proto.WakeSpec{Method: "command", Command: "echo hi", Ready: "tcp", Host: "x", Port: 1}})
	if r := a.doWake(context.Background(), cw, t.TempDir()); r.Status != proto.StatusFailed || !strings.Contains(r.Message, "--allow-scripts") {
		t.Fatalf("custom wake command must also need the opt-in: %+v", r)
	}
}

func TestHookRunsScriptWithEnvironment(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("shell script test is unix-only")
	}
	out := filepath.Join(t.TempDir(), "out.txt")
	a := &Agent{cfg: Config{AllowScripts: true}}
	c := testTask(a, proto.Task{Hook: &proto.HookSpec{
		Script: `echo "job=$VK_JOB_NAME status=$VK_STATUS" > "` + out + `"; echo to-stdout; echo to-stderr >&2`,
		Env:    map[string]string{"VK_JOB_NAME": "Docs", "VK_STATUS": "success"}, TimeoutSec: 10}})
	res := a.doHook(context.Background(), c, t.TempDir())
	if res.Status != proto.StatusSuccess {
		t.Fatalf("%+v\n%s", res, logText(c))
	}
	if b, _ := os.ReadFile(out); strings.TrimSpace(string(b)) != "job=Docs status=success" {
		t.Fatalf("environment not passed: %q", b)
	}
	if lt := logText(c); !strings.Contains(lt, "script: to-stdout") || !strings.Contains(lt, "warning: script: to-stderr") {
		t.Fatalf("output must be captured into the log:\n%s", lt)
	}
}

func TestHookFailureAndTimeout(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("unix-only")
	}
	a := &Agent{cfg: Config{AllowScripts: true}}
	c := testTask(a, proto.Task{Hook: &proto.HookSpec{Script: "echo boom; exit 7", TimeoutSec: 10}})
	if r := a.doHook(context.Background(), c, t.TempDir()); r.Status != proto.StatusFailed || !strings.Contains(r.Message, "status 7") {
		t.Fatalf("exit status must fail the step: %+v", r)
	}
	// A hung script (and its child) is killed at the timeout.
	marker := filepath.Join(t.TempDir(), "child-survived")
	c2 := testTask(a, proto.Task{Hook: &proto.HookSpec{Script: `(sleep 3; touch "` + marker + `") & sleep 30`, TimeoutSec: 1}})
	start := time.Now()
	r := a.doHook(context.Background(), c2, t.TempDir())
	if r.Status != proto.StatusFailed || !strings.Contains(r.Message, "timed out") || time.Since(start) > 8*time.Second {
		t.Fatalf("timeout not enforced: %+v after %s", r, time.Since(start))
	}
	time.Sleep(3500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a child process outlived the timeout (process group not killed)")
	}
}
