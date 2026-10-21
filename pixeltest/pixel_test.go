//go:build pixeltest

// Package pixeltest checks orientation edits against real HEIF decoders:
// libheif's heif-dec and, on macOS, Apple ImageIO through sips. It encodes
// a test pattern, edits the file with package core, decodes both, and
// requires the edited file to display as the original's display with the
// edit applied on screen. This is what settles the 'imir' axis question:
// the spec's wording has varied between editions, so what counts is what
// decoders do.
//
// Run with `make pixeltest`; each tool that isn't installed is skipped.
package pixeltest

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/weisnobody/go-heic-lossless-rotate/pkg/core"
)

// The pattern: 4x2 blocks of distinct, well-separated colors on a 256x128
// image - wide, so a transposition can't go unnoticed, and in big flat
// blocks, so lossy encoding doesn't matter.
const (
	cols, rows = 4, 2
	block      = 64
)

var palette = []color.RGBA{
	{230, 25, 25, 255}, {25, 200, 25, 255}, {25, 25, 230, 255}, {230, 230, 25, 255},
	{25, 230, 230, 255}, {230, 25, 230, 255}, {240, 240, 240, 255}, {20, 20, 20, 255},
}

func pattern() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, cols*block, rows*block))
	for y := range rows * block {
		for x := range cols * block {
			img.SetRGBA(x, y, palette[(y/block)*cols+x/block])
		}
	}
	return img
}

// on-screen transforms of a decoded image

