package pyfmt

import (
	"errors"
	"fmt"
	"io/fs"
)

// OSError mirrors str(e) for the OSError Python raises from open(), read(),
// or write() on a path: "[Errno 2] No such file or directory: 'x.heic'".
// The errno number and text follow the platform's C library on Unix (Go's
// errno text with its first letter capitalized matches macOS and glibc
// strerror for the common errnos); see errnoOf for Windows. Errors that
// aren't *fs.PathError, or whose errno is unknown, fall back to
// err.Error() (docs/COMPAT.md OSERR).
func OSError(err error) string {
	var pe *fs.PathError
	if !errors.As(err, &pe) {
		return err.Error()
	}
	num, text, ok := errnoOf(pe.Err)
	if !ok {
		return err.Error()
	}
	return fmt.Sprintf("[Errno %d] %s: %s", num, text, StrRepr(pe.Path))
}
