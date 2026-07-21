package openapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// RouteMeta describes one HTTP route for OpenAPI path emission.
type RouteMeta struct {
	Method  string
	Path    string
	Summary string
	Tags    []string
	Public  bool
}

// Document is a minimal OpenAPI 3.1 document with paths only.
type Document struct {
	OpenAPI string              `json:"openapi"`
	Paths   map[string]pathItem `json:"paths"`
	Info    map[string]string   `json:"info"`
}

type pathItem map[string]operation

type operation struct {
	Summary     string   `json:"summary,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	OperationID string   `json:"operationId,omitempty"`
}

// EmitPathsJSON builds an OpenAPI 3.1 document from route metadata and
// returns indented JSON suitable for serving as openapi.json.
func EmitPathsJSON(title string, routes []RouteMeta) ([]byte, error) {
	doc := Document{
		OpenAPI: "3.1.0",
		Info: map[string]string{
			"title":   title,
			"version": "0.0.0",
		},
		Paths: make(map[string]pathItem, len(routes)),
	}
	for _, r := range routes {
		method := strings.ToLower(strings.TrimSpace(r.Method))
		if method == "" {
			return nil, fmt.Errorf("openapi: route requires method")
		}
		path := normalizePath(r.Path)
		if path == "" {
			return nil, fmt.Errorf("openapi: route requires path")
		}
		item, ok := doc.Paths[path]
		if !ok {
			item = make(pathItem)
			doc.Paths[path] = item
		}
		if _, exists := item[method]; exists {
			return nil, fmt.Errorf("openapi: duplicate route %s %s", strings.ToUpper(method), path)
		}
		op := operation{
			Summary: r.Summary,
			Tags:    append([]string(nil), r.Tags...),
		}
		if r.Public {
			op.Tags = append(op.Tags, "public")
		}
		if op.Summary == "" {
			op.Summary = strings.ToUpper(method) + " " + path
		}
		op.OperationID = sanitizeOperationID(method, path)
		item[method] = op
	}
	// Ensure operationId uniqueness after sanitisation (e.g. /a/b vs /a_b).
	seen := make(map[string]string) // opID -> method+path
	for path, item := range doc.Paths {
		for method, op := range item {
			base := op.OperationID
			id := base
			for n := 2; ; n++ {
				if prev, ok := seen[id]; !ok {
					seen[id] = method + " " + path
					op.OperationID = id
					item[method] = op
					break
				} else if prev == method+" "+path {
					break
				}
				id = fmt.Sprintf("%s_%d", base, n)
			}
		}
	}
	return json.MarshalIndent(doc, "", "  ")
}

// sanitizeOperationID builds a code-generator-safe operationId from method+path.
// Braces and other non-identifier runes become underscores; slashes become underscores.
func sanitizeOperationID(method, path string) string {
	p := strings.Trim(path, "/")
	var b strings.Builder
	b.WriteString(method)
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune('_')
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// RoutesFromHandler is reserved for future ServeMux introspection.
// Today it always returns nil; callers must build []RouteMeta explicitly
// (e.g. from their route table) until a stdlib-safe introspection path
// exists. Kept exported so generated stubs and early adopters compile
// against a stable symbol.
func RoutesFromHandler(_ http.Handler) []RouteMeta {
	return nil
}

func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}
