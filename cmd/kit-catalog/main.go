// Command kit-catalog audits a service tree (one go.mod) or a
// directory of services (many go.mods) and emits a manifest of
// which rho-kit packages each composes.
//
// # Why this exists
//
// A team running 30 kit-using services cannot easily answer
// fleet questions like:
//
//   - "Which services use signedrequest?"  (target a kit CVE)
//   - "Which services pin pgstore vs redisstore?" (DB migration)
//   - "Which still use messaging.MemoryBroker outside tests?"
//   - "Which services are on httpx v2.0.0 vs v2.0.5?" (upgrade plan)
//
// kit-catalog answers all of these by walking each service's
// go.{mod,sum} + *.go files and emitting a structured manifest.
//
// # Usage
//
//	# Audit a single service (cwd holds go.mod):
//	kit-catalog
//
//	# Audit a fleet directory (every immediate subdirectory with
//	# a go.mod is a service):
//	kit-catalog -fleet ../services
//
//	# Filter to services that import a specific package:
//	kit-catalog -fleet ../services -uses github.com/bds421/rho-kit/httpx/v2/middleware/signedrequest
//
//	# Output formats: json (default), table, csv
//	kit-catalog -fleet ../services -format table
//
// # Manifest shape
//
// JSON output:
//
//	{
//	  "scanned_at":      "2026-05-16T12:34:56Z",
//	  "service_count":   12,
//	  "services": [
//	    {
//	      "module":       "github.com/example/orders-api",
//	      "path":         "../services/orders-api",
//	      "kit_packages": [
//	        "github.com/bds421/rho-kit/httpx/v2",
//	        "github.com/bds421/rho-kit/data/v2/idempotency/pgstore",
//	        ...
//	      ],
//	      "kit_versions": {
//	        "github.com/bds421/rho-kit/httpx/v2": "v2.0.3",
//	        ...
//	      }
//	    },
//	    ...
//	  ]
//	}
//
// # Exit codes
//
//	0 — manifest emitted successfully
//	1 — no services found at the supplied path
//	2 — CLI / discovery error (no go.mod, unreadable file, etc.)
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const kitImportPrefix = "github.com/bds421/rho-kit/"

type service struct {
	Module      string            `json:"module"`
	Path        string            `json:"path"`
	KitPackages []string          `json:"kit_packages"`
	KitVersions map[string]string `json:"kit_versions,omitempty"`
}

type manifest struct {
	ScannedAt    string    `json:"scanned_at"`
	ServiceCount int       `json:"service_count"`
	Services     []service `json:"services"`
}

func main() {
	var (
		fleet      string
		usesFilter string
		format     string
	)
	flag.StringVar(&fleet, "fleet", "", "Scan every immediate subdirectory with a go.mod (default: scan the current directory as a single service).")
	flag.StringVar(&usesFilter, "uses", "", "Filter to services that import this kit package (exact import path).")
	flag.StringVar(&format, "format", "json", "Output format: json | table | csv.")
	flag.Parse()

	var services []service
	if fleet != "" {
		s, err := scanFleet(fleet)
		if err != nil {
			fail("scan fleet %q: %v", fleet, err)
		}
		services = s
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			fail("getwd: %v", err)
		}
		s, err := scanService(cwd)
		if err != nil {
			fail("scan service %q: %v", cwd, err)
		}
		if s == nil {
			fmt.Fprintln(os.Stderr, "kit-catalog: no go.mod found at .; pass -fleet <dir> to scan multiple services")
			os.Exit(1)
		}
		services = []service{*s}
	}

	if usesFilter != "" {
		services = filterByImport(services, usesFilter)
	}

	if len(services) == 0 {
		fmt.Fprintln(os.Stderr, "kit-catalog: no services matched")
		os.Exit(1)
	}

	m := manifest{
		ScannedAt:    time.Now().UTC().Format(time.RFC3339),
		ServiceCount: len(services),
		Services:     services,
	}
	switch format {
	case "json":
		emitJSON(m)
	case "table":
		emitTable(m)
	case "csv":
		emitCSV(m)
	default:
		fail("unknown -format %q (json|table|csv)", format)
	}
}

func scanFleet(root string) ([]service, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []service
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		s, err := scanService(dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", dir, err)
		}
		if s != nil {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Module < out[j].Module })
	return out, nil
}

