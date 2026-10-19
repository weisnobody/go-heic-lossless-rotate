//go:build !windows

package pyfmt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Expected formats were captured from CPython 3.14 on macOS, e.g.
// "[Errno 2] No such file or directory: 'missing.heic'" and
// "[Errno 21] Is a directory: 'adir'". Errno numbers vary by OS, so they come
// from syscall; the text does not.
func TestOSError(t *testing.T) {
	dir := t.TempDir()
	adir := filepath.Join(dir, "adir")
	afile := filepath.Join(dir, "afile")
	if err := os.Mkdir(adir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(afile, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	loop := filepath.Join(dir, "loop1")
	os.Symlink(filepath.Join(dir, "loop2"), loop)
	os.Symlink(loop, filepath.Join(dir, "loop2"))

	read := func(p string) error { _, err := os.ReadFile(p); return err }
	write := func(p string) error { return os.WriteFile(p, []byte("x"), 0o666) }
	long := filepath.Join(dir, strings.Repeat("x", 300))
	quoted := filepath.Join(dir, "it's.heic")

	tests := []struct {
		name  string
		err   error
		errno syscall.Errno
		text  string
		path  string
	}{
		{"missing", read(filepath.Join(dir, "missing.heic")), syscall.ENOENT, "No such file or directory", filepath.Join(dir, "missing.heic")},
		{"directory read", read(adir), syscall.EISDIR, "Is a directory", adir},
		{"directory write", write(adir), syscall.EISDIR, "Is a directory", adir},
		{"not a directory", read(filepath.Join(afile, "x.heic")), syscall.ENOTDIR, "Not a directory", filepath.Join(afile, "x.heic")},
		{"missing parent", write(filepath.Join(dir, "nodir", "out.heic")), syscall.ENOENT, "No such file or directory", filepath.Join(dir, "nodir", "out.heic")},
		{"name too long", read(long), syscall.ENAMETOOLONG, "File name too long", long},
		{"symlink loop", read(loop), syscall.ELOOP, "Too many levels of symbolic links", loop},
		{"quote in path", read(quoted), syscall.ENOENT, "No such file or directory", quoted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := fmt.Sprintf("[Errno %d] %s: %s", int(tt.errno), tt.text, StrRepr(tt.path))
			if got := OSError(tt.err); got != want {
				t.Errorf("OSError = %q\n          want %q", got, want)
			}
		})
	}

	if os.Geteuid() != 0 {
		noperm := filepath.Join(dir, "noperm")
		os.WriteFile(noperm, []byte("hi"), 0o000)
		want := fmt.Sprintf("[Errno %d] Permission denied: %s", int(syscall.EACCES), StrRepr(noperm))
		if got := OSError(read(noperm)); got != want {
			t.Errorf("permission: OSError = %q, want %q", got, want)
		}
	}
	if got := OSError(errors.New("plain")); got != "plain" {
		t.Errorf("non-PathError: %q", got)
	}
	if got := StrRepr(`/tmp/it's.heic`); got != `"/tmp/it's.heic"` {
		t.Errorf("quote selection: %s", got) // Python: "it's.heic" (double quotes)
	}
}
