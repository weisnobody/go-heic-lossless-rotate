//go:build race

package tests

// RaceEnabled skips allocation-budget tests: the race detector adds its own
// allocations.
const RaceEnabled = true
