package openapifilter

import (
	"net/http"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Routes is a mux that registers only the operations of a contract, by the
// patterns oapi-codegen's standard-library servers register them under
// ("METHOD base+path"). Pass it as the generated server's BaseRouter.
type Routes struct {
	*http.ServeMux
	kept, handled map[string]bool
}

// NewRoutes keeps the operations of spec, served under base.
func NewRoutes(spec *openapi3.T, base string) Routes {
	kept := map[string]bool{}
	for path, item := range spec.Paths.Map() {
		for method := range item.Operations() {
			kept[strings.ToUpper(method)+" "+base+path] = true
		}
	}
	return Routes{ServeMux: http.NewServeMux(), kept: kept, handled: map[string]bool{}}
}

// HandleFunc registers a pattern the contract keeps and drops any other,
// which then answers as an unknown path.
func (r Routes) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	if r.kept[pattern] {
		r.handled[pattern] = true
		r.ServeMux.HandleFunc(pattern, h)
	}
}

// Missing are the contract's operations no handler registered, sorted:
// the contract and its generated server out of step.
func (r Routes) Missing() []string {
	var out []string
	for p := range r.kept {
		if !r.handled[p] {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}
