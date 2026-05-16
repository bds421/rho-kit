// Command check-fmt-errorf-wrap flags `fmt.Errorf("...: %w", err)`
// call sites in data/ and infra/ packages where the wrapped value is
// a local error variable (typically a backend SDK error). Wave 136
// introduced `redact.WrapError(prefix, err)` to make Error() safe to
// render across trust boundaries; this gate prevents the old pattern
// from quietly re-entering the data/infra layer.
//
// # Scope
//
// AST scan over every *.go file in data/ and infra/ (the boundary
// where backend errors flow). Skips _test.go files, integrationtest
// modules, and files inside vendor/ or .claude/.
//
// # Detection
//
// Reports any call to fmt.Errorf whose final argument is an
// identifier (typically named "err", "perr", or similar) AND whose
// format string contains a `: %w` segment. The identifier check is
// the simplest reliable signal — passing `err` to fmt.Errorf with
// `%w` is exactly the pattern wave 136 swept.
//
// # Allowlist
//
// A line-level opt-out: append `// kit:ok-fmt-errorf-wrap` to a
// specific fmt.Errorf line when the wrapped value is a known kit
// sentinel that is safe to render verbatim. Example:
//
//	return fmt.Errorf("redis cache get: %w", sharedcache.ErrValueTooLarge) // kit:ok-fmt-errorf-wrap
//
// Package-level sentinels are NOT auto-detected — the heuristic for
// "is this a kit sentinel?" is too brittle. The opt-out keeps each
// kept-as-is wrap visible at code-review time.
//
// Exit codes:
//
//	0  no violations
//	1  violations detected
//	2  CLI / discovery failure
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const optOutMarker = "kit:ok-fmt-errorf-wrap"

type violation struct {
	file string
	line int
	col  int
	expr string
}

func main() {
	root := flag.String("root", ".", "repository root to scan")
	flag.Parse()

	if err := os.Chdir(*root); err != nil {
		fmt.Fprintf(os.Stderr, "check-fmt-errorf-wrap: chdir: %v\n", err)
		os.Exit(2)
	}

	scanRoots := []string{"data", "infra"}
	var violations []violation

	for _, dir := range scanRoots {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				switch d.Name() {
				case "vendor", ".claude", "integrationtest":
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			found, err := scanFile(path)
			if err != nil {
				return err
			}
			violations = append(violations, found...)
			return nil
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "check-fmt-errorf-wrap: walk %s: %v\n", dir, err)
			os.Exit(2)
		}
	}

	if len(violations) == 0 {
		fmt.Println("check-fmt-errorf-wrap OK (no fmt.Errorf %w wraps over locals in data/ or infra/)")
		return
	}

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].file != violations[j].file {
			return violations[i].file < violations[j].file
		}
		return violations[i].line < violations[j].line
	})

	fmt.Fprintln(os.Stderr, "check-fmt-errorf-wrap: violations (use redact.WrapError instead, or annotate with // kit:ok-fmt-errorf-wrap):")
	for _, v := range violations {
		fmt.Fprintf(os.Stderr, "  %s:%d:%d: %s\n", v.file, v.line, v.col, v.expr)
	}
	os.Exit(1)
}

func scanFile(path string) ([]violation, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	optOutLines := map[int]bool{}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, optOutMarker) {
				optOutLines[fset.Position(c.Slash).Line] = true
			}
		}
	}

	var out []violation
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isFmtErrorf(call.Fun) {
			return true
		}
		if len(call.Args) < 2 {
			return true
		}
		fmtArg, ok := call.Args[0].(*ast.BasicLit)
		if !ok || fmtArg.Kind != token.STRING {
			return true
		}
		if !strings.Contains(fmtArg.Value, ": %w") {
			return true
		}
		last := call.Args[len(call.Args)-1]
		ident, ok := last.(*ast.Ident)
		if !ok {
			return true
		}
		if !isErrorIdent(ident.Name) {
			return true
		}
		pos := fset.Position(call.Lparen)
		if optOutLines[pos.Line] {
			return true
		}
		out = append(out, violation{
			file: path,
			line: pos.Line,
			col:  pos.Column,
			expr: fmt.Sprintf("fmt.Errorf(%s, ..., %s)", fmtArg.Value, ident.Name),
		})
		return true
	})
	return out, nil
}

func isFmtErrorf(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "fmt" && sel.Sel.Name == "Errorf"
}

func isErrorIdent(name string) bool {
	switch name {
	case "err", "perr", "rerr", "gErr", "slErr", "saveErr", "closeErr", "relErr", "ctxErr":
		return true
	}
	return false
}
