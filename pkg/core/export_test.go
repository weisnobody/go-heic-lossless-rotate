package core

// Unexported names that package core's external tests (package core_test)
// need.

var (
	VersionTuple     = versionTuple
	ProvenanceErrorf = provenanceErrorf
	BoxParseErrorf   = boxParseErrorf
	ExtractIrotState = extractIrotState
	MakeBox          = makeBox

	// ReconstructedChunks is the seam upstream monkeypatches; tests replace
	// *ReconstructedChunks.
	ReconstructedChunks = &reconstructedChunks
)
