package cli

import (
	"os"
	"path/filepath"
	"strings"
)

// CanonicalName is the command's full name. The short alias `kg` is installed
// alongside it as a symlink.
const CanonicalName = "kgenesis"

// ShortName is the alias installed next to the canonical binary.
const ShortName = "kg"

// binaryName is how this process was invoked, without any directory or .exe
// suffix. Every hint the CLI prints is built from it, so a command suggested by
// `kg` can be pasted back as `kg` rather than sending the reader to look up a
// name they did not type.
var binaryName = resolveBinaryName()

func resolveBinaryName() string {
	if len(os.Args) == 0 {
		return CanonicalName
	}

	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")

	// Under `go test` and similar, argv[0] is the test binary rather than a name
	// any operator would type.
	if name == "" || name == "." || name == string(filepath.Separator) || strings.HasSuffix(name, ".test") {
		return CanonicalName
	}
	return name
}

// invoke renders an invocation of this CLI for help text and error hints.
func invoke(args string) string {
	if args == "" {
		return binaryName
	}
	return binaryName + " " + args
}

// hintColumn is where the description starts in the two-column hints the CLI
// prints. It leaves room for the longest invocation at the canonical name.
const hintColumn = 30

// pad right-pads an invocation so the descriptions beside it line up. The width
// is fixed rather than derived from the binary name, so the same help text reads
// identically whether it came from `kgenesis` or `kg`; an invocation longer than
// the column simply gets a single space.
func pad(invocation string) string {
	if len(invocation) >= hintColumn {
		return invocation + " "
	}
	return invocation + strings.Repeat(" ", hintColumn-len(invocation))
}
