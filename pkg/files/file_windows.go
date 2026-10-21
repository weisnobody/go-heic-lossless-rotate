package files

import (
	"io/fs"
	"os"
	"syscall"
)

// linkCount is the target's hard-link count. Windows' FileInfo doesn't carry
// it, so it's read from an open handle; on any failure it's 1, and the
// output is replaced atomically.
func linkCount(path string, _ fs.FileInfo) uint64 {
	f, err := os.Open(path)
	if err != nil {
		return 1
	}
	defer f.Close()
	var d syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &d); err != nil {
		return 1
	}
	return uint64(d.NumberOfLinks)
}
