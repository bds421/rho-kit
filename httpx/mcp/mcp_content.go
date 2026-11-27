package mcp

import (
	"context"
	"errors"
	"reflect"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// ToolError is a handler error whose Text the caller reads: the result has
// `isError: true` and Text as its content, so an agent sees what failed
// and can correct its next call. The handler chooses Text and must not put
// secrets or infrastructure detail in it; Err, the cause, is only logged
// and audited. Any other error reaches the caller as one of the kit's
// fixed texts ("forbidden", "internal error", ...).
type ToolError struct {
	Text string
	Err  error
}

// NewToolError returns a [ToolError] with text for the caller and cause
// (may be nil) for the server side.
func NewToolError(text string, cause error) *ToolError {
	return &ToolError{Text: text, Err: cause}
}

func (e *ToolError) Error() string {
	if e.Err == nil {
		return e.Text
	}
	return e.Text + ": " + e.Err.Error()
}

func (e *ToolError) Unwrap() error { return e.Err }

// callerText returns the text a ToolError in err's chain chose.
func callerText(err error) (string, bool) {
	var te *ToolError
	if errors.As(err, &te) && te.Text != "" {
		return te.Text, true
	}
	return "", false
}

// Content is one item of a tool result that is not the handler's JSON:
// [Text], [Image], [Resource] or [ResourceLink].
type Content interface {
	sdk() sdkmcp.Content
}

// Contents is a tool result made of content items instead of JSON. A
// handler whose Out is Contents (or implements [ContentResult]) answers
// with exactly these items and no structured content, and its tool
// advertises no output schema: a client shows an image, saves an
// embedded file or follows a link.
type Contents []Content

// ToolContent implements [ContentResult].
func (c Contents) ToolContent() []Content { return c }

// ContentResult is an Out type that answers with content items; see
// [Contents].
type ContentResult interface {
	ToolContent() []Content
}

// Text is a text item.
type Text string

func (t Text) sdk() sdkmcp.Content { return &sdkmcp.TextContent{Text: string(t)} }

// Image is an image item: its bytes (sent in base64) and media type.
type Image struct {
	Data     []byte
	MIMEType string
}

func (i Image) sdk() sdkmcp.Content {
	return &sdkmcp.ImageContent{Data: i.Data, MIMEType: i.MIMEType}
}

// Resource is an embedded resource: a file the client can save. Blob
// holds binary bytes (sent in base64), Text a text file; set one.
type Resource struct {
	URI      string
	MIMEType string
	Text     string
	Blob     []byte
}

func (r Resource) sdk() sdkmcp.Content {
	return &sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{
		URI: r.URI, MIMEType: r.MIMEType, Text: r.Text, Blob: r.Blob,
	}}
}

// ResourceLink points to a resource the client fetches itself, for a file
// too large to embed. Size is in bytes; zero leaves it out.
type ResourceLink struct {
	URI         string
	Name        string
	Title       string
	Description string
	MIMEType    string
	Size        int64
}

func (l ResourceLink) sdk() sdkmcp.Content {
	link := &sdkmcp.ResourceLink{URI: l.URI, Name: l.Name, Title: l.Title, Description: l.Description, MIMEType: l.MIMEType}
	if l.Size > 0 {
		size := l.Size
		link.Size = &size
	}
	return link
}

// buildContent builds the result of a handler that answers with content
// items. ToolContent is the handler's code: a panic in it, or in an item,
// fails the call like a panicking handler. Nil items, typed nil pointers
// too, are skipped.
func (s *Server) buildContent(ctx context.Context, name string, items ContentResult) (result *sdkmcp.CallToolResult, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			result, err = nil, errHandlerPanicked
			s.cfg.logger.ErrorContext(ctx, "mcp: tool content panicked", redact.String("tool", name), redact.Panic(rec))
		}
	}()
	return contentResult(items.ToolContent()), nil
}

func contentResult(items []Content) *sdkmcp.CallToolResult {
	content := make([]sdkmcp.Content, 0, len(items))
	for _, item := range items {
		if v := reflect.ValueOf(item); item != nil && (v.Kind() != reflect.Pointer || !v.IsNil()) {
			content = append(content, item.sdk())
		}
	}
	return &sdkmcp.CallToolResult{Content: content}
}
