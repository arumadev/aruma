package wire

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// REST speaks proto3 JSON (firestore-clients.md §2.5): lowerCamel field names, int64 and uint64
// as strings, bytes as base64, timestamps as RFC 3339 in UTC with 0, 3, 6 or 9 fractional
// digits (values-validate.json time.offset is stored as 2026-01-01T00:00:00.500Z), and doubles
// as numbers or "NaN", "Infinity", "-Infinity".
//
// Requests are read as Google's front end reads them, not as protojson does, so a refused body
// gets production's message with the path of the field at fault, in proto field names:
//
//	Invalid value at 'writes[0].update.fields[0].value.integer_value' (TYPE_INT64), "9223372036854775808"
//	Invalid value at 'writes[0].update.fields[0].value.bytes_value' (TYPE_BYTES), Base64 decoding failed for "***"
//	Invalid value at 'writes[0].update.fields[0].value.timestamp_value' (type.googleapis.com/google.protobuf.Timestamp), Field 'timestampValue', Timestamp value exceeds limits
//	Invalid value (oneof), oneof field 'consistency_selector' is already set. Cannot set 'newTransaction'
//	Invalid value at 'current_document' (oneof), oneof field 'condition_type' is already set. Cannot set 'update_time'
//	Invalid value at 'page_size' (TYPE_INT32), "two"
//
// (values-validate.json int.overflow, bytes.bad_base64, time.year_0, time.year_10000;
// f1-batch-get.json txn.and_new_transaction; f1-update.json exists_and_update_time;
// f1-list-documents.json page_size_not_number). The front end reads on past a bad member and
// reports every problem, one per line. A map entry is addressed by its position in the body,
// fields[0]. A field may be named by its JSON name or its proto name. An unknown field is refused,
// never dropped (ADR-028 §6), with the front end's "Invalid JSON payload received. Unknown name
// …: Cannot find field."; that text, and the others not cited here, are provisional.

// UnmarshalREST decodes a REST request body into m. An empty body is an empty message. Every
// error is INVALID_ARGUMENT.
//
// pathFields name the fields of m the REST path fills (database, parent, name), which the
// front end binds after the body. When the body set a top-level oneof member that was already
// set, with a value that is not an object, the front end then fails to bind each of them too,
// and says so on a line of its own: "Invalid JSON payload received. Unknown name "database":
// Root element must be a message." (f1-batch-get.json txn.and_read_time; one recording).
func UnmarshalREST(body []byte, m proto.Message, pathFields ...string) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	n, err := parseJSON(body)
	if err != nil {
		return err
	}
	if n.kind != jsonObject {
		return InvalidArgument("Invalid JSON payload received. Root element must be a message.")
	}
	var d decoder
	d.message(m.ProtoReflect(), n, "", "")
	if d.poisoned {
		for _, f := range pathFields {
			d.errs = append(d.errs, `Invalid JSON payload received. Unknown name "`+f+`": Root element must be a message.`)
		}
	}
	return d.err()
}

// The front end's system parameters, which any REST call may carry and no request message
// holds: the API key and the rendering options. They are not bound.
var systemParams = map[string]bool{
	"key": true, "alt": true, "$alt": true, "fields": true, "$fields": true, "prettyPrint": true,
	"$prettyPrint": true, "quotaUser": true, "$quotaUser": true, "userIp": true, "callback": true,
	"$callback": true, "access_token": true, "oauth_token": true, "upload_protocol": true,
	"uploadType": true, "$.xgafv": true,
}

// UnmarshalQuery binds REST query parameters into m as the front end does: a parameter names a
// field by its dotted path (mask.fieldPaths, currentDocument.exists, readTime), in JSON or proto
// names; a repeated field takes every value given; a scalar is read from its text as a JSON
// string would be (an integer, a boolean, base64 bytes, an enum's name, an RFC 3339 time).
// Parameters are bound in the order of their names, and a oneof member is named by its proto
// name: ?transaction=…&readTime=… is refused with "… Cannot set 'transaction'"
// (f1-get.json txn.and_read_time). System parameters (key, alt, fields, prettyPrint, …) are
// skipped. An unknown parameter is INVALID_ARGUMENT (provisional text).
func UnmarshalQuery(q url.Values, m proto.Message) error {
	d := decoder{query: true}
	for _, key := range slices.Sorted(maps.Keys(q)) {
		if !systemParams[key] {
			d.bindQuery(m.ProtoReflect(), key, q[key])
		}
	}
	return d.err()
}

