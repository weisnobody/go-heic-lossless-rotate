package core

import (
	"slices"
	"testing"
)

// A 2x3 grid of distinct labels stands in for an image, so every
// orientation gives a different grid. The grid operations here are written
// independently of orientConcat, from what each Exif Orientation value
// means for display.

type grid [][]int

func baseGrid() grid { return grid{{1, 2, 3}, {4, 5, 6}} }

func (g grid) rotCCW() grid { // a quarter turn counter-clockwise
	h, w := len(g), len(g[0])
	out := make(grid, w)
	for r := range w {
		out[r] = make([]int, h)
		for c := range h {
			out[r][c] = g[c][w-1-r]
		}
	}
	return out
}

func (g grid) flipLR() grid {
	out := make(grid, len(g))
	for r, row := range g {
		out[r] = slices.Clone(row)
		slices.Reverse(out[r])
	}
	return out
}

func (g grid) flipTB() grid {
	out := slices.Clone(g)
	slices.Reverse(out)
	return out
}

func (g grid) transpose() grid {
	h, w := len(g), len(g[0])
	out := make(grid, w)
	for r := range w {
		out[r] = make([]int, h)
		for c := range h {
			out[r][c] = g[c][r]
		}
	}
	return out
}

func (g grid) equal(o grid) bool {
	return slices.EqualFunc(g, o, func(a, b []int) bool { return slices.Equal(a, b) })
}

// exif applies Exif Orientation o's display transform (EXIF 2.3 / TIFF 6.0
// tag 274).
func (g grid) exif(o orient) grid {
	switch o {
	case 1:
		return g
	case 2:
		return g.flipLR()
	case 3:
		return g.rotCCW().rotCCW()
	case 4:
		return g.flipTB()
	case 5:
		return g.transpose()
	case 6:
		return g.rotCCW().rotCCW().rotCCW() // 90 deg clockwise
	case 7:
		return g.transpose().rotCCW().rotCCW()
	case 8:
		return g.rotCCW()
	}
	panic("bad orientation")
}

func TestOrientConcatMatchesDisplay(t *testing.T) {
	for a := orient(1); a <= 8; a++ {
		for b := orient(1); b <= 8; b++ {
			want := baseGrid().exif(a).exif(b)
			if got := baseGrid().exif(a.then(b)); !got.equal(want) {
				t.Errorf("%d then %d = %d: grid %v, want %v", a, b, a.then(b), got, want)
			}
		}
	}
}

func TestIrotImirOrient(t *testing.T) {
	g := baseGrid()
	for turns := range 4 {
		if !baseGrid().exif(irotOrient(turns)).equal(g) {
			t.Errorf("irotOrient(%d) = %d", turns, irotOrient(turns))
		}
		g = g.rotCCW()
	}
	if o := imirOrient(1); !baseGrid().exif(o).equal(baseGrid().flipLR()) {
		t.Errorf("imirOrient(1) = %d, want a left-right flip", o)
	}
	if o := imirOrient(0); !baseGrid().exif(o).equal(baseGrid().flipTB()) {
		t.Errorf("imirOrient(0) = %d, want a top-bottom flip", o)
	}
	if imirOrient(0xff) != orientFlipLR || imirOrient(0xfe) != orientFlipTB {
		t.Error("imirOrient: only bit 0 counts")
	}
}

// libheif's own composition cases (tests/mini_box.cc).
func TestOrientLibheifCases(t *testing.T) {
	H, V := imirOrient(1), imirOrient(0)
	rot := func(ccwDegrees int) orient { return irotOrient(ccwDegrees / 90) }
	for _, tt := range []struct {
		seq  []orient
		want orient
	}{
		{[]orient{H}, 2}, {[]orient{V}, 4},
		{[]orient{rot(270), H}, 5}, {[]orient{rot(270), V}, 7},
		{[]orient{rot(180), H}, 4}, {[]orient{rot(180), V}, 2},
		{[]orient{rot(90), H}, 7},
		{[]orient{H, rot(270)}, 7}, {[]orient{H, rot(180)}, 4},
		{[]orient{V, rot(270)}, 5}, {[]orient{V, rot(90)}, 7},
	} {
		o := orientNormal
		for _, s := range tt.seq {
			o = o.then(s)
		}
		if o != tt.want {
			t.Errorf("%v = %d, want %d", tt.seq, o, tt.want)
		}
	}
}

func TestOrientGroup(t *testing.T) {
	for o := orient(1); o <= 8; o++ {
		if o.then(o.inverse()) != orientNormal || o.inverse().then(o) != orientNormal {
			t.Errorf("inverse(%d) = %d", o, o.inverse())
		}
		turns, flip := o.parts()
		p := irotOrient(turns)
		if flip {
			p = p.then(orientFlipLR)
		}
		if p != o || flip != o.mirrored() {
			t.Errorf("parts(%d) = %d, %v", o, turns, flip)
		}
	}
}

func TestOrientString(t *testing.T) {
	for o, want := range map[orient]string{
		1: "0 deg", 8: "90 deg", 3: "180 deg", 6: "270 deg",
		2: "horizontal flip", 4: "vertical flip",
		7: "90 deg + horizontal flip", 5: "270 deg + horizontal flip",
		0: "invalid orientation 0",
	} {
		if got := o.String(); got != want {
			t.Errorf("orient(%d).String() = %q, want %q", o, got, want)
		}
	}
}
