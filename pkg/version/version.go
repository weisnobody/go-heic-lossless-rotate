// Package version holds the upstream version this port implements and the Go
// port's own release version.
package version

import (
	"runtime"
	"strconv"
)

// Version is the upstream heic-lossless-rotate version this port implements
// (← _version.py). It is written into the provenance record's TVER entry and
// reported by -V and info, exactly as the Python implementation does, so
// output files stay byte-identical to upstream's.
const Version = "1.7.1"

// GoRevision counts Go-port releases for this Version. It resets to 1 on
// each upstream version bump. See docs/DESIGN.md § Go port version and
// release tags.
const GoRevision = 1

// GoVersion is the Go port's own version: Version plus semver build
// metadata, e.g. "1.7.1+go.1". It never reaches a file - only -G output and
// release names.
var GoVersion = Version + "+go." + strconv.Itoa(GoRevision)

// GoVersionString returns the text printed by -G/--goversion: the Go
// port's version, the upstream version it implements, and the Go toolchain
// it was built with.
func GoVersionString() string {
	return "heic-lossless-rotate (Go port) " + GoVersion + "\n" +
		"implements upstream heic-lossless-rotate " + Version + "\n" +
		"built with " + runtime.Version()
}