func (d *decoder) bindQuery(m protoreflect.Message, key string, vals []string) {
	segs := strings.Split(key, ".")
	path := ""
	for i, seg := range segs {
		fd := findField(m.Descriptor(), seg)
		last := i == len(segs)-1
		if fd == nil || fd.IsMap() || (!last && (fd.IsList() || fd.Message() == nil || isWellKnown(fd.Message()))) {
			d.fail(`Invalid JSON payload received. Unknown name "` + key +
				`": Cannot bind query parameter. Field '` + seg + `' could not be found in request message.`)
			return
		}
		if !last {
			path = joinPath(path, string(fd.Name()))
			m = m.Mutable(fd).Message()
			continue
		}
		if len(vals) == 0 {
			return
		}
		if fd.IsList() {
			arr := &jsonNode{kind: jsonArray}
			for _, v := range vals {
				arr.elems = append(arr.elems, &jsonNode{kind: jsonString, s: v})
			}
			d.member(m, fd, arr, path, seg)
			return
		}
		d.member(m, fd, &jsonNode{kind: jsonString, s: vals[len(vals)-1]}, path, seg)
	}
}

// MarshalREST encodes m as production's REST answers it: proto3 JSON with lowerCamel names and
// default values omitted (a document listed with show_missing is {"name": …} alone,
// f0-list-show-missing.json; PartitionQuery with no partitions is {}, f0-descendants.json).
func MarshalREST(m proto.Message) ([]byte, error) {
	b, err := protojson.Marshal(m)
	if err != nil {
		return nil, err
	}
	return compactJSON(b), nil
}

// MarshalRESTStream encodes the responses of a streaming method (batchGet, runQuery,
// runAggregationQuery) as production does: one JSON array of them.
func MarshalRESTStream[M proto.Message](ms []M) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, m := range ms {
		if i > 0 {
			buf.WriteByte(',')
		}
		b, err := MarshalREST(m)
		if err != nil {
			return nil, err
		}
		buf.Write(b)
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}

// The JSON tree a body is read into first: the order of an object's members is kept (a map
// entry's position is its address in an error) and a number keeps its text.
type jsonKind uint8

const (
	jsonNull jsonKind = iota
	jsonBool
	jsonNumber
	jsonString
	jsonObject
	jsonArray
)

type jsonNode struct {
	kind    jsonKind
	b       bool
	s       string // a string's value, a number's text
	members []jsonMember
	elems   []*jsonNode
}

type jsonMember struct {
	key string
	val *jsonNode
}

func errSyntax() *Error { return InvalidArgument("Invalid JSON payload received. Unexpected token.") }

func parseJSON(body []byte) (*jsonNode, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	n, err := readJSON(dec)
	if err != nil {
		return nil, errSyntax()
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errSyntax()
	}
	return n, nil
}

func readJSON(dec *json.Decoder) (*jsonNode, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case nil:
		return &jsonNode{kind: jsonNull}, nil
	case bool:
		return &jsonNode{kind: jsonBool, b: t}, nil
	case json.Number:
		return &jsonNode{kind: jsonNumber, s: string(t)}, nil
	case string:
		return &jsonNode{kind: jsonString, s: t}, nil
	case json.Delim:
		n := &jsonNode{kind: jsonObject}
		if t == '[' {
			n.kind = jsonArray
		}
		for dec.More() {
			if n.kind == jsonObject {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := kt.(string)
				v, err := readJSON(dec)
				if err != nil {
					return nil, err
				}
				n.members = append(n.members, jsonMember{key, v})
				continue
			}
			v, err := readJSON(dec)
			if err != nil {
				return nil, err
			}
			n.elems = append(n.elems, v)
		}
		if _, err := dec.Token(); err != nil { // the closing delimiter
			return nil, err
		}
		return n, nil
	}
	return nil, errors.New("unexpected token")
}

// raw renders n as the front end quotes a value in a message: a string in double quotes, as it
// is, and anything else as JSON.
func (n *jsonNode) raw() string {
	if n.kind == jsonString {
		return `"` + n.s + `"`
	}
	var b strings.Builder
	n.write(&b)
	return b.String()
}