func rotCCW(src image.Image) *image.RGBA {
	b := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dy(), b.Dx()))
	for y := range b.Dy() {
		for x := range b.Dx() {
			out.Set(y, b.Dx()-1-x, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

func flip(src image.Image, lr bool) *image.RGBA {
	b := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := range b.Dy() {
		for x := range b.Dx() {
			dx, dy := x, y
			if lr {
				dx = b.Dx() - 1 - x
			} else {
				dy = b.Dy() - 1 - y
			}
			out.Set(dx, dy, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

type edit struct {
	name     string
	apply    func([]byte) ([]byte, error)
	onScreen func(image.Image) image.Image
}

func rotate(turns int) edit {
	return edit{fmt.Sprintf("R%d", turns),
		func(d []byte) ([]byte, error) { return core.ApplyRotation(d, turns, core.RotateOptions{}) },
		func(img image.Image) image.Image {
			for range turns {
				img = rotCCW(img)
			}
			return img
		}}
}

func mirror(axis core.MirrorAxis) edit {
	name := map[core.MirrorAxis]string{core.MirrorHorizontal: "H", core.MirrorVertical: "V"}[axis]
	return edit{name,
		func(d []byte) ([]byte, error) { return core.ApplyMirror(d, axis, core.RotateOptions{}) },
		func(img image.Image) image.Image { return flip(img, axis == core.MirrorHorizontal) }}
}

var sequences = [][]edit{
	{mirror(core.MirrorHorizontal)},
	{mirror(core.MirrorVertical)},
	{rotate(1)},
	{mirror(core.MirrorHorizontal), rotate(1)},
	{rotate(1), mirror(core.MirrorHorizontal)},
	{mirror(core.MirrorVertical), rotate(3)},
	{rotate(3), mirror(core.MirrorVertical), rotate(1)},
	{mirror(core.MirrorHorizontal), mirror(core.MirrorHorizontal)},
	{mirror(core.MirrorHorizontal), rotate(1), mirror(core.MirrorVertical)},
}

type tool struct {
	name string
	bin  string
	args func(in, out string) []string
}

var encoders = []tool{
	{"heif-enc", "heif-enc", func(in, out string) []string { return []string{"-q", "90", in, "-o", out} }},
	{"sips", "sips", func(in, out string) []string { return []string{"-s", "format", "heic", in, "--out", out} }},
}

// decoders: heif-dec, and Apple ImageIO through imageio.swift (sips can't
// be used to decode: it writes the stored pixels without applying the
// orientation). imageio's bin is set once it's compiled.
var decoders = []tool{
	{"heif-dec", "heif-dec", func(in, out string) []string { return []string{in, out} }},
	{"imageio", "", func(in, out string) []string { return []string{in, out} }},
}

// buildImageIO compiles imageio.swift into dir, or reports why it can't.
func buildImageIO(dir string) (string, error) {
	if _, err := exec.LookPath("swiftc"); err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "imageio")
	if b, err := exec.Command("swiftc", "-O", "-o", bin, "imageio.swift").CombinedOutput(); err != nil {
		return "", fmt.Errorf("swiftc: %v\n%s", err, b)
	}
	return bin, nil
}

func run(t *testing.T, tl tool, in, out string) {
	t.Helper()
	cmd := exec.Command(tl.bin, tl.args(in, out)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", tl.name, err, b)
	}
}

func decode(t *testing.T, dec tool, heic []byte, dir, name string) image.Image {
	t.Helper()
	in, out := filepath.Join(dir, name+".heic"), filepath.Join(dir, name+"-"+dec.name+".png")
	if err := os.WriteFile(in, heic, 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dec, in, out)
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// sameBlocks compares the two images' sizes and the color at the center of
// every block.
func sameBlocks(want, got image.Image) error {
	wb, gb := want.Bounds(), got.Bounds()
	if wb.Dx() != gb.Dx() || wb.Dy() != gb.Dy() {
		return fmt.Errorf("size %dx%d, want %dx%d", gb.Dx(), gb.Dy(), wb.Dx(), wb.Dy())
	}
	for y := block / 2; y < wb.Dy(); y += block {
		for x := block / 2; x < wb.Dx(); x += block {
			w := color.RGBAModel.Convert(want.At(wb.Min.X+x, wb.Min.Y+y)).(color.RGBA)
			g := color.RGBAModel.Convert(got.At(gb.Min.X+x, gb.Min.Y+y)).(color.RGBA)
			if d := max(absDiff(w.R, g.R), absDiff(w.G, g.G), absDiff(w.B, g.B)); d > 40 {
				return fmt.Errorf("block at (%d,%d): color %v, want %v", x, y, g, w)
			}
		}
	}
	return nil
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func TestDecodersShowEdits(t *testing.T) {
	dir := t.TempDir()
	imageio, imageioErr := buildImageIO(dir)
	srcPNG := filepath.Join(dir, "pattern.png")
	f, err := os.Create(srcPNG)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, pattern()); err != nil {
		t.Fatal(err)
	}
	f.Close()

	for _, enc := range encoders {
		t.Run(enc.name, func(t *testing.T) {
			if _, err := exec.LookPath(enc.bin); err != nil {
				t.Skipf("%s not installed", enc.bin)
			}
			srcHEIC := filepath.Join(dir, enc.name+".heic")
			run(t, enc, srcPNG, srcHEIC)
			original, err := os.ReadFile(srcHEIC)
			if err != nil {
				t.Fatal(err)
			}
			for _, dec := range decoders {
				t.Run(dec.name, func(t *testing.T) {
					if dec.name == "imageio" {
						if imageioErr != nil {
							t.Skipf("can't build the ImageIO decoder: %v", imageioErr)
						}
						dec.bin = imageio
					}
					if _, err := exec.LookPath(dec.bin); err != nil {
						t.Skipf("%s not installed", dec.bin)
					}
					base := decode(t, dec, original, dir, enc.name+"-orig")
					if err := sameBlocks(pattern(), base); err != nil {
						t.Fatalf("original decode: %v", err)
					}
					for i, seq := range sequences {
						data, want, name := original, base, ""
						for _, e := range seq {
							if data, err = e.apply(data); err != nil {
								t.Fatalf("%s: %v", e.name, err)
							}
							want = e.onScreen(want)
							name += e.name
						}
						got := decode(t, dec, data, dir, fmt.Sprintf("%s-%d-%s", enc.name, i, name))
						if err := sameBlocks(want, got); err != nil {
							t.Errorf("%s: %v", name, err)
						}
					}
				})
			}
		})
	}
}
