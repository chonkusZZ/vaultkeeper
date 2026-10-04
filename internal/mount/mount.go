// Package mount mounts SMB/NFS shares so an agent "near" a source or
// destination can back it up over the network.
package mount

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"vaultkeeper/internal/proto"
)

// Mount mounts m at dir and returns an unmount func.
// On Windows, UNC paths are directly accessible so no mount is performed.
func Mount(m proto.Mount, dir string) (func(), error) {
	if runtime.GOOS == "windows" {
		return func() {}, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var cmd *exec.Cmd
	cleanup := func() {}
	switch m.Type {
	case "nfs":
		if runtime.GOOS == "darwin" {
			cmd = exec.Command("mount_nfs", "-o", orDefault(m.Options, "ro,resvport"), m.Remote, dir)
		} else {
			cmd = exec.Command("mount", "-t", "nfs", "-o", orDefault(m.Options, "ro"), m.Remote, dir)
		}
	case "smb":
		if runtime.GOOS == "darwin" {
			u := fmt.Sprintf("//%s:%s@%s", urlEsc(m.Username), urlEsc(m.Password), strings.TrimPrefix(m.Remote, "//"))
			cmd = exec.Command("mount_smbfs", u, dir)
		} else {
			cf, err := os.CreateTemp("", "smbcred-*")
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(cf, "username=%s\npassword=%s\n", m.Username, m.Password)
			if m.Domain != "" {
				fmt.Fprintf(cf, "domain=%s\n", m.Domain)
			}
			cf.Close()
			cleanup = func() { os.Remove(cf.Name()) }
			opts := "credentials=" + cf.Name() + ",ro"
			if m.Options != "" {
				opts += "," + m.Options
			}
			cmd = exec.Command("mount", "-t", "cifs", "-o", opts, m.Remote, dir)
		}
	default:
		return nil, fmt.Errorf("unknown mount type %q", m.Type)
	}
	out, err := cmd.CombinedOutput()
	cleanup()
	if err != nil {
		return nil, fmt.Errorf("mount %s: %v: %s", m.Remote, err, strings.TrimSpace(string(out)))
	}
	return func() {
		c := exec.Command("umount", dir)
		_ = c.Run()
		_ = os.Remove(dir)
	}, nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func urlEsc(s string) string {
	return strings.NewReplacer("@", "%40", ":", "%3A", "/", "%2F").Replace(s)
}

// Dir returns the per-job mountpoint inside the agent data dir.
func Dir(dataDir, id string) string { return filepath.Join(dataDir, "mnt", id) }
