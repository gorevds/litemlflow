// Package grpcotlp implements a gRPC OTLP/TraceService receiver.
//
// It translates incoming ExportTraceServiceRequest protobuf messages to the
// internal model.Span type using the same mapping logic as the HTTP/JSON OTLP
// handler in internal/api/native/handlers.go::IngestOTLP, keeping both
// transports semantically equivalent.
//
// Wire protocol: opentelemetry.proto.collector.trace.v1.TraceService.Export
// Dependency rationale: docs/adr/0002-grpc-otlp-deps.md
package grpcotlp

import (
	"context"
	"encoding/hex"
	"strconv"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/litemlflow/litemlflow/internal/model"
	"github.com/litemlflow/litemlflow/internal/store"
)

// traceServiceServer implements TraceServiceServer.
type traceServiceServer struct {
	coltracepb.UnimplementedTraceServiceServer
	store store.Store
}

// Export implements TraceServiceServer.Export.
//
// Spans are mapped to model.Span using the same field mapping as the
// HTTP/JSON IngestOTLP handler:
//   - trace_id, span_id   → hex-encoded IDs (0-padded 32/16 chars)
//   - parent_span_id       → parent_id (empty if nil / zero bytes)
//   - litemlflow.run_id   resource attribute → run_id
//   - attributes           → attributes_json
//   - start_time_unix_nano / end_time_unix_nano → start_time_ns / end_time_ns
//   - status.code          → status_code string
//   - kind                 → kind string
//
// We follow the HTTP handler's "best-effort" policy: malformed IDs (wrong byte
// length) are hex-encoded verbatim rather than rejected, so partial_success
// always reports 0 rejected spans.
func (t *traceServiceServer) Export(
	ctx context.Context,
	req *coltracepb.ExportTraceServiceRequest,
) (*coltracepb.ExportTraceServiceResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}

	var spans []model.Span
	for _, rs := range req.ResourceSpans {
		runID := ""
		if rs.Resource != nil {
			runID = protoAttrString(rs.Resource.Attributes, "litemlflow.run_id")
		}
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				if sp == nil {
					continue
				}
				traceID := bytesToHex(sp.TraceId)
				spanID := bytesToHex(sp.SpanId)
				parentID := ""
				if len(sp.ParentSpanId) > 0 {
					parentID = bytesToHex(sp.ParentSpanId)
				}

				attrMap := protoAttrMap(sp.Attributes)
				attrJSON := marshalAttrs(attrMap)

				endNS := (*int64)(nil)
				if sp.EndTimeUnixNano > 0 {
					v := int64(sp.EndTimeUnixNano)
					endNS = &v
				}

				spans = append(spans, model.Span{
					ID:             spanID,
					TraceID:        traceID,
					ParentID:       parentID,
					RunID:          runID,
					Name:           sp.Name,
					Kind:           protoKindToString(sp.Kind),
					StartTimeNS:    int64(sp.StartTimeUnixNano),
					EndTimeNS:      endNS,
					AttributesJSON: attrJSON,
					StatusCode:     protoStatusToString(sp.Status),
					StatusMessage:  protoStatusMessage(sp.Status),
				})
			}
		}
	}

	if len(spans) > 0 {
		if err := t.store.InsertSpans(ctx, spans); err != nil {
			return nil, status.Errorf(codes.Internal, "insert spans: %v", err)
		}
	}

	return &coltracepb.ExportTraceServiceResponse{
		PartialSuccess: &coltracepb.ExportTracePartialSuccess{
			RejectedSpans: 0,
		},
	}, nil
}

// ---- helpers ----------------------------------------------------------------

// bytesToHex converts a byte slice to a lower-case hex string. Bytes that are
// the wrong length for a standard trace/span ID are still encoded verbatim to
// match the HTTP handler's best-effort policy.
func bytesToHex(b []byte) string {
	return hex.EncodeToString(b)
}

