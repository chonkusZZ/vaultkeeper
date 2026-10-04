//go:build darwin

package mirror

import (
	"strings"

	"golang.org/x/sys/unix"
)

const xattrSupported = true

func platformFeatures() Features {
	return Features{Owner: true, XAttr: true, Note: "extended attributes are copied; macOS ACLs (a separate system) are not"}
}

func listXattr(path string) ([]string, error) {
	n, err := unix.Listxattr(path, nil)
	if err != nil || n <= 0 {
		return nil, ignoreNoData(err)
	}
	buf := make([]byte, n)
	n, err = unix.Listxattr(path, buf)
	if err != nil {
		return nil, ignoreNoData(err)
	}
	var out []string
	for _, s := range strings.Split(string(buf[:n]), "\x00") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

func getXattr(path, name string) ([]byte, error) {
	n, err := unix.Getxattr(path, name, nil)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	n, err = unix.Getxattr(path, name, buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func setXattr(path, name string, val []byte) error { return unix.Setxattr(path, name, val, 0) }
func removeXattr(path, name string) error          { return unix.Removexattr(path, name) }

func ignoreNoData(err error) error {
	if err == unix.ENOTSUP || err == unix.ENOATTR {
		return nil
	}
	return err
}

// System-managed attributes that can't (or shouldn't) be copied.
func managedXattr(name string) bool {
	switch name {
	case "com.apple.provenance", "com.apple.macl", "com.apple.system.Security":
		return false
	}
	return true
}
