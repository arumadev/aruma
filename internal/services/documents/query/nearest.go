package query

import (
	"slices"
	"strconv"
	"strings"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// FindNearest is a query's vector search (StructuredQuery.find_nearest). v0.4.0 answers it as
// production does without a matching vector index, and refuses it with one (ADR-025 §5):
// production serves it then, so the refusal is the parity exception
// find-nearest-with-vector-index.
type FindNearest struct {
	Field     value.FieldPath
	Dimension int
}

// The bounds of a find_nearest (query.proto: limit at most 1000; a vector index's dimension at
// most 2048) and its errors, provisional: production's texts for a malformed find_nearest are
// not recorded.
const (
	maxNearestLimit    = 1000
	maxVectorDimension = 2048
)

var (
	errNearestField   = wire.InvalidArgument("find_nearest.vector_field must be set.")
	errNearestVector  = wire.InvalidArgument("find_nearest.query_vector must be a vector.")
	errNearestMeasure = wire.InvalidArgument("find_nearest.distance_measure must be set.")
	errNearestLimit   = wire.InvalidArgument("find_nearest.limit must be between 1 and 1000.")
	errNearestServed  = wire.Unimplemented("findNearest (vector search)")
)

func findNearestOf(fn *pb.StructuredQuery_FindNearest) (*FindNearest, error) {
	if fn.GetVectorField().GetFieldPath() == "" {
		return nil, errNearestField
	}
	p, err := fieldPathOf(fn.GetVectorField())
	if err != nil {
		return nil, err
	}
	if fn.GetQueryVector() == nil {
		return nil, errNearestVector
	}
	v, err := wire.ValueFromProto(fn.GetQueryVector())
	if err != nil {
		return nil, err
	}
	if !v.IsVector() {
		return nil, errNearestVector
	}
	if err := value.ValidateOperand(v); err != nil {
		return nil, wire.InvalidArgument(err.Error())
	}
	vs, _ := v.VectorValues()
	n := len(vs)
	if n > maxVectorDimension {
		return nil, errNearestVector
	}
	if fn.GetDistanceMeasure() == pb.StructuredQuery_FindNearest_DISTANCE_MEASURE_UNSPECIFIED {
		return nil, errNearestMeasure
	}
	if l := fn.GetLimit(); l == nil || l.GetValue() < 1 || l.GetValue() > maxNearestLimit {
		return nil, errNearestLimit
	}
	return &FindNearest{Field: p, Dimension: n}, nil
}

// VectorIndex is a declared vector index as a find_nearest needs it: its collection group and
// scope, the ascending fields before its vector field, and the vector field's dimension.
type VectorIndex = index.VectorIndex

// NearestError is what a find_nearest query is answered with, given the database's vector
// indexes: with one that matches (the query's collection and scope, its equality fields as the
// prefix in order, its vector field and dimension), UNIMPLEMENTED; without, production's
// FAILED_PRECONDITION and gcloud command, byte for byte (f0-find-nearest.json, REST and gRPC
// alike: rest.populated, rest.empty, rest.cosine_threshold_field, rest.prefilter,
// grpc.populated). The verdict is the shape's alone: neither the measure nor the threshold nor
// the data change it. The collection-group form is not recorded (provisional).
func (q *Query) NearestError(project string, vectors []VectorIndex) error {
	prefix := q.equalityFields()
	scope := index.ScopeCollection
	if q.AllDescendants {
		scope = index.ScopeCollectionGroup
	}
	field := q.FindNearest.Field.String()
	if slices.ContainsFunc(vectors, func(ix VectorIndex) bool {
		return ix.CollectionGroup == q.CollectionID && ix.Scope == scope && ix.Field == field &&
			ix.Dimension == q.FindNearest.Dimension && slices.Equal(ix.Prefix, prefix)
	}) {
		return errNearestServed
	}
	var b strings.Builder
	b.WriteString("Missing vector index configuration. Please create the required index with the following gcloud command: ")
	b.WriteString("gcloud firestore indexes composite create --project=")
	b.WriteString(project)
	b.WriteString(" --collection-group=")
	b.WriteString(q.CollectionID)
	b.WriteString(" --query-scope=")
	b.WriteString(scope.String())
	for _, k := range prefix {
		b.WriteString(" --field-config=order=ASCENDING,field-path=")
		b.WriteString(k)
	}
	b.WriteString(` --field-config=vector-config='{"dimension":"`)
	b.WriteString(strconv.Itoa(q.FindNearest.Dimension))
	b.WriteString(`","flat": "{}"}',field-path=`)
	b.WriteString(field)
	return wire.NewError(wire.CodeFailedPrecondition, b.String())
}

// equalityFields are the fields of the filter's top-level equalities, in the query's order:
// the prefix a vector index needs before its vector field (rest.prefilter).
func (q *Query) equalityFields() []string {
	if q.Where == nil {
		return nil
	}
	leaves := []*Filter{q.Where}
	if q.Where.Op == index.OpAnd {
		leaves = leaves[:0]
		for i := range q.Where.Filters {
			leaves = append(leaves, &q.Where.Filters[i])
		}
	}
	var out []string
	for _, l := range leaves {
		if l.Op == index.OpEqual && !slices.Contains(out, l.Path.String()) {
			out = append(out, l.Path.String())
		}
	}
	return out
}
