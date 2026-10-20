// Package files rotates, reverses, and inspects HEIC/HEIF files on disk,
// doing everything the heic-rotate command does (Go-only).
//
// [RotateFile], [ReverseFile], and [InfoFile] are the rotate, reverse, and
// info commands as library calls. Rotate and reverse:
//
//   - stream the input (only its metadata is held in memory; see package
//     stream);
//   - check the output's box structure, and for rotate, reverse the output
//     in memory to confirm it reconstructs the original exactly - a failed
//     check is an error and nothing is written;
//   - choose a default output name when output is "" ([DefaultRotateOutput],
//     [DefaultReverseOutput]);
//   - refuse to overwrite an existing output unless [FileOptions].Force is
//     set, with an *[OutputExistsError] (errors.Is(err, fs.ErrExist));
//   - write the output atomically.
//
// [FileOptions] holds the command-line flags: Force (-f), DryRun
// (--dry-run), NoExifSync (--no-exif-sync), and IgnoreTamper
// (--ignore-tamper-check). Log receives the command's informational lines;
// pass os.Stdout to print exactly what heic-rotate prints, or leave it nil
// for silence (-q).
//
// Rotation counts quarter turns counter-clockwise, added to the file's
// current rotation; see package core.
//
// # Writes
//
// Outputs are written to a temporary file in the same directory and
// renamed into place, so a crash or full disk never leaves a truncated
// file, and rotating a file onto itself (input == output, with Force) can't
// destroy the original. Otherwise writes behave like the Python tool's: they
// go through symlinks, an overwritten file keeps its permissions (and, best
// effort, its owner), new files get 0666 minus the umask, and a file with
// other hard links is updated in place so the links still share it.
//
// # Errors
//
// Errors are package core's *core.BoxParseError and *core.ProvenanceError,
// an *[OutputExistsError], or file-system errors (*fs.PathError and
// similar).
//
// # Concurrency
//
// Every function is safe to call from multiple goroutines at once, including
// on the same input. On Unix, concurrent writes to the same output (with
// Force) leave one complete output, and readers never see a partial file.
// That case isn't tested on Windows, where replacing a file that is open
// elsewhere can fail.
package files
