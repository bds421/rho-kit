package doctruth

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// HostOptions says which hosts a repository declares it dials and where its
// documentation lists them.
//
// The scan reads the https:// literals in the Go, cgo and embedded files the
// commands are built from (go list -deps, so no test file or test-only
// package). A host a dependency dials by default, or one dialed without an
// https:// literal, is not seen: name it in Unnamed.
type HostOptions struct {
	// Root is the repository root, the directory go list runs in.
	Root string
	// Packages are the go list patterns of the shipped commands. Default
	// ./cmd/...
	Packages []string
	// GOOS and Tags select the files the release builds (a linux-only file,
	// a release tag); empty GOOS is the host's.
	GOOS string
	Tags []string
	// Modules are the module paths whose files are scanned. Default the
	// module at Root.
	Modules []string
	// Declared are the hosts the repository declares it dials: a name, or
	// "*.domain" for any host under it.
	Declared []string
	// Unnamed are hosts dialed that no scanned literal names (a dependency's
	// default), each with where it comes from.
	Unnamed map[string]string
	// NotDialed are hosts the code names without dialing them (links it
	// shows, its user agent, examples in comments), each with why.
	NotDialed map[string]string
	// Document lists the declared hosts in backticks, relative to Root.
	Document string
	// Section is the heading (any level) of the part of Document that lists
	// them. Default "Outbound hosts".
	Section string
}

var urlHost = regexp.MustCompile(`https://([a-z0-9-]+(\.[a-z0-9-]+)+)`)

// reservedHost is a name reserved for examples and tests (RFC 2606, 6761).
var reservedHost = regexp.MustCompile(`(^|\.)(test|example|invalid|localhost)$|(^|\.)example\.(com|org|net)$`)

// RunHosts reports each finding of CheckHosts as a test error.
func RunHosts(t *testing.T, o HostOptions) {
	t.Helper()
	findings, err := CheckHosts(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Error(f)
	}
}

// CheckHosts returns each host the shipped code names that is neither
// declared nor named as not dialed, and each declared host the document's
// section does not list.
func CheckHosts(o HostOptions) ([]string, error) {
	if len(o.Packages) == 0 {
		o.Packages = []string{"./cmd/..."}
	}
	if o.Section == "" {
		o.Section = "Outbound hosts"
	}
	hosts, err := shippedHosts(o)
	if err != nil {
		return nil, err
	}
	for h, from := range o.Unnamed {
		hosts[h] = from
	}
	var findings []string
	for _, h := range sortedKeys(hosts) {
		if _, link := o.NotDialed[h]; !link && !declared(o.Declared, h) {
			findings = append(findings, fmt.Sprintf("%s (%s) is neither declared nor named as not dialed", h, hosts[h]))
		}
	}
	doc, err := os.ReadFile(filepath.Join(o.Root, o.Document)) // #nosec G304 -- the repository's own document
	if err != nil {
		return nil, err
	}
	section := headedSection(string(doc), o.Section)
	for _, h := range o.Declared {
		if !strings.Contains(section, "`"+h+"`") {
			findings = append(findings, fmt.Sprintf("%s's %q section does not list %s", o.Document, o.Section, h))
		}
	}
	return findings, nil
}

func declared(names []string, host string) bool {
	return slices.ContainsFunc(names, func(n string) bool {
		return n == host || (strings.HasPrefix(n, "*.") && strings.HasSuffix(host, n[1:]))
	})
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// shippedHosts are the hosts in the Go files of the packages the commands
// are built from, in o.Modules, each with a file that names it.
func shippedHosts(o HostOptions) (map[string]string, error) {
	modules := o.Modules
	if len(modules) == 0 {
		out, err := goCmd(o.Root, "", "list", "-m")
		if err != nil {
			return nil, err
		}
		modules = []string{strings.TrimSpace(out)}
	}
	args := []string{"list", "-deps", "-tags", strings.Join(o.Tags, ","), "-f", "{{if .Module}}{{.Module.Path}}\t{{.Dir}}\t{{join .GoFiles \" \"}} {{join .CgoFiles \" \"}} {{join .EmbedFiles \" \"}}{{end}}"}
	out, err := goCmd(o.Root, o.GOOS, append(args, o.Packages...)...)
	if err != nil {
		return nil, err
	}
	hosts := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 3 || !slices.Contains(modules, fields[0]) {
			continue
		}
		for _, f := range strings.Fields(fields[2]) {
			path := filepath.Join(fields[1], f)
			b, err := os.ReadFile(path) // #nosec G304 -- a source file go list names
			if err != nil {
				return nil, err
			}
			for _, m := range urlHost.FindAllStringSubmatch(string(b), -1) {
				if !reservedHost.MatchString(m[1]) {
					hosts[m[1]] = path
				}
			}
		}
	}
	return hosts, nil
}

// goCmd runs go in dir as the release builds: outside any workspace, where
// go list -m names the one module at dir.
func goCmd(dir, goos string, args ...string) (string, error) {
	cmd := exec.Command("go", args...) // #nosec G204 -- go over the repository root
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if goos != "" {
		cmd.Env = append(cmd.Env, "GOOS="+goos)
	}
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("go %s: %w: %s", strings.Join(args[:2], " "), err, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("go %s: %w", strings.Join(args[:2], " "), err)
	}
	return string(out), nil
}

// headedSection is the part of doc under a heading named name, up to the
// next heading of its level or higher outside a fenced block.
func headedSection(doc, name string) string {
	var section []string
	level, fenced := 0, false
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
		}
		hashes := len(line) - len(strings.TrimLeft(line, "#"))
		heading := !fenced && hashes > 0 && strings.HasPrefix(line[hashes:], " ")
		switch {
		case level == 0 && heading && strings.TrimSpace(line[hashes:]) == name:
			level = hashes
		case level > 0 && heading && hashes <= level:
			return strings.Join(section, "\n")
		case level > 0:
			section = append(section, line)
		}
	}
	return strings.Join(section, "\n")
}
