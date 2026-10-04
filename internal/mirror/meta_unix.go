//go:build !windows

package mirror

import (
	"os"
	"syscall"
)

func ownerOf(fi os.FileInfo) (uid, gid int, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

func lchown(path string, uid, gid int) error { return os.Lchown(path, uid, gid) }
