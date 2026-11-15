package config

import (
	"fmt"
	"os"
)

// Load reads, parses and validates the configuration file at path. It returns
// the configuration together with every problem found; the configuration is
// only usable when the error list is empty.
func Load(path string) (*Config, []error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, []error{fmt.Errorf("%s: %w", path, err)}
	}
	return Parse(data, path)
}

// Parse parses and validates configuration data. path is only used to label
// the result.
func Parse(data []byte, path string) (*Config, []error) {
	d := newDecoder(path)
	d.decode(data)
	cfg := d.build()
	if len(d.errs) == 0 {
		return cfg, nil
	}
	return cfg, d.errs
}
