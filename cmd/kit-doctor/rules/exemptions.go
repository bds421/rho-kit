package rules

import (
	"bufio"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// kitFactoryExemptions lists module paths that are the canonical kit
// implementations of the safe-construction helpers their rules push
// callers toward. Exempting them lets kit-doctor run cleanly against
// rho-kit itself without weakening enforcement for service repos.
//
// Match is by exact go.mod module path. The keys are rule names; the
// values are the modules where the rule's "unsafe" pattern is the
// safe factory the rule exists to recommend.
var kitFactoryExemptions = map[string]map[string]struct{}{
	"http-server-direct-construction": {
		"github.com/bds421/rho-kit/httpx/v2": {},
	},
	"default-http-client": {
		"github.com/bds421/rho-kit/httpx/v2":            {},
		"github.com/bds421/rho-kit/httpx/v2/budget":     {},
		"github.com/bds421/rho-kit/httpx/v2/sign":       {},
		"github.com/bds421/rho-kit/security/v2/jwtutil": {},
	},
	"http-server-error-log": {
		"github.com/bds421/rho-kit/app/v2": {},
	},
}

// inlineSuppressionPrefix is the marker callers can place on the same
// line or the immediately preceding comment to opt out of a specific
// rule. Format:
//
//	// kit-doctor:allow <rule-name> [reason="..."]
//
// Suppressions are scoped to a single line and a single rule. They
// must be deliberate; the linter never matches by substring elsewhere
// in the file.
const inlineSuppressionPrefix = "kit-doctor:allow"

// isKitFactoryExempt reports whether the file at filename lives in a
// module that is on the per-rule allowlist for ruleName. The result is
// cached per process to keep filesystem walks off the per-finding hot
// path.
func isKitFactoryExempt(ruleName, filename string) bool {
	mods, ok := kitFactoryExemptions[ruleName]
	if !ok || len(mods) == 0 {
		return false
	}
	mod := moduleAtPath(filename)
	if mod == "" {
		return false
	}
	_, ok = mods[mod]
	return ok
}

// moduleCache memoises go.mod lookups per directory.
var moduleCache = map[string]string{}

// moduleAtPath walks upward from filename until it finds a go.mod,
// returning its module path. Returns "" if no go.mod is found before
// the filesystem root.
func moduleAtPath(filename string) string {
	dir := filepath.Dir(filename)
	if cached, ok := moduleCache[dir]; ok {
		return cached
	}
	cur := dir
	for {
		modPath := filepath.Join(cur, "go.mod")
		if mod := readModuleLine(modPath); mod != "" {
			moduleCache[dir] = mod
			return mod
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			moduleCache[dir] = ""
			return ""
		}
		cur = parent
	}
}

func readModuleLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		mod := strings.TrimSpace(strings.TrimPrefix(line, "module"))
		mod = strings.Trim(mod, "\"")
		return mod
	}
	return ""
}

// hasInlineSuppression reports whether a `// kit-doctor:allow <rule>`
// marker appears on the same line as findingLine, or as a leading
// comment line directly above it. The marker must name ruleName
// exactly as the first whitespace-delimited token after the prefix.
func hasInlineSuppression(fset *token.FileSet, file *ast.File, ruleName string, findingLine int) bool {
	if file == nil || fset == nil {
		return false
	}
	for _, group := range file.Comments {
		for _, c := range group.List {
			pos := fset.Position(c.Slash)
			if pos.Line != findingLine && pos.Line != findingLine-1 {
				continue
			}
			if matchesSuppression(c.Text, ruleName) {
				return true
			}
		}
	}
	return false
}

func matchesSuppression(comment, ruleName string) bool {
	body := strings.TrimSpace(strings.TrimPrefix(comment, "//"))
	body = strings.TrimSpace(strings.TrimPrefix(body, "/*"))
	body = strings.TrimSpace(strings.TrimSuffix(body, "*/"))
	idx := strings.Index(body, inlineSuppressionPrefix)
	if idx < 0 {
		return false
	}
	rest := strings.TrimSpace(body[idx+len(inlineSuppressionPrefix):])
	if rest == "" {
		return false
	}
	// First whitespace-delimited token must equal ruleName exactly.
	tok := rest
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		tok = rest[:i]
	}
	return tok == ruleName
}

// isExempt is the single entry point rules call to check both the
// per-package allowlist and inline suppression. Skips findings when
// either path matches.
func isExempt(fset *token.FileSet, file *ast.File, ruleName, filename string, findingLine int) bool {
	if isKitFactoryExempt(ruleName, filename) {
		return true
	}
	return hasInlineSuppression(fset, file, ruleName, findingLine)
}
