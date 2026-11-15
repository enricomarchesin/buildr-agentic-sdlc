// Package buildinfo holds the version stamped into the binary.
package buildinfo

// These values are overridden at build time with -ldflags -X.
var (
	// Version is the heatctl release.
	Version = "1.0.0"
	// Commit is the git revision the binary was built from.
	Commit = ""
	// BuildDate is the time the binary was built.
	BuildDate = ""
)

// String returns the full version line.
func String() string {
	s := "heatctl " + Version
	if Commit != "" {
		s += " (commit " + Commit
		if BuildDate != "" {
			s += ", built " + BuildDate
		}
		s += ")"
	}
	return s
}
