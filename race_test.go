//go:build race

package heicrotate

// raceEnabled skips allocation-budget tests: the race detector adds its own
// allocations.
const raceEnabled = true
