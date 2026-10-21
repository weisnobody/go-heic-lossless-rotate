//go:build !unix

package files

import (
	"io/fs"
	"os"
)

func copyOwner(*os.File, fs.FileInfo) {}
func syncDir(string)                  {}
