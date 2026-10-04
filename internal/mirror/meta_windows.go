//go:build windows

package mirror

import (
	"errors"
	"os"
)

func ownerOf(fi os.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }
func lchown(path string, uid, gid int) error {
	return errors.New("ownership is not supported on Windows")
}
