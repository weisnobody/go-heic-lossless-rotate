//go:build !unix

package heicrotate

import (
	"io/fs"
	"os"
)

func linkCount(fs.FileInfo) uint64    { return 1 }
func copyOwner(*os.File, fs.FileInfo) {}
func syncDir(string)                  {}