func (n *jsonNode) write(b *strings.Builder) {
	switch n.kind {
	case jsonNull:
		b.WriteString("null")
	case jsonBool:
		b.WriteString(strconv.FormatBool(n.b))
	case jsonNumber:
		b.WriteString(n.s)
	case jsonString:
		q, _ := json.Marshal(n.s)
		b.Write(q)
	case jsonObject:
		b.WriteByte('{')
		for i, m := range n.members {
			if i > 0 {
				b.WriteByte(',')
			}
			q, _ := json.Marshal(m.key)
			b.Write(q)
			b.WriteByte(':')
			m.val.write(b)
		}
		b.WriteByte('}')
	case jsonArray:
		b.WriteByte('[')
		for i, e := range n.elems {
			if i > 0 {
				b.WriteByte(',')
			}
			e.write(b)
		}
		b.WriteByte(']')
	}
}

func joinPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

// decoder reads JSON into messages, collecting the front end's error messages as it goes.
type decoder struct {
	errs []string
	// query is set when binding query parameters, whose oneof members errors name by proto name.
	query bool
	// poisoned is set by a top-level oneof conflict on a value that is not an object
	// (UnmarshalREST).
	poisoned bool
}

func (d *decoder) fail(msg string) { d.errs = append(d.errs, msg) }

// err is the front end's answer to the problems found: one INVALID_ARGUMENT, a line per
// problem, with a google.rpc.BadRequest that holds a field violation per line, its field the
// path the line names, if any (f1-update.json exists_and_update_time, f1-get.json
// txn.not_base64, f1-list-documents.json page_size_not_number, f1-batch-get.json txn.*).
func (d *decoder) err() error {
	if len(d.errs) == 0 {
		return nil
	}
	br := &errdetails.BadRequest{}
	for _, msg := range d.errs {
		v := &errdetails.BadRequest_FieldViolation{Description: msg}
		if _, rest, ok := strings.Cut(msg, " at '"); ok {
			v.Field, _, _ = strings.Cut(rest, "'")
		}
		br.FieldViolations = append(br.FieldViolations, v)
	}
	return InvalidArgument(strings.Join(d.errs, "\n")).withDetails(br)
}

// at is " at '{path}'", or nothing at the top.
func at(path string) string {
	if path == "" {
		return ""
	}
	return " at '" + path + "'"
}

// unknownName is the front end's error for a member it cannot place, in the message at parent.
func (d *decoder) unknownName(key, parent, reason string) {
	d.fail(`Invalid JSON payload received. Unknown name "` + key + `"` + at(parent) + ": " + reason)
}

// invalidValue is the front end's error for a value its field cannot hold.
func (d *decoder) invalidValue(path, typ, detail string) {
	d.fail("Invalid value" + at(path) + " (" + typ + "), " + detail)
}

func findField(md protoreflect.MessageDescriptor, key string) protoreflect.FieldDescriptor {
	fds := md.Fields()
	if fd := fds.ByJSONName(key); fd != nil {
		return fd
	}
	return fds.ByName(protoreflect.Name(key))
}

const wktPackage = "google.protobuf"

func isWellKnown(md protoreflect.MessageDescriptor) bool {
	return md.ParentFile().Package() == wktPackage
}

// message reads n into m, found at path; jsonName is its field's JSON name, which a timestamp's
// error names.
func (d *decoder) message(m protoreflect.Message, n *jsonNode, path, jsonName string) {
	md := m.Descriptor()
	if isWellKnown(md) {
		d.wellKnown(m, n, path, jsonName)
		return
	}
	if n.kind != jsonObject {
		d.invalidValue(path, "type.googleapis.com/"+string(md.FullName()), n.raw())
		return
	}
	for _, mem := range n.members {
		fd := findField(md, mem.key)
		if fd == nil {
			d.unknownName(mem.key, path, "Cannot find field.")
			continue
		}
		d.member(m, fd, mem.val, path, mem.key)
	}
}

