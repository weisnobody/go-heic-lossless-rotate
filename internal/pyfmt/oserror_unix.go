//go:build !windows

package pyfmt

import (
	"errors"
	"syscall"
	"unicode"
	"unicode/utf8"
)

// errnoOf returns the POSIX errno number and strerror-style text.
func errnoOf(err error) (int, string, bool) {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return 0, "", false
	}
	return int(errno), capitalize(errno.Error()), true
}

// capitalize upper-cases the first rune, turning Go's errno text ("no such
// file or directory") into the C library's ("No such file or directory").
func capitalize(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
