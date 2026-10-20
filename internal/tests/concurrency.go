package tests

import (
	"fmt"
	"sync"
	"testing"

	"github.com/pjg/go-heic-lossless-rotate/internal/synth"
)

// ConcurrentWorkers is how many goroutines the concurrency tests run at
// once.
const ConcurrentWorkers = 16

// ConcurrencyInputs returns the inputs the concurrency tests share between
// goroutines: both layouts, the fast and slow 'irot' paths, an Exif patch
// inside mdat, a twice-rotated file, a v1 record, and two that fail.
func ConcurrencyInputs(t testing.TB) map[string][]byte {
	t.Helper()
	payload := CyclicPayload(64 << 10)
	metaFirst := MustSynth(t, func(o *synth.Options) {
		o.Layout, o.IncludeExif, o.ExifPosition, o.MdatPayload = synth.MetaFirst, true, synth.ExifMiddle, payload
	})
	mdatFirst := MustSynth(t, func(o *synth.Options) {
		o.Layout, o.IncludeIrot, o.IrotValue, o.MdatPayload = synth.MdatFirst, true, 1, payload
	})
	rotated := MustApply(t, MustApply(t, metaFirst, 1), 2)
	return map[string][]byte{
		"meta-first with Exif":    metaFirst,
		"mdat-first with irot":    mdatFirst,
		"rotated twice":           rotated,
		"v1 record":               MakeV1StyleRecord(t, mdatFirst, MustApply(t, mdatFirst, 1)),
		"truncated (fails)":       rotated[:len(rotated)/2],
		"not a HEIF file (fails)": []byte("not a HEIF file at all"),
	}
}

// Parallel runs f on n goroutines, released together so their work
// overlaps as much as possible, and waits for all of them.
func Parallel(n int, f func(worker int)) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	for w := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			f(w)
		}()
	}
	close(start)
	wg.Wait()
}

// ErrText is err's type and message ("" for nil), for comparing outcomes.
func ErrText(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%T: %v", err, err)
}