// member reads v into field fd of m, the message at parent, named key in the request.
func (d *decoder) member(m protoreflect.Message, fd protoreflect.FieldDescriptor, v *jsonNode, parent, key string) {
	if od := fd.ContainingOneof(); od != nil && !od.IsSynthetic() {
		if set := m.WhichOneof(od); set != nil && set != fd {
			name := key
			if d.query {
				name = string(fd.Name())
			}
			d.fail("Invalid value" + at(parent) + " (oneof), oneof field '" + string(od.Name()) +
				"' is already set. Cannot set '" + name + "'")
			if parent == "" && v.kind != jsonObject {
				d.poisoned = true
			}
			return
		}
	}
	path := joinPath(parent, string(fd.Name()))
	if v.kind == jsonNull && !acceptsNull(fd) {
		m.Clear(fd)
		return
	}
	switch {
	case fd.IsMap():
		if v.kind != jsonObject {
			d.invalidValue(path, typeName(fd), v.raw())
			return
		}
		mp := m.Mutable(fd).Map()
		for i, mem := range v.members {
			entry := path + "[" + strconv.Itoa(i) + "]"
			mk, ok := d.scalar(fd.MapKey(), &jsonNode{kind: jsonString, s: mem.key}, entry+".key", "key", entry)
			if !ok {
				continue
			}
			vfd := fd.MapValue()
			var mv protoreflect.Value
			if vfd.Message() != nil {
				mv = mp.NewValue()
				before := len(d.errs)
				d.message(mv.Message(), mem.val, entry+".value", vfd.JSONName())
				ok = len(d.errs) == before
			} else {
				mv, ok = d.scalar(vfd, mem.val, entry+".value", "value", entry)
			}
			if ok {
				mp.Set(mk.MapKey(), mv)
			}
		}
	case fd.IsList():
		if v.kind != jsonArray {
			d.invalidValue(path, typeName(fd), v.raw())
			return
		}
		l := m.Mutable(fd).List()
		for i, el := range v.elems {
			at := path + "[" + strconv.Itoa(i) + "]"
			if fd.Message() != nil {
				ev := l.NewElement()
				if el.kind != jsonNull || acceptsNull(fd) {
					d.message(ev.Message(), el, at, fd.JSONName())
				}
				l.Append(ev)
			} else if ev, ok := d.scalar(fd, el, at, key, parent); ok {
				l.Append(ev)
			}
		}
	case fd.Message() != nil:
		if v.kind == jsonArray && !isWellKnown(fd.Message()) {
			d.unknownName(key, parent, "Proto field is not repeating, cannot start list.")
			return
		}
		mv := m.NewField(fd)
		d.message(mv.Message(), v, path, fd.JSONName())
		m.Set(fd, mv)
	default:
		if pv, ok := d.scalar(fd, v, path, key, parent); ok {
			m.Set(fd, pv)
		}
	}
}

// acceptsNull reports whether null is a value of fd rather than its absence: the
// google.protobuf.NullValue enum (Firestore's nullValue) and google.protobuf.Value.
func acceptsNull(fd protoreflect.FieldDescriptor) bool {
	if e := fd.Enum(); e != nil {
		return e.FullName() == "google.protobuf.NullValue"
	}
	if md := fd.Message(); md != nil {
		return md.FullName() == "google.protobuf.Value"
	}
	return false
}

// typeName names fd's type as the front end does in "Invalid value" messages.
func typeName(fd protoreflect.FieldDescriptor) string {
	if e := fd.Enum(); e != nil {
		return "type.googleapis.com/" + string(e.FullName())
	}
	if md := fd.Message(); md != nil {
		return "type.googleapis.com/" + string(md.FullName())
	}
	return "TYPE_" + strings.ToUpper(fd.Kind().String())
}

