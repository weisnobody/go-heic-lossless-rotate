//go:build difftest

package difftest

// The Phase 2 gate: the Go core must be byte-, log-, and error-identical to
// upstream core.py on every input Python handles, and return a typed error
// wherever Python crashes or hangs (docs/COMPAT.md D2, D4-D7).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	heicrotate "github.com/pjg/go-heic-lossless-rotate"
	"github.com/pjg/go-heic-lossless-rotate/internal/craft"
	"github.com/pjg/go-heic-lossless-rotate/internal/pyfmt"
	"github.com/pjg/go-heic-lossless-rotate/internal/synth"
)

// goResult is one Go-side outcome, in the shape pydriver.py reports.
type goResult struct {
	out     []byte // compared when the op produces bytes
	hasOut  bool
	json    any // compared (as JSON) when the op produces a structured result
	hasJSON bool
	log     string
	err     error
}

type gateCase struct {
	group string
	req   Request
	run   func() goResult
}

// corpus accumulates gate cases and the named inputs they reference.
type corpus struct {
	inputs map[string][]byte
	cases  []gateCase
}

func newCorpus() *corpus { return &corpus{inputs: map[string][]byte{}} }

// input registers data (deduplicated by content) and returns its name.
func (c *corpus) input(data []byte) string {
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:12])
	if _, ok := c.inputs[name]; !ok {
		c.inputs[name] = bytes.Clone(data)
	}
	return name
}

func (c *corpus) add(group, op string, args map[string]any, inputArgs map[string]string, run func() goResult) {
	id := fmt.Sprintf("%s#%d", strings.ReplaceAll(group, " ", "_"), len(c.cases))
	c.cases = append(c.cases, gateCase{group, Request{ID: id, Op: op, Args: args, InputArgs: inputArgs}, run})
}

// Go-side equivalents of the pydriver.py ops.

func goApply(data []byte, turns int, sync bool) func() goResult {
	return func() goResult {
		var log bytes.Buffer
		out, err := heicrotate.ApplyRotation(data, turns, heicrotate.RotateOptions{NoExifSync: !sync, Log: &log})
		return goResult{out: out, hasOut: true, log: log.String(), err: err}
	}
}

func goReverse(data []byte, force bool) func() goResult {
	return func() goResult {
		var log bytes.Buffer
		out, err := heicrotate.ReverseRotation(data, heicrotate.ReverseOptions{IgnoreTamper: force, Log: &log})
		return goResult{out: out, hasOut: true, log: log.String(), err: err}
	}
}

func goVerify(data []byte, force bool) func() goResult {
	return func() goResult {
		var log bytes.Buffer
		err := heicrotate.VerifyReversible(data, heicrotate.ReverseOptions{IgnoreTamper: force, Log: &log})
		return goResult{log: log.String(), err: err}
	}
}

// infoDict renders Info as gather_info's dict (absent keys omitted).
func infoDict(i *heicrotate.Info) map[string]any {
	m := map[string]any{"current_quarter_turns": i.CurrentQuarterTurns, "has_provenance": i.HasProvenance}
	if !i.HasProvenance {
		return m
	}
	m["version"] = i.Version
	m["version_supported"] = i.VersionSupported
	m["has_iloc_snapshot"] = i.HasIlocSnapshot
	m["has_exif_snapshot"] = i.HasExifSnapshot
	if i.OriginalQuarterTurns != nil {
		m["original_quarter_turns"] = *i.OriginalQuarterTurns
		m["delta_quarter_turns"] = *i.DeltaQuarterTurns
	}
	if i.PCRC != nil {
		m["pcrc"] = *i.PCRC
	}
	if i.ToolVersion != nil {
		m["tool_version"] = []int{int(i.ToolVersion[0]), int(i.ToolVersion[1]), int(i.ToolVersion[2])}
	}
	return m
}

func goInfo(data []byte) func() goResult {
	return func() goResult {
		info, err := heicrotate.GatherInfo(data)
		if err != nil {
			return goResult{err: err}
		}
		text, code := heicrotate.FormatInfo(info, false)
		quietText, quietCode := heicrotate.FormatInfo(info, true)
		var qt any
		if quietText != "" {
			qt = quietText
		}
		return goResult{hasJSON: true, json: map[string]any{
			"info": infoDict(info), "text": text, "code": code, "quiet_text": qt, "quiet_code": quietCode,
		}}
	}
}

