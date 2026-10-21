//go:build unix

package files

import (
	"io/fs"
	"os"
	"syscall"
)

// linkCount is the target's hard-link count.
func linkCount(_ string, fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}

// copyOwner gives f the owner of the file it replaces, best effort: it
// succeeds as root (or for a group the user belongs to) and is otherwise
// ignored, leaving the file owned by the current user.
func copyOwner(f *os.File, fi fs.FileInfo) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		_ = f.Chown(int(st.Uid), int(st.Gid))
	}
}

// syncDir flushes dir's entries so the rename survives a crash (best effort).
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}
