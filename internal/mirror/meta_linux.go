//go:build linux

package mirror

import (
	"strings"

	"golang.org/x/sys/unix"
)

const xattrSupported = true

func platformFeatures() Features {
	return Features{Owner: true, XAttr: true, Note: "POSIX ACLs, file capabilities and user.* attributes are copied as extended attributes"}
}

func splitNul(b []byte) []string {
	var out []string
	for _, s := range strings.Split(string(b), "\x00") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func listXattr(path string) ([]string, error) {
	n, err := unix.Llistxattr(path, nil)
	if err != nil || n <= 0 {
		return nil, ignoreNoData(err)
	}
	buf := make([]byte, n)
	n, err = unix.Llistxattr(path, buf)
	if err != nil {
		return nil, ignoreNoData(err)
	}
	return splitNul(buf[:n]), nil
}

func getXattr(path, name string) ([]byte, error) {
	n, err := unix.Lgetxattr(path, name, nil)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	n, err = unix.Lgetxattr(path, name, buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func setXattr(path, name string, val []byte) error { return unix.Lsetxattr(path, name, val, 0) }
func removeXattr(path, name string) error          { return unix.Lremovexattr(path, name) }

// Filesystems without xattr support just report none.
func ignoreNoData(err error) error {
	if err == unix.ENOTSUP || err == unix.ENODATA {
		return nil
	}
	return err
}

// Attributes worth copying. security.selinux and trusted.* are host-specific.
func managedXattr(name string) bool {
	return strings.HasPrefix(name, "user.") || name == "system.posix_acl_access" ||
		name == "system.posix_acl_default" || name == "system.nfs4_acl" || name == "security.capability"
}
