package wire

import (
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/typepb"
)

// The conversions between the protos and package value are lossless both ways: every bit of a
// double (-0.0, NaN), a timestamp's nanoseconds, bytes, references by their full name, vectors
// and the BSON shapes as the maps they are. What production changes on a write, it changes in
// core, not here: timestamps keep microseconds (value.TruncateMicros; values-order.json t-nanos
// is stored as …123456Z), and BSON shapes lose stray keys (value.CanonicalBSON).
//
// Parsing refuses what production refuses while it reads a value off the wire: a timestamp out
// of range and a malformed reference, with production's messages. Everything else production
// checks on a write (depth, sizes, field names, vectors, BSON content, geopoint ranges) is
// value.Validate's. Over REST, a bad timestamp, integer or base64 string already fails in
// UnmarshalREST, with the field's path in the message as production words it.

// The timestamp range a value may hold: 0001-01-01T00:00:00Z to 9999-12-31T23:59:59.999999999Z
// (values-order.json t-min, t-max; values-validate.json time.*).
const (
	minTimestampSeconds = -62135596800
	maxTimestampSeconds = 253402300799
)

// msgNoValue is the error for a Value with none of its kinds set, at any depth
// (f1-wire-errors.json value.no_type, value.no_type_nested).
const msgNoValue = "Cannot convert firestore.v1.Value with type unset."

// ValueFromProto converts a value off the wire.
func ValueFromProto(v *firestorepb.Value) (value.Value, error) {
	switch x := v.GetValueType().(type) {
	case *firestorepb.Value_NullValue:
		return value.Null(), nil
	case *firestorepb.Value_BooleanValue:
		return value.Bool(x.BooleanValue), nil
	case *firestorepb.Value_IntegerValue:
		return value.Int(x.IntegerValue), nil
	case *firestorepb.Value_DoubleValue:
		return value.Double(x.DoubleValue), nil
	case *firestorepb.Value_TimestampValue:
		return timestampFromProto(x.TimestampValue)
	case *firestorepb.Value_StringValue:
		return value.String(x.StringValue), nil
	case *firestorepb.Value_BytesValue:
		return value.Bytes(x.BytesValue), nil
	case *firestorepb.Value_ReferenceValue:
		if _, err := ParseReference(x.ReferenceValue); err != nil {
			return value.Value{}, err
		}
		return value.Reference(x.ReferenceValue), nil
	case *firestorepb.Value_GeoPointValue:
		return value.GeoPoint(x.GeoPointValue.GetLatitude(), x.GeoPointValue.GetLongitude()), nil
	case *firestorepb.Value_ArrayValue:
		vs := x.ArrayValue.GetValues()
		els := make([]value.Value, len(vs))
		for i, e := range vs {
			var err error
			if els[i], err = ValueFromProto(e); err != nil {
				return value.Value{}, err
			}
		}
		return value.Array(els...), nil
	case *firestorepb.Value_MapValue:
		return FieldsFromProto(x.MapValue.GetFields())
	case nil:
		return value.Value{}, InvalidArgument(msgNoValue)
	}
	// field_reference_value, variable_reference_value, function_value and pipeline_value belong
	// to pipelines (Enterprise edition), not to documents or queries (provisional text).
	return value.Value{}, Unimplemented("Pipeline expressions in values")
}

// FieldsFromProto converts a document's fields, or a map value's, to a map value.
func FieldsFromProto(fields map[string]*firestorepb.Value) (value.Value, error) {
	m := make(map[string]value.Value, len(fields))
	for k, fv := range fields {
		v, err := ValueFromProto(fv)
		if err != nil {
			return value.Value{}, err
		}
		m[k] = v
	}
	return value.MapOf(m), nil
}

// MapFromProto converts a MapValue to a map value.
func MapFromProto(m *firestorepb.MapValue) (value.Value, error) {
	return FieldsFromProto(m.GetFields())
}

func timestampFromProto(ts *timestamppb.Timestamp) (value.Value, error) {
	s, n := ts.GetSeconds(), ts.GetNanos()
	if s < minTimestampSeconds || s > maxTimestampSeconds || n < 0 || n > 999_999_999 {
		// The text is the one REST parsing gives (values-validate.json time.year_0); gRPC's is
		// provisional.
		return value.Value{}, InvalidArgument("Timestamp value exceeds limits")
	}
	return value.Timestamp(s, n), nil
}

