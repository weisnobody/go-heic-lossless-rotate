package cli

// Go-only (COMPAT E2): the mirror subcommand.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weisnobody/go-heic-lossless-rotate/internal/synth"
)

func TestMirrorCLI(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "photo.heic")
	original := mustSynthFile(t, input, func(o *synth.Options) { o.IncludeExif = true })

	t.Run("default output, info, reverse", func(t *testing.T) {
		rc, out, errText := runCLI("mirror", "horizontal", input)
		mirrored := filepath.Join(dir, "photo_mirrored.heic")
		if rc != 0 || !exists(mirrored) {
			t.Fatalf("mirror: rc %d\n%s%s", rc, out, errText)
		}
		for _, want := range []string{"[mirror] flipping horizontal", "[exif sync]", "reversibility self-check passed", "[ok] wrote " + mirrored} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout lacks %q:\n%s", want, out)
			}
		}
		if rc, out, _ := runCLI("info", mirrored); rc != 5 || !strings.Contains(out, "has added: horizontal flip") {
			t.Errorf("info: rc %d\n%s", rc, out)
		}
		if rc, _, _ := runCLI("info", "-q", mirrored); rc != 5 {
			t.Errorf("info -q: rc %d", rc)
		}
		restored := filepath.Join(dir, "photo_mirrored_restored.heic")
		if rc, out, errText := runCLI("reverse", mirrored); rc != 0 || !bytes.Equal(readFile(t, restored), original) {
			t.Fatalf("reverse: rc %d\n%s%s", rc, out, errText)
		}
		// mirror of a _restored file drops the suffix, like rotate.
		if rc, _, errText := runCLI("mirror", "vertical", restored, "-f"); rc != 0 || !exists(filepath.Join(dir, "photo_mirrored.heic")) {
			t.Errorf("mirror _restored: rc %d %s", rc, errText)
		}
	})

	t.Run("flags", func(t *testing.T) {
		outPath := filepath.Join(dir, "flags.heic")
		if rc, out, _ := runCLI("mirror", "vertical", input, outPath, "--dry-run"); rc != 0 || exists(outPath) || !strings.Contains(out, "[dry-run] mirror would succeed") {
			t.Errorf("--dry-run: rc %d exists %v\n%s", rc, exists(outPath), out)
		}
		if rc, out, _ := runCLI("-q", "mirror", "vertical", input, outPath); rc != 0 || out != "" || !exists(outPath) {
			t.Errorf("-q: rc %d %q", rc, out)
		}
		if rc, _, errText := runCLI("mirror", "vertical", input, outPath); rc != 10 || !strings.Contains(errText, "refusing to overwrite") {
			t.Errorf("existing output: rc %d %q", rc, errText)
		}
		if rc, _, errText := runCLI("mirror", "vertical", input, outPath, "-f", "--no-exif-sync", "-q"); rc != 0 || errText != "" {
			t.Errorf("-f --no-exif-sync: rc %d %q", rc, errText)
		}
		data := readFile(t, outPath)
		if !bytes.Contains(data, []byte("imir")) {
			t.Error("output has no imir")
		}
	})

	t.Run("usage errors", func(t *testing.T) {
		for _, argv := range [][]string{
			{"mirror", "diagonal", input},
			{"mirror", input},
			{"mirror"},
		} {
			if rc, _, errText := runCLI(argv...); rc != 2 || errText == "" {
				t.Errorf("%q: rc %d stderr %q", argv, rc, errText)
			}
		}
		if _, _, errText := runCLI("mirror", "diagonal", input); !strings.Contains(errText, "invalid choice: 'diagonal' (choose from 'horizontal', 'vertical')") {
			t.Errorf("stderr %q", errText)
		}
	})

	t.Run("own imir refused", func(t *testing.T) {
		axis := byte(1)
		own := filepath.Join(dir, "own.heic")
		mustSynthFile(t, own, func(o *synth.Options) { o.Imir = &axis })
		rc, _, errText := runCLI("mirror", "horizontal", own, filepath.Join(dir, "own_out.heic"))
		if rc != 10 || !strings.Contains(errText, "error: the primary item already has its own 'imir'") {
			t.Errorf("rc %d %q", rc, errText)
		}
		if _, err := os.Stat(filepath.Join(dir, "own_out.heic")); err == nil {
			t.Error("output written")
		}
	})
}
