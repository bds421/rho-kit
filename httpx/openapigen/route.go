package openapigen

import "maps"

// newRouteState builds a registered operation from its route options.
func newRouteState(method, path string, cfg *routeConfig) *routeState {
	rs := &routeState{
		method:    method,
		path:      path,
		responses: map[int]Response{},
	}

	rs.op.Tags = append([]string(nil), cfg.tags...)
	rs.op.Summary = cfg.summary
	rs.op.Description = cfg.description
	rs.op.OperationID = cfg.operationID
	rs.op.Parameters = applyParameterExamples(
		mergePathParameters(path, cfg.parameters, cfg.skipParamDiscovery),
		cfg.parameterExamples,
	)
	rs.op.Deprecated = cfg.deprecated
	rs.op.Security = cfg.security
	rs.op.ExternalDocs = cfg.externalDocs

	if cfg.requestSchema != nil {
		rs.requestSchema = cfg.requestSchema
		rs.requestType = cfg.requestMediaType
		rs.requestDesc = cfg.requestDescription
		rs.requestReq = cfg.requestRequired
		if rs.requestType == "" {
			rs.requestType = DefaultJSONMediaType
		}
	}
	if len(cfg.requestExamples) > 0 {
		rs.requestExamples = make(map[string]any, len(cfg.requestExamples))
		maps.Copy(rs.requestExamples, cfg.requestExamples)
	}

	for status := range cfg.responseStatuses() {
		rs.responses[status] = cfg.response(status)
	}
	return rs
}

// responseStatuses collects every status that has any registration
// contributing to it — singular schema, extra content entries, header
// declarations, or just a description. Every source is walked so the
// merge sees the union, regardless of registration order.
func (cfg *routeConfig) responseStatuses() map[int]struct{} {
	statusSet := map[int]struct{}{}
	for status := range cfg.responseSchemas {
		statusSet[status] = struct{}{}
	}
	for status := range cfg.responseExtraContent {
		statusSet[status] = struct{}{}
	}
	for status := range cfg.responseHeaders {
		statusSet[status] = struct{}{}
	}
	for status := range cfg.responseDescriptions {
		statusSet[status] = struct{}{}
	}
	for status := range cfg.responseExamples {
		statusSet[status] = struct{}{}
	}
	return statusSet
}

// response merges every registration of one status.
func (cfg *routeConfig) response(status int) Response {
	desc := cfg.responseDescriptions[status]
	if desc == "" {
		desc = defaultResponseDescription(status)
	}
	var headers map[string]Header
	if hs, ok := cfg.responseHeaders[status]; ok && len(hs) > 0 {
		headers = make(map[string]Header, len(hs))
		maps.Copy(headers, hs)
	}
	return Response{
		Description: desc,
		Headers:     headers,
		Content:     cfg.responseContent(status),
	}
}

// responseContent merges the media types of one status: the singular
// schema, the extra content entries, and examples without a schema.
func (cfg *routeConfig) responseContent(status int) map[string]MediaType {
	var content map[string]MediaType
	examplesForStatus := cfg.responseExamples[status]
	// Singular schema contributes one entry under the configured
	// media type (default application/json).
	if schema, ok := cfg.responseSchemas[status]; ok {
		mediaType := cfg.responseTypes[status]
		if mediaType == "" {
			mediaType = DefaultJSONMediaType
		}
		entry := MediaType{Schema: schema}
		if ex, ok := examplesForStatus[mediaType]; ok {
			entry.Example = ex
		}
		content = map[string]MediaType{
			mediaType: entry,
		}
	}
	// Extra-content entries append (and replace any duplicate
	// media-type entry from the singular shape — last write
	// wins, mirroring how options compose elsewhere).
	if extras, ok := cfg.responseExtraContent[status]; ok && len(extras) > 0 {
		if content == nil {
			content = make(map[string]MediaType, len(extras))
		}
		for mt, schema := range extras {
			entry := MediaType{Schema: schema}
			if ex, ok := examplesForStatus[mt]; ok {
				entry.Example = ex
			}
			content[mt] = entry
		}
	}
	// Examples for media types that have no schema entry still
	// attach to a MediaType node so generated portals see them.
	// This is the "schema-less example" case for callers who
	// document the shape via description alone.
	for mt, ex := range examplesForStatus {
		if _, has := content[mt]; has {
			continue
		}
		if content == nil {
			content = map[string]MediaType{}
		}
		content[mt] = MediaType{Example: ex}
	}
	return content
}
