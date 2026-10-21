// Command heic-rotate losslessly and reversibly rotates HEIC/HEIF images by editing
// container metadata only. It is a drop-in replacement for the Python
// heic-lossless-rotate CLI; heic-rotate and heic-lossless-rotate are the
// same program under two names, as with the pip-installed tool. Run with -h
// for usage.
package main

import (
	"os"

	"github.com/weisnobody/go-heic-lossless-rotate/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args, os.Stdout, os.Stderr))
}
