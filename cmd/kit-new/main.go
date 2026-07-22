// Command kit-new generates a service skeleton wired to the rho-kit
// golden path. The scaffold compiles immediately and ships with a
// CI workflow that runs `kit-doctor` so the service stays aligned
// with the kit's secure defaults as it grows.
//
// Usage:
//
//	kit-new SERVICE_NAME -module-path github.com/org/SERVICE_NAME [-dir ./SERVICE_NAME] [-mcp] [-postgres] [-tenant] [-production] [-rho-version vX.Y.Z]
//
// Flags:
//
//	-module-path  Go module path (required).
//	-dir          Output directory (default: ./<service-name>).
//	-mcp          Scaffold a sample MCP tool registration in
//	              internal/app/wire.go and add a smoke-test target
//	              to the generated Makefile.
//	-postgres     Scaffold the sqlc + pgx + goose golden path:
//	              sqlc.yaml, db/queries/users.sql, a starter migration
//	              under db/migrations, and a small migrations package
//	              that exposes the embed.FS to internal/app/wire.go.
//	-tenant       Scaffold strict X-Tenant-Id extraction plus tenant-wrapped
//	              Redis cache and idempotency stores.
//	-rho-version  Pin the rho-kit module version in the generated
//	              go.mod (e.g. v2.0.0). Empty (default) writes no
//	              explicit require block; the user runs `go mod tidy`
//	              to populate it from the imports against their
//	              configured proxy.
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
	"strings"
)

func main() {
	modulePath := flag.String("module-path", "", "Go module path (e.g. github.com/org/my-service)")
	dir := flag.String("dir", "", "output directory (default: ./<service-name>)")
	withMCP := flag.Bool("mcp", false, "scaffold a sample MCP tool registration in internal/app/wire.go")
	withPostgres := flag.Bool("postgres", false, "scaffold the sqlc + pgx + goose data path (db/queries, db/migrations, sqlc.yaml, migrations package)")
	withTenant := flag.Bool("tenant", false, "scaffold strict X-Tenant-Id extraction plus tenant-wrapped Redis cache and idempotency stores")
	production := flag.Bool("production", false, "scaffold the resource-API production profile (JWT, OpenFGA, Postgres, RabbitMQ inbox/outbox, tracing, contracts)")
	rhoVersion := flag.String("rho-version", "", "pin rho-kit module version in go.mod (e.g. v2.0.0); empty leaves go.mod without explicit require so go mod tidy resolves from imports")
	leadingName, args := splitLeadingServiceName(os.Args[1:])
	if err := flag.CommandLine.Parse(args); err != nil {
		os.Exit(2)
	}

	name := leadingName
	if name == "" {
		if flag.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: kit-new SERVICE_NAME -module-path github.com/org/SERVICE_NAME [-dir ./SERVICE_NAME] [-mcp] [-postgres] [-tenant] [-production] [-rho-version vX.Y.Z]")
			os.Exit(2)
		}
		name = flag.Arg(0)
	} else if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: kit-new SERVICE_NAME -module-path github.com/org/SERVICE_NAME [-dir ./SERVICE_NAME] [-mcp] [-postgres] [-tenant] [-production] [-rho-version vX.Y.Z]")
		os.Exit(2)
	}
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

	p := Params{ServiceName: name, ModulePath: *modulePath, MCP: *withMCP, Postgres: *withPostgres, Tenant: *withTenant, Production: *production, RhoVersion: *rhoVersion}
	if err := scaffold(out, p); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if _, err := fmt.Fprintf(os.Stdout, "kit-new: generated %s into %s\n", name, out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func splitLeadingServiceName(args []string) (string, []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", args
	}
	rest := make([]string, len(args)-1)
	copy(rest, args[1:])
	return args[0], rest
}