// scalar reads a scalar of fd's kind from v, found at path as member key of the message at
// parent. A JSON array or object for a scalar is the front end's "Unknown name" error.
func (d *decoder) scalar(fd protoreflect.FieldDescriptor, v *jsonNode, path, key, parent string) (protoreflect.Value, bool) {
	switch v.kind {
	case jsonArray:
		d.unknownName(key, parent, "Proto field is not repeating, cannot start list.")
		return protoreflect.Value{}, false
	case jsonObject:
		d.unknownName(key, parent, "Starting an object on a scalar field")
		return protoreflect.Value{}, false
	}
	switch fd.Kind() {
	case protoreflect.BoolKind:
		if b, ok := jsonBoolValue(v); ok {
			return protoreflect.ValueOfBool(b), true
		}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		if i, ok := jsonInt(v, 32); ok {
			return protoreflect.ValueOfInt32(int32(i)), true
		}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		if i, ok := jsonInt(v, 64); ok {
			return protoreflect.ValueOfInt64(i), true
		}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		if u, ok := jsonUint(v, 32); ok {
			return protoreflect.ValueOfUint32(uint32(u)), true
		}
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		if u, ok := jsonUint(v, 64); ok {
			return protoreflect.ValueOfUint64(u), true
		}
	case protoreflect.FloatKind:
		if f, ok := jsonFloat(v); ok && (math.IsInf(f, 0) || math.IsNaN(f) || math.Abs(f) <= math.MaxFloat32) {
			return protoreflect.ValueOfFloat32(float32(f)), true
		}
	case protoreflect.DoubleKind:
		if f, ok := jsonFloat(v); ok {
			return protoreflect.ValueOfFloat64(f), true
		}
	case protoreflect.StringKind:
		if v.kind == jsonString {
			return protoreflect.ValueOfString(v.s), true
		}
	case protoreflect.BytesKind:
		if v.kind == jsonString {
			if b, ok := decodeBase64(v.s); ok {
				return protoreflect.ValueOfBytes(b), true
			}
			d.invalidValue(path, typeName(fd), "Base64 decoding failed for "+v.raw())
			return protoreflect.Value{}, false
		}
	case protoreflect.EnumKind:
		if v.kind == jsonNull {
			return protoreflect.ValueOfEnum(0), true
		}
		if v.kind == jsonString {
			if ev := fd.Enum().Values().ByName(protoreflect.Name(v.s)); ev != nil {
				return protoreflect.ValueOfEnum(ev.Number()), true
			}
		}
		if i, ok := jsonInt(v, 32); ok {
			return protoreflect.ValueOfEnum(protoreflect.EnumNumber(i)), true
		}
	}
	d.invalidValue(path, typeName(fd), v.raw())
	return protoreflect.Value{}, false
}

func jsonBoolValue(v *jsonNode) (bool, bool) {
	switch v.kind {
	case jsonBool:
		return v.b, true
	case jsonString:
		// The front end reads a boolean's text as safe_strtob does (query parameters are text).
		switch strings.ToLower(v.s) {
		case "true", "t", "yes", "y", "1":
			return true, true
		case "false", "f", "no", "n", "0":
			return false, true
		}
	}
	return false, false
}

// jsonInt reads a signed integer of bits bits: a string holds its decimal digits; a number may
// be written with a fraction or an exponent when its value is a whole number in range.
func jsonInt(v *jsonNode, bits int) (int64, bool) {
	switch v.kind {
	case jsonString:
		i, err := strconv.ParseInt(v.s, 10, bits)
		return i, err == nil
	case jsonNumber:
		if i, err := strconv.ParseInt(v.s, 10, bits); err == nil {
			return i, true
		}
		f, err := strconv.ParseFloat(v.s, 64)
		limit := math.Ldexp(1, bits-1)
		if err != nil || f != math.Trunc(f) || f < -limit || f >= limit {
			return 0, false
		}
		return int64(f), true
	}
	return 0, false
}

func jsonUint(v *jsonNode, bits int) (uint64, bool) {
	switch v.kind {
	case jsonString:
		u, err := strconv.ParseUint(v.s, 10, bits)
		return u, err == nil
	case jsonNumber:
		if u, err := strconv.ParseUint(v.s, 10, bits); err == nil {
			return u, true
		}
		f, err := strconv.ParseFloat(v.s, 64)
		if err != nil || f != math.Trunc(f) || f < 0 || f >= math.Ldexp(1, bits) {
			return 0, false
		}
		return uint64(f), true
	}
	return 0, false
}

// jsonFloat reads a double: a number, or a string holding "NaN", "Infinity", "-Infinity" or a
// decimal number ("-0" too). A number out of range is refused.
func jsonFloat(v *jsonNode) (float64, bool) {
	switch v.kind {
	case jsonNumber:
		f, err := strconv.ParseFloat(v.s, 64)
		return f, err == nil
	case jsonString:
		switch v.s {
		case "NaN":
			return math.NaN(), true
		case "Infinity":
			return math.Inf(1), true
		case "-Infinity":
			return math.Inf(-1), true
		}
		if v.s == "" || strings.Trim(v.s, "0123456789+-.eE") != "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(v.s, 64)
		return f, err == nil
	}
	return 0, false
}

// decodeBase64 reads base64 in the standard or the URL-safe alphabet, padded or not.
func decodeBase64(s string) ([]byte, bool) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, true
		}
	}
	return nil, false
}

