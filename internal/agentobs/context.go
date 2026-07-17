package agentobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const spanContextVersion = 1

type SpanContext struct {
	TraceID TraceID
	SpanID  SpanID
}

func (c SpanContext) Validate() error {
	if strings.TrimSpace(string(c.TraceID)) == "" || strings.TrimSpace(string(c.SpanID)) == "" {
		return errors.New("Span Context identity is incomplete")
	}
	return nil
}

func (c SpanContext) MarshalText() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Version int     `json:"version"`
		TraceID TraceID `json:"trace_id"`
		SpanID  SpanID  `json:"span_id"`
	}{Version: spanContextVersion, TraceID: c.TraceID, SpanID: c.SpanID})
}

func (c *SpanContext) UnmarshalText(encoded []byte) error {
	if c == nil {
		return errors.New("nil Span Context receiver")
	}
	var envelope struct {
		Version int     `json:"version"`
		TraceID TraceID `json:"trace_id"`
		SpanID  SpanID  `json:"span_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("Span Context has trailing data")
	}
	if envelope.Version != spanContextVersion {
		return errors.New("Span Context version is unsupported")
	}
	decoded := SpanContext{TraceID: envelope.TraceID, SpanID: envelope.SpanID}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*c = decoded
	return nil
}

type spanContextKey struct{}

func ContextWithSpanContext(ctx context.Context, span SpanContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if span.Validate() != nil {
		return ctx
	}
	return context.WithValue(ctx, spanContextKey{}, span)
}

func SpanContextFromContext(ctx context.Context) (SpanContext, bool) {
	if ctx == nil {
		return SpanContext{}, false
	}
	span, ok := ctx.Value(spanContextKey{}).(SpanContext)
	if !ok || span.Validate() != nil {
		return SpanContext{}, false
	}
	return span, true
}
