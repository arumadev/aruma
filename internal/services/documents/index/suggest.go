package index

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
)

// The status a needs-index error carries (IX §2.3). REST answers HTTP 400 with
// {"error":{"code":400,"message":…,"status":"FAILED_PRECONDITION"}}, wrapped in an array on the
// streaming methods (runQuery, runAggregationQuery); gRPC answers code 9 with the same message
// (inferred: only REST was recorded).
const (
	GRPCCode   = 9
	HTTPStatus = 400
	Status     = "FAILED_PRECONDITION"
)

// DefaultDatabase is the default database's id, as it appears in the link's index name.
const DefaultDatabase = "(default)"

// The link parameters (IX §2.3): one composite, one single-field collection-group index, or one
// of several missing indexes of either kind.
const (
	ParamComposite = "create_composite"
	ParamExemption = "create_exemption"
	ParamIndex     = "create_index"
)

// The five message templates, byte for byte as production sends them (IX §2.3, corpus
// template.text).
const (
	// Template 1 (and 5, a collection-group composite): {url}.
	msgIndex = "The query requires an index. You can create it here: "
	// Template 2 is template 1 followed by this: note the space before the full stop and the
	// newline.
	msgMultipleRange = " .\nThe query contains range and inequality filters on multiple fields, please refer to the documentation for index selection best practices: https://cloud.google.com/firestore/docs/query-data/multiple-range-fields."
	// Template 3, with no space after "here:" and create_index repeated per index.
	msgIndexes = "The query requires multiple indexes. You can create them here:"
	// Template 4: "…requires a {kind} index for collection {cg} and field {field}. You can
	// create it here: {url}".
	msgSingleField = "The query requires a "
)

// consoleBase is the Firebase console link up to the project id. The link points at Firebase
// even on Aruma, unchanged and with no hint of Aruma's own: SDKs, users and tools match and parse
// it (ADR-025 §4.3).
const consoleBase = "https://console.firebase.google.com/v1/r/project/"

// Error is production's needs-index error.
type Error struct {
	// Message is the status message, byte for byte.
	Message string
	// Links are the console link's parameters, in order.
	Links []Link
}

func (e *Error) Error() string { return e.Message }

// Link is one parameter of the console link.
type Link struct {
	// Param is ParamComposite, ParamExemption or ParamIndex.
	Param string
	// Payload is the base64 Index proto (EncodeIndex), URL-safe without padding.
	Payload string
}

// Suggest is the error production returns for a NeedsIndex decision on project and database
// (DefaultDatabase for the default one), or nil for any other verdict.
//
// For a named database the database id goes into the index name; the probe ran on (default)
// only, so that form is unverified (R22).
func Suggest(project, database string, d Decision) *Error {
	if d.Verdict != NeedsIndex || len(d.Missing) == 0 {
		return nil
	}
	e := &Error{}
	var msg strings.Builder
	if len(d.Missing) > 1 {
		for _, r := range d.Missing {
			e.Links = append(e.Links, Link{ParamIndex, payload(project, database, r)})
		}
		msg.WriteString(msgIndexes)
		msg.WriteString(e.url(project))
		e.Message = msg.String()
		return e
	}
	r := d.Missing[0]
	if r.Exemption {
		e.Links = []Link{{ParamExemption, payload(project, database, r)}}
		f := r.Index.Fields[0]
		msg.WriteString(msgSingleField)
		msg.WriteString(singleFieldKind(r.Index.Scope, f.Mode))
		msg.WriteString(" index for collection ")
		msg.WriteString(r.Index.CollectionGroup)
		msg.WriteString(" and field ")
		msg.WriteString(f.Path)
		msg.WriteString(". You can create it here: ")
		msg.WriteString(e.url(project))
		e.Message = msg.String()
		return e
	}
	e.Links = []Link{{ParamComposite, payload(project, database, r)}}
	msg.WriteString(msgIndex)
	msg.WriteString(e.url(project))
	if d.MultipleRangeFields {
		msg.WriteString(msgMultipleRange)
	}
	e.Message = msg.String()
	return e
}

// singleFieldKind names a single-field index in template 4: COLLECTION_GROUP_ASC,
// COLLECTION_GROUP_DESC or COLLECTION_GROUP_CONTAINS.
func singleFieldKind(scope Scope, mode FieldMode) string {
	switch mode {
	case Descending:
		return scope.String() + "_DESC"
	case Contains:
		return scope.String() + "_CONTAINS"
	}
	return scope.String() + "_ASC"
}

// url is the console link carrying e's parameters.
func (e *Error) url(project string) string {
	var b strings.Builder
	b.WriteString(consoleBase)
	b.WriteString(project)
	b.WriteString("/firestore/indexes?")
	for i, l := range e.Links {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(l.Param)
		b.WriteByte('=')
		b.WriteString(l.Payload)
	}
	return b.String()
}

func payload(project, database string, r Required) string {
	return base64.RawURLEncoding.EncodeToString(EncodeIndex(project, database, r))
}

// EncodeIndex is the link's payload before base64: a google.firestore.admin.v1.Index message,
// hand-encoded so that this package keeps to Go's standard library (wire/pb/adminpb holds the
// generated type; protocheck holds the two to the same bytes). Fields, in number order:
//
//	1 name         string  projects/{p}/databases/{db}/collectionGroups/{cg}/indexes/_
//	                       (…/fields/{field} for an exemption)
//	2 query_scope  enum    1 COLLECTION, 2 COLLECTION_GROUP
//	3 fields       repeated IndexField:
//	                 1 field_path    string
//	                 2 order         enum  1 ASCENDING, 2 DESCENDING
//	                 3 array_config  enum  1 CONTAINS
//
// A composite's fields end with __name__; an exemption's are its one field.
func EncodeIndex(project, database string, r Required) []byte {
	name := "projects/" + project + "/databases/" + database + "/collectionGroups/" + r.Index.CollectionGroup
	if r.Exemption {
		name += "/fields/" + r.Index.Fields[0].Path
	} else {
		name += "/indexes/_"
	}
	b := appendString(nil, 1, name)
	b = appendVarint(b, 2, uint64(r.Index.Scope))
	for _, f := range r.Index.Fields {
		field := appendString(nil, 1, f.Path)
		switch f.Mode {
		case Contains:
			field = appendVarint(field, 3, 1)
		case Descending:
			field = appendVarint(field, 2, 2)
		default:
			field = appendVarint(field, 2, 1)
		}
		b = appendBytes(b, 3, field)
	}
	return b
}

// Protobuf wire types.
const (
	wireVarint = 0
	wireBytes  = 2
)

func appendVarint(b []byte, field int, v uint64) []byte {
	b = binary.AppendUvarint(b, uint64(field)<<3|wireVarint)
	return binary.AppendUvarint(b, v)
}

func appendBytes(b []byte, field int, v []byte) []byte {
	b = binary.AppendUvarint(b, uint64(field)<<3|wireBytes)
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}

func appendString(b []byte, field int, v string) []byte {
	b = binary.AppendUvarint(b, uint64(field)<<3|wireBytes)
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}
