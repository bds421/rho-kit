// Command kit-new generates a service skeleton wired to the rho-kit
// golden path. The scaffold compiles immediately and ships with a
// CI workflow that runs `kit-doctor` so the service stays aligned
// with the kit's secure defaults as it grows.
//
// Usage:
//
//	kit-new SERVICE_NAME -module-path github.com/org/SERVICE_NAME [-dir ./SERVICE_NAME] [-mcp]
//
// Flags:
//
//	-module-path  Go module path (required).
//	-dir          Output directory (default: ./<service-name>).
//	-mcp          Scaffold a sample MCP tool registration in
//	              internal/app/wire.go and add a smoke-test target
//	              to the generated Makefile.
//
// To regenerate a service into a fresh directory after upgrading
// rho-kit, run kit-new again with the same flags into a sibling
// directory and diff the output.
//
// Adding a template: drop a `*.tmpl` file into ./templates/ and add
// a row to `templateFile` in scaffold.go pointing at its destination
// path. Both the file body and the destination path are rendered
// through `text/template` so placeholders like `{{.ServiceName}}`
// work in either place.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	modulePath := flag.String("module-path", "", "Go module path (e.g. github.com/org/my-service)")
	dir := flag.String("dir", "", "output directory (default: ./<service-name>)")
	withMCP := flag.Bool("mcp", false, "scaffold a sample MCP tool registration in internal/app/wire.go")
	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: kit-new SERVICE_NAME -module-path github.com/org/SERVICE_NAME [-dir ./SERVICE_NAME] [-mcp]")
		os.Exit(2)
	}
	name := flag.Arg(0)
	if err := ValidateServiceName(name); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *modulePath == "" {
		fmt.Fprintln(os.Stderr, "kit-new: -module-path is required")
		os.Exit(2)
	}
	out := *dir
	if out == "" {
		out = "./" + name
	}

	p := Params{ServiceName: name, ModulePath: *modulePath, MCP: *withMCP}
	if err := scaffold(out, p); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if _, err := fmt.Fprintf(os.Stdout, "kit-new: generated %s into %s\n", name, out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
