// Command vk-agent is the Vaultkeeper agent: a single executable that acts as
// a backup source, a backup destination, or both.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"vaultkeeper/internal/agent"
	"vaultkeeper/internal/restic"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func main() {
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("vk-agent", flag.ExitOnError)
	manager := fs.String("manager", env("VK_MANAGER", ""), "manager base URL, e.g. http://backup-mgr:8080")
	token := fs.String("token", env("VK_TOKEN", ""), "enrolment token (first run only)")
	name := fs.String("name", env("VK_NAME", ""), "agent display name (default: hostname)")
	roles := fs.String("roles", env("VK_ROLES", "source"), "comma list: source,dest")
	dataDir := fs.String("data-dir", env("VK_DATA_DIR", filepath.Join(home, ".vaultkeeper-agent")), "state dir; on a destination this also holds the repositories")
	listen := fs.String("listen", env("VK_LISTEN", ":8765"), "destination data endpoint listen address")
	advertise := fs.String("advertise", env("VK_ADVERTISE", ""), "URL source agents use to reach this destination (default: auto-detected https://ip:port)")
	mirrorRoot := fs.String("mirror-root", env("VK_MIRROR_ROOT", ""), "destination: folder holding raw mirrors (default <data-dir>/mirrors); point this at your big disk or mounted share")
	allowScripts := fs.Bool("allow-scripts", env("VK_ALLOW_SCRIPTS", "") != "", "let the manager run shell commands on this machine (wake commands, post-job scripts). Off by default; cannot be enabled remotely")
	insecure := fs.Bool("insecure", env("VK_INSECURE", "") != "", "skip TLS verification of the manager (self-signed)")

	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "install-restic":
			_ = fs.Parse(args[1:])
			p, err := restic.Install(*dataDir)
			if err != nil {
				log.Fatal(err)
			}
			fmt.Println("installed", p)
			return
		case "systemd":
			_ = fs.Parse(args[1:])
			exe, _ := os.Executable()
			fmt.Printf(`[Unit]
Description=Vaultkeeper backup agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s run --manager %s --name %q --roles %s --data-dir %s --listen %s
Restart=always
RestartSec=5
Environment=VK_TOKEN=%s

[Install]
WantedBy=multi-user.target
`, exe, *manager, *name, *roles, *dataDir, *listen, *token)
			return
		case "run":
			args = args[1:]
		default:
			fmt.Fprintln(os.Stderr, "usage: vk-agent [run|install-restic|systemd] [flags]")
			os.Exit(2)
		}
	}
	_ = fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg := agent.Config{
		Manager: *manager, Token: *token, Name: *name, DataDir: *dataDir,
		Listen: *listen, Advertise: *advertise, Insecure: *insecure, MirrorRoot: *mirrorRoot, AllowScripts: *allowScripts,
	}
	for _, r := range strings.Split(*roles, ",") {
		if r = strings.TrimSpace(r); r == "source" || r == "dest" {
			cfg.Roles = append(cfg.Roles, r)
		}
	}
	if err := agent.Run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}
