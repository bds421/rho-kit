package contract

import (
	"net/http"
	"strings"
)

func compareOpenAPI(report *Report, artifact Artifact, oldDoc, newDoc map[string]any) {
	oldPaths := object(oldDoc["paths"])
	newPaths := object(newDoc["paths"])
	for path, rawOldItem := range oldPaths {
		oldItem := object(rawOldItem)
		newItem := object(newPaths[path])
		if newItem == nil {
			report.add(artifact, "http_path_removed", "HTTP path "+path+" was removed")
			continue
		}
		for method, rawOldOperation := range oldItem {
			method = strings.ToLower(method)
			if !isHTTPMethod(method) {
				continue
			}
			oldOperation := object(rawOldOperation)
			newOperation := object(newItem[method])
			operationName := strings.ToUpper(method) + " " + path
			if newOperation == nil {
				report.add(artifact, "http_operation_removed", "HTTP operation "+operationName+" was removed")
				continue
			}
			compareRequestSchema(report, artifact, operationName, oldOperation, newOperation, oldDoc, newDoc)
			compareResponseSchemas(report, artifact, operationName, oldOperation, newOperation, oldDoc, newDoc)
		}
	}
}

func compareRequestSchema(report *Report, artifact Artifact, operation string, oldOperation, newOperation, oldDoc, newDoc map[string]any) {
	oldBody := object(oldOperation["requestBody"])
	newBody := object(newOperation["requestBody"])
	if oldBody == nil || newBody == nil {
		if oldBody != nil && newBody == nil {
			report.add(artifact, "http_request_body_removed", operation+" no longer accepts its documented request body")
		}
		return
	}
	oldSchema := mediaSchema(oldBody, oldDoc)
	newSchema := mediaSchema(newBody, newDoc)
	if oldSchema == nil || newSchema == nil {
		report.add(artifact, "http_schema_unsupported", operation+" has a request body the compatibility checker cannot resolve")
		return
	}
	compareSchemaInput(report, artifact, oldSchema, newSchema, "HTTP request "+operation)
}

func compareResponseSchemas(report *Report, artifact Artifact, operation string, oldOperation, newOperation, oldDoc, newDoc map[string]any) {
	oldResponses := object(oldOperation["responses"])
	newResponses := object(newOperation["responses"])
	for status, rawOldResponse := range oldResponses {
		oldResponse := object(rawOldResponse)
		newResponse := object(newResponses[status])
		if newResponse == nil {
			report.add(artifact, "http_response_removed", operation+" no longer documents response status "+status)
			continue
		}
		oldSchema := mediaSchema(oldResponse, oldDoc)
		newSchema := mediaSchema(newResponse, newDoc)
		if oldSchema == nil && newSchema == nil {
			continue // bodyless response remains bodyless
		}
		if oldSchema == nil || newSchema == nil {
			report.add(artifact, "http_response_schema_changed", operation+" changed body shape for response status "+status)
			continue
		}
		compareSchemaOutput(report, artifact, oldSchema, newSchema, "HTTP response "+operation+" "+status)
	}
}

func compareSchema(report *Report, artifact Artifact, oldSchema, newSchema map[string]any, label string) {
	compareSchemaOutput(report, artifact, oldSchema, newSchema, label)
	compareSchemaInput(report, artifact, oldSchema, newSchema, label)
}

// compareSchemaOutput checks whether a newer producer can still provide the
// fields an older consumer/client expects.
func compareSchemaOutput(report *Report, artifact Artifact, oldSchema, newSchema map[string]any, label string) {
	oldProperties, newProperties := object(oldSchema["properties"]), object(newSchema["properties"])
	for name, rawOld := range oldProperties {
		rawNew, exists := newProperties[name]
		if !exists {
			report.add(artifact, "required_output_property_removed", label+" removed property "+name)
			continue
		}
		oldChild, newChild := object(rawOld), object(rawNew)
		if oldChild == nil || newChild == nil {
			report.add(artifact, "schema_unsupported", label+" has a non-object property schema for "+name)
			continue
		}
		if schemaType(oldChild) != schemaType(newChild) {
			report.add(artifact, "output_property_type_changed", label+" changed property type for "+name)
		}
		compareSchemaOutput(report, artifact, oldChild, newChild, label+"."+name)
	}
	for required := range stringSet(oldSchema["required"]) {
		if _, exists := newProperties[required]; !exists {
			report.add(artifact, "required_output_property_removed", label+" removed required property "+required)
		}
	}
	oldRequired, newRequired := stringSet(oldSchema["required"]), stringSet(newSchema["required"])
	for required := range oldRequired {
		if _, stillRequired := newRequired[required]; !stillRequired {
			report.add(artifact, "required_output_property_no_longer_required", label+" no longer guarantees property "+required)
		}
	}
}

// compareSchemaInput checks whether requests/events previously accepted by an
// old contract are not newly rejected by required fields or narrowed types.
func compareSchemaInput(report *Report, artifact Artifact, oldSchema, newSchema map[string]any, label string) {
	oldRequired, newRequired := stringSet(oldSchema["required"]), stringSet(newSchema["required"])
	for required := range newRequired {
		if _, existed := oldRequired[required]; !existed {
			report.add(artifact, "new_required_input_property", label+" added required property "+required)
		}
	}
	oldProperties, newProperties := object(oldSchema["properties"]), object(newSchema["properties"])
	for name, rawOld := range oldProperties {
		rawNew, exists := newProperties[name]
		if !exists {
			continue // an optional old input may remain ignored by the new service
		}
		oldChild, newChild := object(rawOld), object(rawNew)
		if oldChild == nil || newChild == nil {
			report.add(artifact, "schema_unsupported", label+" has a non-object property schema for "+name)
			continue
		}
		if schemaType(oldChild) != schemaType(newChild) {
			report.add(artifact, "input_property_type_changed", label+" changed accepted type for "+name)
		}
		compareSchemaInput(report, artifact, oldChild, newChild, label+"."+name)
	}
}

func mediaSchema(body, doc map[string]any) map[string]any {
	content := object(body["content"])
	jsonMedia := object(content["application/json"])
	schema := object(jsonMedia["schema"])
	return resolveRef(schema, doc)
}

func resolveRef(schema, doc map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	ref, _ := schema["$ref"].(string)
	if ref == "" {
		return schema
	}
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(ref, prefix) {
		return nil
	}
	name := strings.ReplaceAll(strings.TrimPrefix(ref, prefix), "~1", "/")
	name = strings.ReplaceAll(name, "~0", "~")
	return object(object(object(doc["components"])["schemas"])[name])
}

func object(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func stringSet(v any) map[string]struct{} {
	out := map[string]struct{}{}
	items, _ := v.([]any)
	for _, item := range items {
		if s, ok := item.(string); ok {
			out[s] = struct{}{}
		}
	}
	return out
}

func schemaType(schema map[string]any) string {
	value, _ := schema["type"].(string)
	return value
}

func isHTTPMethod(method string) bool {
	switch method {
	case strings.ToLower(http.MethodGet), strings.ToLower(http.MethodPut), strings.ToLower(http.MethodPost), strings.ToLower(http.MethodDelete), strings.ToLower(http.MethodPatch), strings.ToLower(http.MethodHead), strings.ToLower(http.MethodOptions), strings.ToLower(http.MethodTrace):
		return true
	default:
		return false
	}
}
