package core

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/typepb"
)

// The stored form of a document (ADR-024 §1.2): google.firestore.v1.Document in binary protobuf,
// deterministic. documents.doc and versions.doc hold the fields alone; the changelog's images
// hold their times too. This is storage, not the wire: the transports convert values to their
// own messages (wire/convert.go), and this file need not follow them but must read what it wrote.

var marshal = proto.MarshalOptions{Deterministic: true}

// encodeDocument encodes fields (a map value) with the times that are not zero.
func encodeDocument(fields value.Value, createTime, updateTime time.Time) ([]byte, error) {
	d := &firestorepb.Document{Fields: fieldsToPB(fields)}
	if !createTime.IsZero() {
		d.CreateTime = timestamppb.New(createTime)
	}
	if !updateTime.IsZero() {
		d.UpdateTime = timestamppb.New(updateTime)
	}
	return marshal.Marshal(d)
}

// decodeDocument decodes a stored document: its fields, and the times it holds (zero if none).
func decodeDocument(b []byte) (fields value.Value, createTime, updateTime time.Time, err error) {
	var d firestorepb.Document
	if err := proto.Unmarshal(b, &d); err != nil {
		return value.Value{}, time.Time{}, time.Time{}, err
	}
	if fields, err = fieldsFromPB(d.GetFields()); err != nil {
		return value.Value{}, time.Time{}, time.Time{}, err
	}
	if d.CreateTime != nil {
		createTime = d.CreateTime.AsTime()
	}
	if d.UpdateTime != nil {
		updateTime = d.UpdateTime.AsTime()
	}
	return fields, createTime, updateTime, nil
}

func fieldsToPB(m value.Value) map[string]*firestorepb.Value {
	fs := m.Fields()
	if len(fs) == 0 {
		return nil
	}
	out := make(map[string]*firestorepb.Value, len(fs))
	for _, f := range fs {
		out[f.Name] = toPB(f.Value)
	}
	return out
}

func toPB(v value.Value) *firestorepb.Value {
	switch v.Kind() {
	case value.KindBoolean:
		return &firestorepb.Value{ValueType: &firestorepb.Value_BooleanValue{BooleanValue: v.BoolValue()}}
	case value.KindInteger:
		return &firestorepb.Value{ValueType: &firestorepb.Value_IntegerValue{IntegerValue: v.IntValue()}}
	case value.KindDouble:
		return &firestorepb.Value{ValueType: &firestorepb.Value_DoubleValue{DoubleValue: v.DoubleValue()}}
	case value.KindTimestamp:
		s, n := v.TimestampValue()
		return &firestorepb.Value{ValueType: &firestorepb.Value_TimestampValue{TimestampValue: &timestamppb.Timestamp{Seconds: s, Nanos: n}}}
	case value.KindString:
		return &firestorepb.Value{ValueType: &firestorepb.Value_StringValue{StringValue: v.StringValue()}}
	case value.KindBytes:
		return &firestorepb.Value{ValueType: &firestorepb.Value_BytesValue{BytesValue: v.BytesValue()}}
	case value.KindReference:
		return &firestorepb.Value{ValueType: &firestorepb.Value_ReferenceValue{ReferenceValue: v.StringValue()}}
	case value.KindGeoPoint:
		lat, lng := v.GeoPointValue()
		return &firestorepb.Value{ValueType: &firestorepb.Value_GeoPointValue{GeoPointValue: &typepb.LatLng{Latitude: lat, Longitude: lng}}}
	case value.KindArray:
		els := v.Elements()
		a := &firestorepb.ArrayValue{Values: make([]*firestorepb.Value, len(els))}
		for i, e := range els {
			a.Values[i] = toPB(e)
		}
		return &firestorepb.Value{ValueType: &firestorepb.Value_ArrayValue{ArrayValue: a}}
	case value.KindMap:
		return &firestorepb.Value{ValueType: &firestorepb.Value_MapValue{MapValue: &firestorepb.MapValue{Fields: fieldsToPB(v)}}}
	}
	return &firestorepb.Value{ValueType: &firestorepb.Value_NullValue{NullValue: structpb.NullValue_NULL_VALUE}}
}

func fieldsFromPB(fs map[string]*firestorepb.Value) (value.Value, error) {
	m := make(map[string]value.Value, len(fs))
	for k, x := range fs {
		v, err := fromPB(x)
		if err != nil {
			return value.Value{}, err
		}
		m[k] = v
	}
	return value.MapOf(m), nil
}

func fromPB(x *firestorepb.Value) (value.Value, error) {
	switch t := x.GetValueType().(type) {
	case *firestorepb.Value_NullValue:
		return value.Null(), nil
	case *firestorepb.Value_BooleanValue:
		return value.Bool(t.BooleanValue), nil
	case *firestorepb.Value_IntegerValue:
		return value.Int(t.IntegerValue), nil
	case *firestorepb.Value_DoubleValue:
		return value.Double(t.DoubleValue), nil
	case *firestorepb.Value_TimestampValue:
		return value.Timestamp(t.TimestampValue.GetSeconds(), t.TimestampValue.GetNanos()), nil
	case *firestorepb.Value_StringValue:
		return value.String(t.StringValue), nil
	case *firestorepb.Value_BytesValue:
		return value.Bytes(t.BytesValue), nil
	case *firestorepb.Value_ReferenceValue:
		return value.Reference(t.ReferenceValue), nil
	case *firestorepb.Value_GeoPointValue:
		return value.GeoPoint(t.GeoPointValue.GetLatitude(), t.GeoPointValue.GetLongitude()), nil
	case *firestorepb.Value_ArrayValue:
		vs := make([]value.Value, len(t.ArrayValue.GetValues()))
		for i, e := range t.ArrayValue.GetValues() {
			v, err := fromPB(e)
			if err != nil {
				return value.Value{}, err
			}
			vs[i] = v
		}
		return value.Array(vs...), nil
	case *firestorepb.Value_MapValue:
		return fieldsFromPB(t.MapValue.GetFields())
	}
	return value.Value{}, fmt.Errorf("documents: stored value of unknown type %T", x.GetValueType())
}
