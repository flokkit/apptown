package otlpfake

import (
	"encoding/hex"
	"fmt"
	"strings"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricpb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

type (
	metricpbSum       = metricpb.Metric_Sum
	metricpbGauge     = metricpb.Metric_Gauge
	metricpbHistogram = metricpb.Metric_Histogram
)

func hexOf(b []byte) string { return hex.EncodeToString(b) }

func number(v any) float64 {
	switch n := v.(type) {
	case *metricpb.NumberDataPoint_AsInt:
		return float64(n.AsInt)
	case *metricpb.NumberDataPoint_AsDouble:
		return n.AsDouble
	}
	return 0
}

// value flattens an AnyValue to text, so assertions and the canary search
// see every string wherever it is nested.
func value(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_BoolValue:
		return fmt.Sprint(x.BoolValue)
	case *commonpb.AnyValue_IntValue:
		return fmt.Sprint(x.IntValue)
	case *commonpb.AnyValue_DoubleValue:
		return fmt.Sprint(x.DoubleValue)
	case *commonpb.AnyValue_BytesValue:
		return hexOf(x.BytesValue)
	case *commonpb.AnyValue_ArrayValue:
		parts := make([]string, 0, len(x.ArrayValue.Values))
		for _, e := range x.ArrayValue.Values {
			parts = append(parts, value(e))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case *commonpb.AnyValue_KvlistValue:
		parts := make([]string, 0, len(x.KvlistValue.Values))
		for _, kv := range x.KvlistValue.Values {
			parts = append(parts, kv.Key+"="+value(kv.Value))
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	return ""
}
