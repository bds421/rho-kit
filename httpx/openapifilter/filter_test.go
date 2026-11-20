package openapifilter

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

const contract = `
openapi: 3.0.3
info: {title: t, version: "1"}
tags:
  - name: open
  - name: closed
paths:
  /open:
    get:
      operationId: getOpen
      tags: [open]
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: "#/components/schemas/Shared"}}}}
    delete:
      operationId: deleteOpen
      x-profile: extra
      tags: [open]
      responses: {"204": {description: gone}}
  /closed:
    post:
      operationId: postClosed
      x-profile: off
      tags: [closed]
      requestBody: {$ref: "#/components/requestBodies/ClosedBody"}
      responses: {"204": {description: ok}}
  /odd:
    get:
      operationId: getOdd
      x-profile: 7
      responses: {"204": {description: ok}}
components:
  requestBodies:
    ClosedBody:
      content: {application/json: {schema: {$ref: "#/components/schemas/Closed"}}}
  schemas:
    Shared: {type: object, properties: {inner: {$ref: "#/components/schemas/Inner"}}}
    Inner: {type: string}
    Closed: {type: object, properties: {deeper: {$ref: "#/components/schemas/OnlyClosed"}, shared: {$ref: "#/components/schemas/Inner"}}}
    OnlyClosed: {type: string}
    Hook: {type: object}
`

func load(t *testing.T) *openapi3.T {
	t.Helper()
	spec, err := openapi3.NewLoader().LoadFromData([]byte(contract))
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

var keepExtra = Filter{Extension: "x-profile", Keep: func(v string) bool { return v == "extra" }}

func operationIDs(spec *openapi3.T) []string {
	var ids []string
	for _, item := range spec.Paths.Map() {
		for _, op := range item.Operations() {
			ids = append(ids, op.OperationID)
		}
	}
	slices.Sort(ids)
	return ids
}

func TestAFilterKeepsUnmarkedAndAcceptedOperations(t *testing.T) {
	spec, err := keepExtra.Spec(func() (*openapi3.T, error) { return load(t), nil })()
	if err != nil {
		t.Fatal(err)
	}
	if got := operationIDs(spec); !slices.Equal(got, []string{"deleteOpen", "getOpen"}) {
		t.Fatalf("kept %v", got)
	}
	if spec.Paths.Value("/closed") != nil || spec.Paths.Value("/odd") != nil {
		t.Error("a path without operations stays")
	}
	if len(spec.Tags) != 1 || spec.Tags[0].Name != "open" {
		t.Errorf("tags %v", spec.Tags)
	}
}

// Components only removed operations reached go, through other components
// too; those a kept operation reaches and those no path reached stay.
func TestAFilterDropsOnlyTheComponentsOfRemovedOperations(t *testing.T) {
	spec := load(t)
	if err := keepExtra.Apply(spec); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range spec.Components.Schemas {
		names = append(names, name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"Hook", "Inner", "Shared"}) {
		t.Errorf("schemas %v", names)
	}
	if len(spec.Components.RequestBodies) != 0 {
		t.Error("the removed operation's request body stays")
	}
	doc, err := json.Marshal(spec)
	if err != nil || strings.Contains(string(doc), "OnlyClosed") {
		t.Errorf("the document still names what was removed: %v", err)
	}
}

func TestAFilterThatKeepsEveryValueKeepsAStringMarkOnly(t *testing.T) {
	all := Filter{Extension: "x-profile", Keep: func(string) bool { return true }}
	spec := load(t)
	if err := all.Apply(spec); err != nil {
		t.Fatal(err)
	}
	if got := operationIDs(spec); !slices.Equal(got, []string{"deleteOpen", "getOpen", "postClosed"}) {
		t.Errorf("kept %v", got)
	}
}

func TestUnknownNamesOperationsWithAnotherValue(t *testing.T) {
	got := Unknown(load(t), "x-profile", "extra")
	slices.Sort(got)
	if !slices.Equal(got, []string{"getOdd", "postClosed"}) {
		t.Errorf("unknown %v", got)
	}
	if got := Unknown(load(t), "x-profile", "extra", "off"); !slices.Equal(got, []string{"getOdd"}) {
		t.Errorf("unknown %v", got)
	}
}

func TestAReadErrorPassesThrough(t *testing.T) {
	want := openapi3.ErrURINotSupported
	if _, err := keepExtra.Spec(func() (*openapi3.T, error) { return nil, want })(); !errors.Is(err, want) {
		t.Errorf("error %v", err)
	}
}