// protoAttrString returns the string value of the first attribute in attrs
// whose key matches key, or "" if not found.
func protoAttrString(attrs []*commonpb.KeyValue, key string) string {
	for _, kv := range attrs {
		if kv.GetKey() == key {
			if sv, ok := kv.GetValue().GetValue().(*commonpb.AnyValue_StringValue); ok {
				return sv.StringValue
			}
		}
	}
	return ""
}

// protoAttrMap converts a slice of KeyValue proto messages to a plain
// map[string]any using the same type mapping as the HTTP handler's
// otlpAttrMap function.
func protoAttrMap(attrs []*commonpb.KeyValue) map[string]any {
	out := make(map[string]any, len(attrs))
	for _, kv := range attrs {
		if kv == nil {
			continue
		}
		switch v := kv.GetValue().GetValue().(type) {
		case *commonpb.AnyValue_StringValue:
			out[kv.Key] = v.StringValue
		case *commonpb.AnyValue_IntValue:
			out[kv.Key] = v.IntValue
		case *commonpb.AnyValue_DoubleValue:
			out[kv.Key] = v.DoubleValue
		case *commonpb.AnyValue_BoolValue:
			out[kv.Key] = v.BoolValue
		default:
			// Arrays, kvlists, bytes — store as string representation.
			out[kv.Key] = kv.GetValue().String()
		}
	}
	return out
}

// marshalAttrs serialises attrs to a JSON string. Returns "" on empty input
// or marshal error (matching jsonOrEmpty in the HTTP handler).
func marshalAttrs(attrs map[string]any) string {
	if len(attrs) == 0 {
		return ""
	}
	// Inline simple JSON marshalling to avoid importing encoding/json here;
	// use the same approach as the HTTP handler.
	var sb []byte
	sb = append(sb, '{')
	first := true
	for k, v := range attrs {
		if !first {
			sb = append(sb, ',')
		}
		first = false
		sb = appendJSONString(sb, k)
		sb = append(sb, ':')
		sb = appendJSONValue(sb, v)
	}
	sb = append(sb, '}')
	if string(sb) == "{}" {
		return ""
	}
	return string(sb)
}

func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			dst = append(dst, '\\', '"')
		case '\\':
			dst = append(dst, '\\', '\\')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		default:
			dst = append(dst, c)
		}
	}
	return append(dst, '"')
}

func appendJSONValue(dst []byte, v any) []byte {
	switch val := v.(type) {
	case string:
		return appendJSONString(dst, val)
	case int64:
		return strconv.AppendInt(dst, val, 10)
	case float64:
		return strconv.AppendFloat(dst, val, 'f', -1, 64)
	case bool:
		if val {
			return append(dst, "true"...)
		}
		return append(dst, "false"...)
	default:
		// Fallback: treat as string.
		return appendJSONString(dst, strconv.Itoa(0))
	}
}

// protoKindToString maps the OTel SpanKind enum to the string we store.
// Mirrors otlpKindToString in the HTTP handler.
func protoKindToString(k tracepb.Span_SpanKind) string {
	switch k {
	case tracepb.Span_SPAN_KIND_INTERNAL:
		return "INTERNAL"
	case tracepb.Span_SPAN_KIND_SERVER:
		return "SERVER"
	case tracepb.Span_SPAN_KIND_CLIENT:
		return "CLIENT"
	case tracepb.Span_SPAN_KIND_PRODUCER:
		return "PRODUCER"
	case tracepb.Span_SPAN_KIND_CONSUMER:
		return "CONSUMER"
	default:
		return ""
	}
}

// protoStatusToString maps the OTel Status_StatusCode to our string.
// Mirrors otlpStatusToString in the HTTP handler.
func protoStatusToString(s *tracepb.Status) string {
	if s == nil {
		return "UNSET"
	}
	switch s.Code {
	case tracepb.Status_STATUS_CODE_OK:
		return "OK"
	case tracepb.Status_STATUS_CODE_ERROR:
		return "ERROR"
	default:
		return "UNSET"
	}
}

func protoStatusMessage(s *tracepb.Status) string {
	if s == nil {
		return ""
	}
	return s.Message
}