func goVerifyStructure(data []byte) func() goResult {
	return func() goResult { return goResult{err: heicrotate.VerifyStructure(data)} }
}

func goCurrentRotation(data []byte) func() goResult {
	return func() goResult {
		r, err := heicrotate.GetCurrentRotation(data)
		return goResult{json: r, hasJSON: err == nil, err: err}
	}
}

// addAllOps queues every core operation on data.
func (c *corpus) addAllOps(group string, data []byte) {
	in := map[string]string{"data": c.input(data)}
	for turn := range 4 {
		for _, sync := range []bool{true, false} {
			c.add(group, "apply_rotation", map[string]any{"delta_turns": turn, "sync_exif": sync}, in, goApply(data, turn, sync))
		}
	}
	for _, force := range []bool{false, true} {
		c.add(group, "reverse_rotation", map[string]any{"ignore_tamper": force}, in, goReverse(data, force))
		c.add(group, "verify_reversible", map[string]any{"ignore_tamper": force}, in, goVerify(data, force))
	}
	c.add(group, "info", nil, in, goInfo(data))
	c.add(group, "verify_structure", nil, in, goVerifyStructure(data))
	c.add(group, "get_current_rotation", nil, in, goCurrentRotation(data))
}

// addSweepOps queues the cheaper op subset used for the corruption sweep.
func (c *corpus) addSweepOps(group string, data []byte) {
	in := map[string]string{"data": c.input(data)}
	c.add(group, "apply_rotation", map[string]any{"delta_turns": 1, "sync_exif": true}, in, goApply(data, 1, true))
	// Both tamper settings: without the override nearly every corruption
	// stops at the CCRC check; with it, reverse's structural checks run.
	c.add(group, "reverse_rotation", map[string]any{"ignore_tamper": false}, in, goReverse(data, false))
	c.add(group, "reverse_rotation", map[string]any{"ignore_tamper": true}, in, goReverse(data, true))
	c.add(group, "info", nil, in, goInfo(data))
	c.add(group, "verify_structure", nil, in, goVerifyStructure(data))
}