// wellKnown reads a google.protobuf type. Timestamps are read here, for production's messages;
// the others (Duration, FieldMask, the wrappers, Struct and Value, Empty) by protojson, any
// failure reported as an invalid value of the type.
func (d *decoder) wellKnown(m protoreflect.Message, n *jsonNode, path, jsonName string) {
	md := m.Descriptor()
	typeURL := "type.googleapis.com/" + string(md.FullName())
	if md.FullName() == "google.protobuf.Timestamp" {
		if n.kind != jsonString {
			d.invalidValue(path, typeURL, n.raw())
			return
		}
		s, ns, e := parseTimestamp(n.s)
		if e != "" {
			d.invalidValue(path, typeURL, "Field '"+jsonName+"', "+e)
			return
		}
		m.Set(md.Fields().ByName("seconds"), protoreflect.ValueOfInt64(s))
		m.Set(md.Fields().ByName("nanos"), protoreflect.ValueOfInt32(ns))
		return
	}
	var b strings.Builder
	n.write(&b)
	if err := protojson.Unmarshal([]byte(b.String()), m.Interface()); err != nil {
		d.invalidValue(path, typeURL, n.raw())
	}
}

// parseTimestamp reads an RFC 3339 time, YYYY-MM-DDTHH:MM:SS[.fraction](Z|±HH:MM), into seconds
// and nanoseconds since the epoch. A time outside 0001-01-01T00:00:00Z to 9999-12-31T23:59:59.
// 999999999Z, after its offset is applied, "exceeds limits" (values-validate.json time.year_0,
// time.year_10000); a malformed one gets the front end's format error (provisional text).
func parseTimestamp(s string) (int64, int32, string) {
	const (
		bad      = "Invalid time format: Failed to parse input"
		tooLarge = "Timestamp value exceeds limits"
	)
	ymd, rest, ok := strings.Cut(s, "T")
	if !ok {
		return 0, 0, bad
	}
	yStr, md, ok := strings.Cut(ymd, "-")
	if !ok || len(yStr) < 4 || !allDigits(yStr) || len(md) != 5 || md[2] != '-' {
		return 0, 0, bad
	}
	year, err := strconv.Atoi(yStr)
	month, ok1 := twoDigits(md[0:2])
	day, ok2 := twoDigits(md[3:5])
	if err != nil || !ok1 || !ok2 || len(rest) < 9 || rest[2] != ':' || rest[5] != ':' {
		return 0, 0, bad
	}
	hour, ok1 := twoDigits(rest[0:2])
	minute, ok2 := twoDigits(rest[3:5])
	sec, ok3 := twoDigits(rest[6:8])
	if !ok1 || !ok2 || !ok3 {
		return 0, 0, bad
	}
	rest = rest[8:]
	nanos := 0
	if frac, ok := strings.CutPrefix(rest, "."); ok {
		i := 0
		for i < len(frac) && isDigit(frac[i]) {
			i++
		}
		if i == 0 || i > 9 {
			return 0, 0, bad
		}
		digits := frac[:i] + strings.Repeat("0", 9-i)
		nanos, _ = strconv.Atoi(digits)
		rest = frac[i:]
	}
	offset := 0
	switch {
	case rest == "Z":
	case len(rest) == 6 && (rest[0] == '+' || rest[0] == '-') && rest[3] == ':':
		oh, ok1 := twoDigits(rest[1:3])
		om, ok2 := twoDigits(rest[4:6])
		if !ok1 || !ok2 || oh > 23 || om > 59 {
			return 0, 0, bad
		}
		offset = oh*3600 + om*60
		if rest[0] == '-' {
			offset = -offset
		}
	default:
		return 0, 0, bad
	}
	if month < 1 || month > 12 || day < 1 || hour > 23 || minute > 59 || sec > 59 {
		return 0, 0, bad
	}
	t := time.Date(year, time.Month(month), day, hour, minute, sec, 0, time.UTC)
	if t.Day() != day {
		return 0, 0, bad // February 30th and the like
	}
	secs := t.Unix() - int64(offset)
	if year > 9999 || secs < minTimestampSeconds || secs > maxTimestampSeconds {
		return 0, 0, tooLarge
	}
	return secs, int32(nanos), ""
}

func twoDigits(s string) (int, bool) {
	if len(s) != 2 || !isDigit(s[0]) || !isDigit(s[1]) {
		return 0, false
	}
	return int(s[0]-'0')*10 + int(s[1]-'0'), true
}

func allDigits(s string) bool {
	for i := range len(s) {
		if !isDigit(s[i]) {
			return false
		}
	}
	return s != ""
}