// scanService inspects one directory containing a go.mod and
// returns the kit composition manifest. Returns (nil, nil) when
// the directory has no go.mod — caller decides whether that's
// fatal.
func scanService(dir string) (*service, error) {
	modPath := filepath.Join(dir, "go.mod")
	modBytes, err := os.ReadFile(modPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	moduleName, versions := parseGoMod(string(modBytes))

	imports, err := collectKitImports(dir)
	if err != nil {
		return nil, err
	}

	pkgList := make([]string, 0, len(imports))
	for p := range imports {
		pkgList = append(pkgList, p)
	}
	sort.Strings(pkgList)

	// Filter the version map to only the modules actually used by
	// the service's go-imports (drops indirect deps the service
	// doesn't directly compose).
	usedVersions := map[string]string{}
	for _, pkg := range pkgList {
		mod := moduleForImport(pkg, versions)
		if mod != "" {
			usedVersions[mod] = versions[mod]
		}
	}

	return &service{
		Module:      moduleName,
		Path:        dir,
		KitPackages: pkgList,
		KitVersions: usedVersions,
	}, nil
}

var (
	modulePattern = regexp.MustCompile(`(?m)^module\s+([^\s]+)`)
	// requirePattern matches both single-line `require <path> <ver>`
	// and indented entries inside a `require ( ... )` block.
	requirePattern = regexp.MustCompile(`(?m)^(?:require\s+)?\s*(github\.com/bds421/rho-kit/[^\s]+)\s+(v[^\s]+)`)
)

// parseGoMod extracts the service module name and a map of every
// kit module pin -> version string from go.mod text. Indirect
// require blocks are honored (the regex matches indented lines).
func parseGoMod(content string) (moduleName string, versions map[string]string) {
	versions = map[string]string{}
	if m := modulePattern.FindStringSubmatch(content); len(m) == 2 {
		moduleName = m[1]
	}
	for _, m := range requirePattern.FindAllStringSubmatch(content, -1) {
		if len(m) == 3 {
			versions[m[1]] = m[2]
		}
	}
	return moduleName, versions
}

// importPattern captures import paths from a Go source file.
// Handles both single-line `import "..."` and grouped
// `import ( "..." )`.
var importPattern = regexp.MustCompile(`"(github\.com/bds421/rho-kit/[^"]+)"`)

// collectKitImports walks every non-test .go file under root and
// returns the set of kit import paths the source references.
func collectKitImports(root string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip vendor + hidden dirs.
			name := d.Name()
			if path != root && (name == "vendor" || strings.HasPrefix(name, ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Skip test files by default — they exercise the kit but
		// don't reflect production composition.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, m := range importPattern.FindAllStringSubmatch(string(data), -1) {
			if len(m) == 2 {
				out[m[1]] = struct{}{}
			}
		}
		return nil
	})
	return out, err
}

// moduleForImport finds the kit MODULE path (the prefix in
// go.mod's require block) that an IMPORT path resolves under.
// E.g. import "github.com/bds421/rho-kit/httpx/v2/middleware/signedrequest"
// resolves to module "github.com/bds421/rho-kit/httpx/v2".
//
// We pick the longest prefix match so e.g. a service requiring
// both .../data/v2 and .../data/idempotency/redisstore/v2
// attributes each import to the right module.
func moduleForImport(importPath string, versions map[string]string) string {
	var best string
	for mod := range versions {
		if strings.HasPrefix(importPath, mod+"/") || importPath == mod {
			if len(mod) > len(best) {
				best = mod
			}
		}
	}
	return best
}

func filterByImport(services []service, target string) []service {
	out := make([]service, 0, len(services))
	for _, s := range services {
		for _, p := range s.KitPackages {
			if p == target {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

func emitJSON(m manifest) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(m)
}

func emitTable(m manifest) {
	fmt.Printf("kit-catalog: %d service(s) scanned at %s\n\n", m.ServiceCount, m.ScannedAt)
	for _, s := range m.Services {
		fmt.Printf("== %s\n   path: %s\n   kit packages (%d):\n", s.Module, s.Path, len(s.KitPackages))
		for _, p := range s.KitPackages {
			fmt.Printf("     - %s\n", p)
		}
		if len(s.KitVersions) > 0 {
			fmt.Println("   kit module pins:")
			keys := make([]string, 0, len(s.KitVersions))
			for k := range s.KitVersions {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Printf("     - %s @ %s\n", k, s.KitVersions[k])
			}
		}
		fmt.Println()
	}
}

func emitCSV(m manifest) {
	w := csv.NewWriter(os.Stdout)
	defer w.Flush()
	_ = w.Write([]string{"service_module", "service_path", "kit_package", "kit_module", "kit_version"})
	for _, s := range m.Services {
		for _, pkg := range s.KitPackages {
			mod := moduleForImport(pkg, s.KitVersions)
			_ = w.Write([]string{s.Module, s.Path, pkg, mod, s.KitVersions[mod]})
		}
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "kit-catalog: "+format+"\n", args...)
	os.Exit(2)
}
