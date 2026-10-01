package grpcotlp_test

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// TestGRPCOTLPAttributesAreValidJSON: the hand-rolled encoder emitted raw
// control characters and NaN/Inf literals, producing invalid JSON that the
// read path silently dropped.
func TestGRPCOTLPAttributesAreValidJSON(t *testing.T) {
	t.Parallel()
	fs := &fakeStore{}
	client, stop := newBufconnServer(t, fs)
	defer stop()

	str := func(k, v string) *commonpb.KeyValue {
		return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
	}
	dbl := func(k string, v float64) *commonpb.KeyValue {
		return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: v}}}
	}
	req := &coltracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
			TraceId: make([]byte, 16), SpanId: []byte{1, 2, 3, 4, 5, 6, 7, 8}, Name: "s",
			Attributes: []*commonpb.KeyValue{
				str("ctl\x01key", "bell\x07 and \x1f"),
				dbl("nan", math.NaN()),
				dbl("inf", math.Inf(1)),
				dbl("ok", 1.5),
			},
		}}}},
	}}}
	if _, err := client.Export(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	spans := fs.recorded()
	if len(spans) != 1 {
		t.Fatalf("got %d spans", len(spans))
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(spans[0].AttributesJSON), &attrs); err != nil {
		t.Fatalf("attributes_json is invalid JSON: %v (%q)", err, spans[0].AttributesJSON)
	}
	if attrs["ctl\x01key"] != "bell\x07 and \x1f" {
		t.Errorf("control-char attribute round-trip: %#v", attrs["ctl\x01key"])
	}
	if attrs["nan"] != "NaN" || attrs["inf"] != "+Inf" || attrs["ok"] != 1.5 {
		t.Errorf("numeric attributes: %#v", attrs)
	}
}
