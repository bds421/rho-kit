package app

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// LoadSeedJSON reads a JSON file at the given path and unmarshals it into target.
// Used inside WithSeed callbacks to load seed data from the path provided by --seed.
func LoadSeedJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read seed file %s: %w", path, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse seed file %s: %w", path, err)
	}
	return nil
}

// parseSeedFlag checks os.Args for "--seed <path>" and returns the path.
// Returns empty string if the flag is not present.
//
// Note: this uses manual os.Args parsing rather than the flag package, so
// --seed won't appear in --help output. This is intentional — seed is a
// deployment-time operation, not a user-facing flag.
func parseSeedFlag() string {
	for i, arg := range os.Args {
		if arg == "--seed" && i+1 < len(os.Args) {
			path := os.Args[i+1]
			if path == "" {
				panic("app: --seed requires a non-empty path")
			}
			return path
		}
		if strings.HasPrefix(arg, "--seed=") {
			path := strings.TrimPrefix(arg, "--seed=")
			if path == "" {
				panic("app: --seed= requires a non-empty path")
			}
			return path
		}
	}
	return ""
}
