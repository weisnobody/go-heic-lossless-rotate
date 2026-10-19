//go:build windows

package pyfmt

import (
	"errors"
	"io/fs"
)

// errnoOf maps the common Win32 errors to the CRT errno Python reports for
// open()/read()/write(). Best effort (docs/COMPAT.md OSERR).
func errnoOf(err error) (int, string, bool) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return 2, "No such file or directory", true
	case errors.Is(err, fs.ErrPermission):
		return 13, "Permission denied", true
	case errors.Is(err, fs.ErrExist):
		return 17, "File exists", true
	}
	return 0, "", false
}
