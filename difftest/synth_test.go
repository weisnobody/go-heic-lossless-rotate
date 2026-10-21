//go:build difftest

package difftest

import (
	"fmt"
	"testing"

	"github.com/weisnobody/go-heic-lossless-rotate/internal/synth"
)

// synthCase is one build_synthetic_heic call, expressed both ways.
type synthCase struct {
	name string
	opts synth.Options
	// pyDefaults: call build_synthetic_heic() with NO kwargs, to check
	// DefaultOptions() against Python's actual defaults rather than our
	// transcription of them.
	pyDefaults bool
}

func (c synthCase) request(id string) Request {
	if c.pyDefaults {
		return Request{ID: id, Op: "synth"}
	}
	o := c.opts
	return Request{
		ID: id,
		Op: "synth",
		Args: map[string]any{
			"layout":           string(o.Layout),
			"include_irot":     o.IncludeIrot,
			"irot_value":       o.IrotValue,
			"include_exif":     o.IncludeExif,
			"exif_orientation": o.ExifOrientation,
			"exif_position":    string(o.ExifPosition),
			"extra_mdat":       o.ExtraMdat,
		},
		BytesArgs: map[string][]byte{"mdat_payload": o.MdatPayload},
	}
}

// synthCases enumerates every combination the Python generator supports,
// plus payload-shape variants, out-of-range irot values, and Python's
// own defaults.
func synthCases() []synthCase {
	var cases []synthCase
	add := func(name string, mutate func(*synth.Options)) {
		o := synth.DefaultOptions()
		mutate(&o)
		cases = append(cases, synthCase{name: name, opts: o})
	}

	cases = append(cases, synthCase{name: "python_defaults", opts: synth.DefaultOptions(), pyDefaults: true})

	type irotOpt struct {
		on    bool
		value int
	}
	irots := []irotOpt{{false, 0}, {true, 0}, {true, 1}, {true, 2}, {true, 3}}
	type exifOpt struct {
		on          bool
		orientation uint16
		position    synth.ExifPosition
	}
	exifs := []exifOpt{{false, 1, synth.ExifEnd}}
	for _, orient := range []uint16{1, 3, 6, 8} {
		for _, pos := range []synth.ExifPosition{synth.ExifEnd, synth.ExifMiddle} {
			exifs = append(exifs, exifOpt{true, orient, pos})
		}
	}
	for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
		for _, ir := range irots {
			for _, ex := range exifs {
				for _, extra := range []bool{false, true} {
					name := fmt.Sprintf("%s/irot=%v:%d/exif=%v:%d:%s/extra=%v",
						layout, ir.on, ir.value, ex.on, ex.orientation, ex.position, extra)
					add(name, func(o *synth.Options) {
						o.Layout = layout
						o.IncludeIrot, o.IrotValue = ir.on, ir.value
						o.IncludeExif, o.ExifOrientation, o.ExifPosition = ex.on, ex.orientation, ex.position
						o.ExtraMdat = extra
					})
				}
			}
		}
	}

	allBytes := make([]byte, 256)
	for i := range allBytes {
		allBytes[i] = byte(i)
	}
	large := make([]byte, 4<<20)
	for i := range large {
		large[i] = byte(i*31 + i>>8)
	}
	payloads := []struct {
		name string
		data []byte
	}{{"empty", []byte{}}, {"one_byte", []byte{0x5a}}, {"all_256", allBytes}, {"4MiB", large}}
	for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
		for _, p := range payloads {
			add(fmt.Sprintf("payload=%s/%s", p.name, layout), func(o *synth.Options) {
				o.Layout, o.MdatPayload = layout, p.data
				o.IncludeIrot, o.IrotValue = true, 1
				o.IncludeExif, o.ExifOrientation, o.ExifPosition = true, 6, synth.ExifMiddle
				o.ExtraMdat = true
			})
		}
	}

	// Python masks irot_value with & 0x3, including negatives.
	for _, v := range []int{4, 5, -1} {
		add(fmt.Sprintf("irot_out_of_range=%d", v), func(o *synth.Options) {
			o.IncludeIrot, o.IrotValue = true, v
		})
	}

	// Error paths: messages and check order must match Python's ValueError.
	add("error/layout", func(o *synth.Options) { o.Layout = "sideways" })
	add("error/exif_position", func(o *synth.Options) { o.ExifPosition = "top" })
	add("error/both", func(o *synth.Options) { o.Layout, o.ExifPosition = "x", "y" })
	return cases
}

// TestSynthMatchesPython is the Phase 1 gate: internal/synth must produce
// byte-identical output to upstream's tests/synth_heic.py.
func TestSynthMatchesPython(t *testing.T) {
	py := Upstream(t)
	cases := synthCases()
	reqs := make([]Request, len(cases))
	for i, c := range cases {
		reqs[i] = c.request(fmt.Sprintf("c%04d", i))
	}
	resps := py.Drive(t, nil, reqs)
	t.Logf("compared %d synth cases against Python %s", len(cases), py.Version)

	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, goErr := synth.BuildSyntheticHEIC(c.opts)
			resp := resps[i]
			switch {
			case resp.Err != "" && goErr != nil:
				if want := "ValueError: " + goErr.Error(); resp.Err != want {
					t.Errorf("error mismatch:\n  python %s\n  go     %s", resp.Err, want)
				}
			case resp.Err != "":
				t.Errorf("python failed (%s) but go succeeded", resp.Err)
			case goErr != nil:
				t.Errorf("go failed (%v) but python succeeded", goErr)
			default:
				DiffBytes(t, "synth output", resp.Out, got)
			}
		})
	}
}
