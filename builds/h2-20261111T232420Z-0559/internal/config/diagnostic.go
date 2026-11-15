package config

import "fmt"

// Diagnostic is one problem found in a configuration file. Key is the
// dotted path of the setting that is wrong, for example
// "zones[0].rooms[1].target".
type Diagnostic struct {
	Line   int
	Column int
	Key    string
	Msg    string
}

// Error implements the error interface.
func (d Diagnostic) Error() string {
	if d.Line > 0 {
		return fmt.Sprintf("line %d: %s: %s", d.Line, d.Key, d.Msg)
	}
	return fmt.Sprintf("%s: %s", d.Key, d.Msg)
}

// Diagnostics formats a list of errors for a human reader, one per line.
func Diagnostics(errs []error) string {
	out := ""
	for _, err := range errs {
		out += err.Error() + "\n"
	}
	return out
}