func mustSynth(t *testing.T, o synth.Options) []byte {
	t.Helper()
	data, err := synth.BuildSyntheticHEIC(o)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustApply(t *testing.T, data []byte, turns int) []byte {
	t.Helper()
	out, err := heicrotate.ApplyRotation(data, turns, heicrotate.RotateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// synthFixtures: layout x irot (none, 0-3) x exif (none, end, middle) x extra mdat.
func synthFixtures(t *testing.T) map[string][]byte {
	out := map[string][]byte{}
	for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
		for _, irot := range []int{-1, 0, 1, 2, 3} {
			for _, exif := range []string{"none", "end", "middle"} {
				for _, extra := range []bool{false, true} {
					o := synth.DefaultOptions()
					o.Layout, o.ExtraMdat = layout, extra
					o.IncludeIrot, o.IrotValue = irot >= 0, max(irot, 0)
					if exif != "none" {
						o.IncludeExif, o.ExifOrientation, o.ExifPosition = true, 6, synth.ExifPosition(exif)
					}
					out[fmt.Sprintf("%s/irot=%d/exif=%s/extra=%v", layout, irot, exif, extra)] = mustSynth(t, o)
				}
			}
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// editProvenance re-encodes a rotated MdatFirst file's provenance record
// after mutate (the record follows mdat there, so resizing it needs no iloc
// patching).
func editProvenance(t *testing.T, rotated []byte, mutate func(*uint8, heicrotate.ProvenanceEntries)) []byte {
	t.Helper()
	s, err := heicrotate.LocateStructures(rotated)
	if err != nil || s.ProvenanceHdr == nil {
		t.Fatalf("no provenance: %v", err)
	}
	version, entries, err := heicrotate.ParseProvenance(rotated, *s.ProvenanceHdr)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&version, entries)
	newBox, err := heicrotate.BuildProvenanceBox(version, entries)
	if err != nil {
		t.Fatal(err)
	}
	p := s.ProvenanceHdr
	return bytes.Join([][]byte{rotated[:p.Start], newBox, rotated[p.ContentEnd:]}, nil)
}

func TestCoreMatchesPython(t *testing.T) {
	py := Upstream(t)
	fixtures := synthFixtures(t)

	// Phase A: have Python build the legacy-format records (v1 OPRP, and
	// TVER-stripped) with its own test helpers.
	pre := newCorpus()
	var v1Reqs, stripReqs []Request
	for _, name := range sortedKeys(fixtures) {
		orig := fixtures[name]
		rot := mustApply(t, orig, 1)
		if strings.HasPrefix(name, "mdat_first") {
			v1Reqs = append(v1Reqs, Request{ID: fmt.Sprintf("v1#%d", len(v1Reqs)), Op: "make_v1_style_record",
				InputArgs: map[string]string{"original": pre.input(orig), "rotated_v2": pre.input(rot)}})
		}
		stripReqs = append(stripReqs, Request{ID: fmt.Sprintf("strip#%d", len(stripReqs)), Op: "strip_provenance_tag",
			Args: map[string]any{"tag": "TVER"}, InputArgs: map[string]string{"data": pre.input(rot)}})
	}
	legacy := map[string][]byte{}
	for i, r := range py.Drive(t, pre.inputs, append(v1Reqs, stripReqs...)) {
		if r.Err != "" {
			t.Fatalf("building legacy record %d: %s", i, r.Err)
		}
		legacy[r.ID] = r.Out
	}

	// Phase B: the corpus.
	c := newCorpus()
	for _, name := range sortedKeys(fixtures) {
		orig := fixtures[name]
		c.addAllOps("synth originals", orig)
		for turn := range 4 {
			rot := mustApply(t, orig, turn)
			c.addAllOps("synth rotated", rot)
			c.addAllOps("synth rotated twice", mustApply(t, rot, 1))
		}
	}
	for _, id := range sortedKeys(legacy) {
		c.addAllOps("legacy records", legacy[id])
		c.addAllOps("legacy records rotated", mustApply(t, legacy[id], 1))
	}

	// Upstream's tamper scenarios, on every fixture they apply to.
	for _, name := range sortedKeys(fixtures) {
		rot := mustApply(t, fixtures[name], 1)
		s, err := heicrotate.LocateStructures(rot)
		if err != nil {
			t.Fatal(err)
		}
		ipco, _, _ := heicrotate.FindUniqueChild(rot, s.IprpHdr.ContentStart, s.IprpHdr.ContentEnd, "ipco", "'iprp'")
		children, _ := heicrotate.ParseIpcoChildren(rot, ipco.ContentStart, ipco.ContentEnd)
		for _, ch := range children {
			tampered := bytes.Clone(rot)
			tampered[ch.ContentStart] ^= 0xFF
			c.addAllOps("tamper ipco child", tampered)
		}
		ftyp := bytes.Clone(rot)
		ftyp[10] ^= 0xFF
		c.addAllOps("tamper ftyp", ftyp)
		if field, ok, _ := heicrotate.FindExifOrientationField(rot, s.MetaBodyStart, s.MetaHdr.ContentEnd); ok {
			exif := bytes.Clone(rot)
			exif[field.AbsOffset] ^= 0xFF
			c.addAllOps("tamper exif", exif)
		}
	}

	// Large payloads (size as a covered dimension).
	big := make([]byte, 2<<20)
	for i := range big {
		big[i] = byte(i)
	}
	for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
		for _, exif := range []bool{false, true} {
			o := synth.DefaultOptions()
			o.Layout, o.MdatPayload, o.IncludeExif, o.ExifPosition = layout, big, exif, synth.ExifMiddle
			orig := mustSynth(t, o)
			in := map[string]string{"data": c.input(orig)}
			c.add("large", "apply_rotation", map[string]any{"delta_turns": 1, "sync_exif": true}, in, goApply(orig, 1, true))
			rot := mustApply(t, orig, 1)
			rin := map[string]string{"data": c.input(rot)}
			c.add("large", "reverse_rotation", map[string]any{"ignore_tamper": false}, rin, goReverse(rot, false))
			c.add("large", "verify_reversible", map[string]any{"ignore_tamper": false}, rin, goVerify(rot, false))
			c.add("large", "info", nil, rin, goInfo(rot))
		}
	}

	// Crafted structures synth can't produce.
	for _, name := range sortedKeys(craft.Variants()) {
		data := craft.Build(craft.Variants()[name])
		c.addAllOps("crafted", data)
		if rot, err := heicrotate.ApplyRotation(data, 1, heicrotate.RotateOptions{}); err == nil {
			c.addAllOps("crafted rotated", rot)
			c.addAllOps("crafted rotated twice", mustApply(t, rot, 3))
		}
	}

	// Edited provenance records and odd top-level arrangements.
	for _, name := range []string{"mdat_first/irot=-1/exif=end/extra=false", "mdat_first/irot=1/exif=none/extra=true"} {
		rot := mustApply(t, fixtures[name], 1)
		edits := map[string]func(*uint8, heicrotate.ProvenanceEntries){
			"OMSZ 3 bytes":  func(_ *uint8, e heicrotate.ProvenanceEntries) { e["OMSZ"] = e["OMSZ"][:3] },
			"OIRT 2 bytes":  func(_ *uint8, e heicrotate.ProvenanceEntries) { e["OIRT"] = []byte{0, 0} },
			"no CCRC":       func(_ *uint8, e heicrotate.ProvenanceEntries) { delete(e, "CCRC") },
			"no PCRC":       func(_ *uint8, e heicrotate.ProvenanceEntries) { delete(e, "PCRC") },
			"PCRC 3 bytes":  func(_ *uint8, e heicrotate.ProvenanceEntries) { e["PCRC"] = e["PCRC"][:3] },
			"OIPF 2 bytes":  func(_ *uint8, e heicrotate.ProvenanceEntries) { e["OIPF"] = []byte{0, 0} },
			"no OIPF":       func(_ *uint8, e heicrotate.ProvenanceEntries) { delete(e, "OIPF") },
			"no OMSZ":       func(_ *uint8, e heicrotate.ProvenanceEntries) { delete(e, "OMSZ") },
			"no OIRT":       func(_ *uint8, e heicrotate.ProvenanceEntries) { delete(e, "OIRT") },
			"TVER 2 bytes":  func(_ *uint8, e heicrotate.ProvenanceEntries) { e["TVER"] = []byte{1, 7} },
			"unknown tag":   func(_ *uint8, e heicrotate.ProvenanceEntries) { e["ZZZZ"] = []byte("future") },
			"version 0":     func(v *uint8, _ heicrotate.ProvenanceEntries) { *v = 0 },
			"version 3":     func(v *uint8, _ heicrotate.ProvenanceEntries) { *v = 3 },
			"OEXO 1 byte":   func(_ *uint8, e heicrotate.ProvenanceEntries) { e["OEXO"] = []byte{1} },
			"OILC truncate": func(_ *uint8, e heicrotate.ProvenanceEntries) { e["OILC"] = e["OILC"][:4] },
		}
		for _, edit := range sortedKeys(edits) {
			c.addAllOps("provenance edits", editProvenance(t, rot, edits[edit]))
		}
		// uuid box moved in front of meta; zero-size 64-bit box appended.
		s, _ := heicrotate.LocateStructures(rot)
		p := s.ProvenanceHdr
		moved := bytes.Join([][]byte{rot[:s.MetaHdr.Start], rot[p.Start:p.ContentEnd], rot[s.MetaHdr.Start:p.Start], rot[p.ContentEnd:]}, nil)
		c.addAllOps("odd layout", moved)
		// Python loops forever on this (COMPAT D6): a short timeout, and only
		// the sweep ops, so the gate doesn't spend minutes waiting on purpose.
		zero := append(append(bytes.Clone(fixtures[name]), 0, 0, 0, 1, 'f', 'r', 'e', 'e'), make([]byte, 16)...)
		before := len(c.cases)
		c.addSweepOps("zero-size box", zero)
		for i := before; i < len(c.cases); i++ {
			c.cases[i].req.Timeout = 0.5
		}
	}

	// Edits to a v1 record's OPRP snapshot, and an OIPF that clears the wide
	// flag on a file whose other associations need it.
	if len(v1Reqs) > 0 {
		v1 := legacy[v1Reqs[0].ID]
		for name, oprp := range map[string][]byte{"OPRP truncated": nil, "OPRP not a box": []byte("garbage!garbage!")} {
			c.addAllOps("provenance edits", editProvenance(t, v1, func(_ *uint8, e heicrotate.ProvenanceEntries) {
				if oprp == nil {
					e["OPRP"] = e["OPRP"][:12]
				} else {
					e["OPRP"] = oprp
				}
			}))
			_ = name
		}
	}
	wideOpts := craft.Variants()["mdat_first/wide_high_assoc"]
	wideRot := mustApply(t, craft.Build(wideOpts), 1)
	c.addAllOps("provenance edits", editProvenance(t, wideRot, func(_ *uint8, e heicrotate.ProvenanceEntries) {
		e["OIPF"] = []byte{0, 0, 0}
	}))

	// Top-level oddities: a truncated 64-bit header at EOF, a zero-size
	// 64-bit box inside meta (Python hangs: D6), and a meta too small for
	// its own FullBox header.
	base := fixtures["meta_first/irot=-1/exif=none/extra=false"]
	c.addAllOps("odd layout", append(bytes.Clone(base), 0, 0, 0, 1, 'f', 'r', 'e', 'e', 0, 0, 0, 0))
	ftyp, _, _ := heicrotate.FindChild(base, 0, len(base), "ftyp")
	tiny := append(bytes.Clone(base[:ftyp.ContentEnd]), 0, 0, 0, 10, 'm', 'e', 't', 'a', 0, 0)
	c.addAllOps("odd layout", tiny)
	{
		s, _ := heicrotate.LocateStructures(base)
		m := s.MetaHdr
		inner := bytes.Join([][]byte{base[m.ContentStart:m.ContentEnd], {0, 0, 0, 1, 'f', 'r', 'e', 'e'}, make([]byte, 8)}, nil)
		withZero := bytes.Join([][]byte{base[:m.Start], craft.Box("meta", inner), base[m.ContentEnd:]}, nil)
		before := len(c.cases)
		c.addSweepOps("zero-size box", withZero)
		for i := before; i < len(c.cases); i++ {
			c.cases[i].req.Timeout = 0.5
		}
	}

	// Corruption sweep: every byte x 3 XOR masks over 8 files.
	for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
		for _, irot := range []bool{false, true} {
			o := synth.DefaultOptions()
			o.Layout, o.IncludeIrot, o.IncludeExif, o.ExifPosition = layout, irot, true, synth.ExifMiddle
			orig := mustSynth(t, o)
			for _, base := range [][]byte{orig, mustApply(t, orig, 1)} {
				for i := range base {
					for _, m := range []byte{0xFF, 0x01, 0x80} {
						bad := bytes.Clone(base)
						bad[i] ^= m
						c.addSweepOps("corruption sweep", bad)
					}
				}
			}
		}
	}

	resps := py.Drive(t, c.inputs, reqsOf(c.cases))
	t.Logf("%d cases over %d distinct inputs, Python %s", len(c.cases), len(c.inputs), py.Version)
	reportGate(t, c.cases, resps)
}

func reqsOf(cases []gateCase) []Request {
	reqs := make([]Request, len(cases))
	for i, gc := range cases {
		reqs[i] = gc.req
	}
	return reqs
}

// reportGate compares every case and reports mismatches grouped, with up to
// 5 details per group, plus how many allowed divergences each COMPAT entry
// accounted for.
func reportGate(t *testing.T, cases []gateCase, resps []Response) {
	t.Helper()
	type groupStats struct {
		n, bad  int
		compat  map[string]int
		details []string
	}
	stats := map[string]*groupStats{}
	var order []string
	for i, gc := range cases {
		st := stats[gc.group]
		if st == nil {
			st = &groupStats{compat: map[string]int{}}
			stats[gc.group] = st
			order = append(order, gc.group)
		}
		st.n++
		if problem, compat := compareCase(gc.run(), resps[i]); problem != "" {
			st.bad++
			if len(st.details) < 5 {
				st.details = append(st.details, fmt.Sprintf("%s %s %v\n%s", gc.req.ID, gc.req.Op, gc.req.Args, problem))
			}
		} else if compat != "" {
			st.compat[compat]++
		}
	}
	for _, g := range order {
		st := stats[g]
		t.Run(strings.ReplaceAll(g, " ", "_"), func(t *testing.T) {
			t.Logf("%d cases, allowed divergences: %v", st.n, st.compat)
			if st.bad > 0 {
				t.Errorf("%d of %d cases differ from Python:\n%s", st.bad, st.n, strings.Join(st.details, "\n---\n"))
			}
		})
	}
}

// compareCase returns a description of how go differs from py ("" if it
// doesn't) and the COMPAT ID of an allowed divergence.
func compareCase(g goResult, py Response) (problem, compat string) {
	ok, compat := ClassifyErr(py.Err, g.err)
	if !ok {
		return fmt.Sprintf("  python err: %q\n  go err:     %q", py.Err, goErrString(g.err)), ""
	}
	if compat != ErrClassMatch {
		return "", compat // Python crashed/hung, or a deliberate Go refusal
	}
	if py.Log != g.log {
		return fmt.Sprintf("  log differs:\n  python:\n%s\n  go:\n%s", py.Log, g.log), ""
	}
	if py.Err != "" {
		return "", ""
	}
	if g.hasOut && !bytes.Equal(py.Out, g.out) {
		return "  output bytes: " + describeDiff(py.Out, g.out), ""
	}
	if g.hasJSON {
		goJSON, err := json.Marshal(g.json)
		if err != nil {
			return "  marshal go result: " + err.Error(), ""
		}
		var pv, gv any
		if json.Unmarshal(py.JSON, &pv) != nil || json.Unmarshal(goJSON, &gv) != nil || !reflect.DeepEqual(pv, gv) {
			return fmt.Sprintf("  result differs:\n  python %s\n  go     %s", py.JSON, goJSON), ""
		}
	}
	return "", ""
}

// TestPyfmtMatchesPython checks internal/pyfmt against the live
// interpreter over a wide value table.
func TestPyfmtMatchesPython(t *testing.T) {
	py := Upstream(t)
	type check struct {
		req  Request
		want string
	}
	var checks []check
	add := func(kind string, args map[string]any, raw []byte, goText string) {
		a := map[string]any{"kind": kind}
		for k, v := range args {
			a[k] = v
		}
		r := Request{ID: fmt.Sprintf("repr#%d", len(checks)), Op: "repr", Args: a}
		if raw != nil {
			r.BytesArgs = map[string][]byte{"raw": raw}
		}
		checks = append(checks, check{r, goText})
	}
	for b := range 256 {
		one := []byte{byte(b)}
		add("bytes", nil, one, pyfmt.BytesRepr(one))
		add("decode_ascii_replace", nil, one, pyfmt.DecodeASCIIReplace(one))
		s := pyfmt.DecodeASCIIReplace(one)
		add("str", map[string]any{"value": s}, nil, pyfmt.StrRepr(s))
	}
	for _, s := range []string{"", "ispe", "it's", `say "hi"`, `both ' and "`, `back\slash`, "is�\x00", "tab\there", "  \U0001F600"} {
		add("str", map[string]any{"value": s}, nil, pyfmt.StrRepr(s))
		add("bytes", nil, []byte(s), pyfmt.BytesRepr([]byte(s)))
	}
	for _, v := range []uint32{0, 1, 0xab, 0xdeadbeef, 0xffffffff} {
		add("hex08", map[string]any{"value": v}, nil, pyfmt.Hex08(v))
	}
	add("enumerate", map[string]any{"value": []string{"ispe", "irot", "is�\x00", "it's"}}, nil,
		pyfmt.EnumerateList([]string{"ispe", "irot", "is�\x00", "it's"}))
	add("enumerate", map[string]any{"value": []string{}}, nil, pyfmt.EnumerateList(nil))
	add("int_bool_list", map[string]any{"value": [][]any{{1, true}, {130, false}}}, nil,
		pyfmt.IntBoolList([]pyfmt.IntBool{{Int: 1, Bool: true}, {Int: 130, Bool: false}}))

	reqs := make([]Request, len(checks))
	for i, c := range checks {
		reqs[i] = c.req
	}
	bad := 0
	for i, r := range py.Drive(t, nil, reqs) {
		var pyText string
		if r.Err != "" || json.Unmarshal(r.JSON, &pyText) != nil {
			t.Fatalf("%s: python error %q", r.ID, r.Err)
		}
		if pyText != checks[i].want {
			if bad++; bad <= 10 {
				t.Errorf("%s %v: python %q, go %q", r.ID, checks[i].req.Args, pyText, checks[i].want)
			}
		}
	}
	t.Logf("compared %d formatting cases", len(checks))
}
