//go:build !windows

package pyfmt

import (
	"errors"
	"syscall"
)

// errnoOf returns the POSIX errno number and strerror-style text.
func errnoOf(err error) (int, string, bool) {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return 0, "", false
	}
	return int(errno), capitalize(errno.Error()), true
}
