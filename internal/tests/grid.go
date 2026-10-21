package tests

import (
	"slices"
	"testing"

	"github.com/weisnobody/go-heic-lossless-rotate/pkg/core"
)

// Grid is a tiny labelled image for checking orientation edits: a 2x3 grid
// of distinct labels, so each of the eight orientations gives a different
// grid. Its transforms are written from first principles, independent of
// package core's orientation table.
type Grid [][]int

// BaseGrid is the image as stored, before any transform.
func BaseGrid() Grid { return Grid{{1, 2, 3}, {4, 5, 6}} }

// RotCCW turns the image a quarter turn counter-clockwise.
func (g Grid) RotCCW() Grid {
	h, w := len(g), len(g[0])
	out := make(Grid, w)
	for r := range w {
		out[r] = make([]int, h)
		for c := range h {
			out[r][c] = g[c][w-1-r]
		}
	}
	return out
}

// Rot turns the image turns quarter turns counter-clockwise.
func (g Grid) Rot(turns int) Grid {
	for range turns & 3 {
		g = g.RotCCW()
	}
	return g
}

// FlipLR mirrors the image left-right.
func (g Grid) FlipLR() Grid {
	out := make(Grid, len(g))
	for r, row := range g {
		out[r] = slices.Clone(row)
		slices.Reverse(out[r])
	}
	return out
}

// FlipTB mirrors the image top-bottom.
func (g Grid) FlipTB() Grid {
	out := slices.Clone(g)
	slices.Reverse(out)
	return out
}

func (g Grid) transpose() Grid {
	h, w := len(g), len(g[0])
	out := make(Grid, w)
	for r := range w {
		out[r] = make([]int, h)
		for c := range h {
			out[r][c] = g[c][r]
		}
	}
	return out
}

// Exif applies Exif Orientation o's display transform (TIFF 6.0 tag 274).
func (g Grid) Exif(o uint16) Grid {
	switch o {
	case 1:
		return g
	case 2:
		return g.FlipLR()
	case 3:
		return g.Rot(2)
	case 4:
		return g.FlipTB()
	case 5:
		return g.transpose()
	case 6:
		return g.Rot(3)
	case 7:
		return g.transpose().Rot(2)
	case 8:
		return g.Rot(1)
	}
	return nil
}

// Equal reports whether two grids match.
func (g Grid) Equal(o Grid) bool {
	return slices.EqualFunc(g, o, func(a, b []int) bool { return slices.Equal(a, b) })
}

// DisplayedGrid is BaseGrid as a HEIF decoder displays data's primary item:
// each associated 'irot' (counter-clockwise quarter turns, low 2 bits) and
// 'imir' (bit 0 set: left-right, clear: top-bottom) in association order -
// what libheif does (image_item.cc, pixelimage.cc).
func DisplayedGrid(t testing.TB, data []byte) Grid {
	t.Helper()
	s := MustLocate(t, data)
	ipco, ok1, err1 := core.FindChild(data, s.IprpHdr.ContentStart, s.IprpHdr.ContentEnd, "ipco")
	ipma, ok2, err2 := core.FindChild(data, s.IprpHdr.ContentStart, s.IprpHdr.ContentEnd, "ipma")
	if !ok1 || !ok2 || err1 != nil || err2 != nil {
		t.Fatalf("no ipco/ipma: %v %v", err1, err2)
	}
	children, err := core.ParseIpcoChildren(data, ipco.ContentStart, ipco.ContentEnd)
	if err != nil {
		t.Fatal(err)
	}
	_, _, entries, err := core.ParseIpma(data, ipma.ContentStart, ipma.ContentEnd)
	if err != nil {
		t.Fatal(err)
	}
	g := BaseGrid()
	for _, e := range entries {
		if e.ItemID != s.PrimaryItemID {
			continue
		}
		for _, a := range e.Assocs {
			child := children[a.PropIndex-1]
			switch {
			case child.Is("irot"):
				g = g.Rot(int(data[child.ContentStart]))
			case child.Is("imir") && data[child.ContentStart]&1 == 1:
				g = g.FlipLR()
			case child.Is("imir"):
				g = g.FlipTB()
			}
		}
		break
	}
	return g
}

// ExifOrientation is data's embedded Exif Orientation value, if it has one.
func ExifOrientation(t testing.TB, data []byte) (uint16, bool) {
	t.Helper()
	s := MustLocate(t, data)
	f, ok, err := core.FindExifOrientationField(data, s.MetaBodyStart, s.MetaHdr.ContentEnd)
	if err != nil {
		t.Fatal(err)
	}
	return f.Value, ok
}
