//go:build !unix && !windows

package files

import "io/fs"

func linkCount(string, fs.FileInfo) uint64 { return 1 }