// ValueToProto converts a value to the wire.
func ValueToProto(v value.Value) *firestorepb.Value {
	switch v.Kind() {
	case value.KindBoolean:
		return &firestorepb.Value{ValueType: &firestorepb.Value_BooleanValue{BooleanValue: v.BoolValue()}}
	case value.KindInteger:
		return &firestorepb.Value{ValueType: &firestorepb.Value_IntegerValue{IntegerValue: v.IntValue()}}
	case value.KindDouble:
		return &firestorepb.Value{ValueType: &firestorepb.Value_DoubleValue{DoubleValue: v.DoubleValue()}}
	case value.KindTimestamp:
		s, n := v.TimestampValue()
		return &firestorepb.Value{ValueType: &firestorepb.Value_TimestampValue{
			TimestampValue: &timestamppb.Timestamp{Seconds: s, Nanos: n}}}
	case value.KindString:
		return &firestorepb.Value{ValueType: &firestorepb.Value_StringValue{StringValue: v.StringValue()}}
	case value.KindBytes:
		return &firestorepb.Value{ValueType: &firestorepb.Value_BytesValue{BytesValue: v.BytesValue()}}
	case value.KindReference:
		return &firestorepb.Value{ValueType: &firestorepb.Value_ReferenceValue{ReferenceValue: v.StringValue()}}
	case value.KindGeoPoint:
		lat, lng := v.GeoPointValue()
		return &firestorepb.Value{ValueType: &firestorepb.Value_GeoPointValue{
			GeoPointValue: &typepb.LatLng{Latitude: lat, Longitude: lng}}}
	case value.KindArray:
		els := v.Elements()
		vs := make([]*firestorepb.Value, len(els))
		for i, e := range els {
			vs[i] = ValueToProto(e)
		}
		return &firestorepb.Value{ValueType: &firestorepb.Value_ArrayValue{ArrayValue: &firestorepb.ArrayValue{Values: vs}}}
	case value.KindMap:
		return &firestorepb.Value{ValueType: &firestorepb.Value_MapValue{MapValue: MapToProto(v)}}
	}
	return &firestorepb.Value{ValueType: &firestorepb.Value_NullValue{}}
}

// FieldsToProto converts a map value to a document's fields; nil for an empty map, which the
// wire does not tell from no fields.
func FieldsToProto(m value.Value) map[string]*firestorepb.Value {
	fs := m.Fields()
	if len(fs) == 0 {
		return nil
	}
	out := make(map[string]*firestorepb.Value, len(fs))
	for _, f := range fs {
		out[f.Name] = ValueToProto(f.Value)
	}
	return out
}

// MapToProto converts a map value to a MapValue.
func MapToProto(m value.Value) *firestorepb.MapValue {
	return &firestorepb.MapValue{Fields: FieldsToProto(m)}
}

// DocumentFromProto reads a document off the wire, as a write's update carries it: its name,
// parsed with ParseDocumentName, and its fields. The times are output only and are not read.
func DocumentFromProto(d *firestorepb.Document) (Name, value.Value, error) {
	n, err := ParseDocumentName(d.GetName())
	if err != nil {
		return Name{}, value.Value{}, err
	}
	fields, err := FieldsFromProto(d.GetFields())
	if err != nil {
		return Name{}, value.Value{}, err
	}
	return n, fields, nil
}

// DocumentToProto is a document as reads return it. createTime and updateTime may be nil, as
// for a missing document listed with show_missing, which production returns as its name alone
// (f0-list-show-missing.json).
func DocumentToProto(name string, fields value.Value, createTime, updateTime *timestamppb.Timestamp) *firestorepb.Document {
	return &firestorepb.Document{Name: name, Fields: FieldsToProto(fields), CreateTime: createTime, UpdateTime: updateTime}
}

// CheckMicros refuses a time a request gives as a read time or a precondition's update_time
// when it is finer than microseconds: "timestamp cannot have more than microseconds precision"
// (f1-get.json read_time.nanoseconds, f1-update.json update_time_plus_1ns). A value's timestamp
// is cut to microseconds instead (value.TruncateMicros).
func CheckMicros(ts *timestamppb.Timestamp) error {
	if ts.GetNanos()%1000 != 0 {
		return InvalidArgument("timestamp cannot have more than microseconds precision")
	}
	return nil
}

// CheckReadTime checks a read_time as production does before its window: CheckMicros, then
// "Only timestamps past epoch are supported." for the epoch and before (f1-get.json
// read_time.epoch). Too old and future are core's, which knows the clock.
func CheckReadTime(ts *timestamppb.Timestamp) error {
	if err := CheckMicros(ts); err != nil {
		return err
	}
	if ts.GetSeconds() < 0 || ts.GetSeconds() == 0 && ts.GetNanos() == 0 {
		return InvalidArgument("Only timestamps past epoch are supported.")
	}
	return nil
}
