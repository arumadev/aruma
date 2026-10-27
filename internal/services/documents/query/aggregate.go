package query

import (
	"math"
	"math/big"
	"slices"
	"strconv"

	"google.golang.org/genproto/googleapis/rpc/errdetails"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// Aggregation is one aggregation of a StructuredAggregationQuery (ADR-025 §5): count (with
// up_to), sum or avg of a field, and the alias its result goes under.
type Aggregation struct {
	Op    index.AggregationOp
	Field value.FieldPath // sum and avg
	// UpTo bounds a count when Bounded: at most this many, 0 included (f3-aggregations.json
	// count.up_to_0).
	UpTo    int64
	Bounded bool
	Alias   string
}

// maxAggregations is production's bound: "A minimum of one and maximum of five aggregations per
// query" (query.proto).
const maxAggregations = 5

// maxAliasBytes bounds an alias as a field name (alias.long_1500, alias.long_1501).
const maxAliasBytes = 1500

// The aggregations' errors, production's texts (f3-aggregations.json).
var (
	errNoAggregationQuery = wire.InvalidArgument("Only structured aggregation queries are supported.")
	errNoAggregations     = wire.InvalidArgument("Aggregations can not be empty.")
	errAggregationOp      = wire.InvalidArgument("Operator field in Aggregation is not set.")
	errAggregationUpTo    = wire.InvalidArgument("The `up_to` value in a COUNT aggregation must be greater than or equal to zero.")
	errAggregationName    = wire.InvalidArgument("Aggregations are not supported for the property: __key__")
	errAggregationNoPath  = wire.InvalidArgument("Invalid empty property path string.")
	errAliasLong          = wire.InvalidArgument("The property.name is longer than 1500 bytes.")
)

// errTooManyAggregations is multi.six's answer, with its google.rpc.ErrorInfo.
func errTooManyAggregations(n int) error {
	return wire.InvalidArgument("The maximum number of aggregations allowed in an aggregation query is " +
		strconv.Itoa(maxAggregations) + ". Received: " + strconv.Itoa(n)).WithDetails(&errdetails.ErrorInfo{
		Reason: "TOO_MANY_AGGREGATIONS",
		Domain: "firestore.googleapis.com",
		Metadata: map[string]string{
			"actual_aggregations": strconv.Itoa(n),
			"max_aggregations":    strconv.Itoa(maxAggregations),
		},
	})
}

func errAliasDuplicate(alias string) error {
	return wire.InvalidArgument("Aggregation aliases contain duplicate alias: " + alias + ".")
}

func errAliasReserved(alias string) error {
	return wire.InvalidArgument(`The property.name "` + alias + `" is reserved.`)
}

// AggregationQuery is the structured query of an aggregation query, its aggregations read with
// AggregationsOf. Production counts the whole database for a request with aggregations and no
// structured query (request.no_query): a kindless query over every descendant of the parent.
func AggregationQuery(saq *pb.StructuredAggregationQuery) (*pb.StructuredQuery, error) {
	if saq == nil {
		return nil, errNoAggregationQuery
	}
	if sq := saq.GetStructuredQuery(); sq != nil {
		return sq, nil
	}
	return &pb.StructuredQuery{From: []*pb.StructuredQuery_CollectionSelector{{AllDescendants: true}}}, nil
}

// AggregationsOf reads a StructuredAggregationQuery's aggregations. An alias left empty is
// field_1, field_2, … in the order of the aggregations without one (query.proto,
// Aggregation.alias), so an explicit field_1 beside an unnamed aggregation is a duplicate
// (alias.absent_with_field_1).
func AggregationsOf(as []*pb.StructuredAggregationQuery_Aggregation) ([]Aggregation, error) {
	switch {
	case len(as) == 0:
		return nil, errNoAggregations
	case len(as) > maxAggregations:
		return nil, errTooManyAggregations(len(as))
	}
	out := make([]Aggregation, 0, len(as))
	unnamed := 0
	for _, a := range as {
		agg := Aggregation{Alias: a.GetAlias()}
		switch {
		case agg.Alias == "":
			unnamed++
			agg.Alias = "field_" + strconv.Itoa(unnamed)
		case reservedName(agg.Alias):
			return nil, errAliasReserved(agg.Alias)
		case len(agg.Alias) > maxAliasBytes:
			return nil, errAliasLong
		}
		var ref *pb.StructuredQuery_FieldReference
		switch op := a.GetOperator().(type) {
		case *pb.StructuredAggregationQuery_Aggregation_Count_:
			agg.Op = index.AggCount
			if u := op.Count.GetUpTo(); u != nil {
				if u.GetValue() < 0 {
					return nil, errAggregationUpTo
				}
				agg.UpTo, agg.Bounded = u.GetValue(), true
			}
		case *pb.StructuredAggregationQuery_Aggregation_Sum_:
			agg.Op, ref = index.AggSum, op.Sum.GetField()
		case *pb.StructuredAggregationQuery_Aggregation_Avg_:
			agg.Op, ref = index.AggAvg, op.Avg.GetField()
		default:
			return nil, errAggregationOp
		}
		if agg.Op != index.AggCount {
			if ref.GetFieldPath() == "" {
				return nil, errAggregationNoPath
			}
			p, err := fieldPathOf(ref)
			if err != nil {
				return nil, err
			}
			if p.IsDocumentID() {
				return nil, errAggregationName
			}
			agg.Field = p
		}
		if slices.ContainsFunc(out, func(o Aggregation) bool { return o.Alias == agg.Alias }) {
			return nil, errAliasDuplicate(agg.Alias)
		}
		out = append(out, agg)
	}
	return out, nil
}

// Shapes are the aggregations as production's index rule reads them (R13).
func Shapes(aggs []Aggregation) []index.Aggregation {
	out := make([]index.Aggregation, len(aggs))
	for i, a := range aggs {
		out[i] = index.Aggregation{Op: a.Op}
		if a.Op != index.AggCount {
			out[i].Field = a.Field.String()
		}
	}
	return out
}

// Aggregator accumulates a query's aggregations over its results, as production computes them
// (ADR-025 §5; OF §2b): count counts every result, to up_to at most; sum and avg read numbers
// alone, ignoring any other value; and a result without every summed or averaged field takes no
// part in any aggregation ("Aggregating several fields only includes documents that contain all
// those fields").
//
// A sum is exact whatever the order the results arrive in, so the index paths and the
// reference path, which read them in different orders, agree: the finite numbers are added
// without rounding, and the total is rounded once at the end (f3-aggregations.json set.dbl:
// 0.1 + 0.2 + 0.3 is 0.6, where adding in turn gives 0.6000000000000001). Integers alone stay an
// integer when the exact total fits in 64 bits, whatever the partial sums did (set.ovf-back:
// MAX + 1 - 2 is the integer 9223372036854775806), and are a double when it does not (set.ovf,
// set.negovf). A NaN, or both infinities, make NaN (set.nan, set.inf); one infinity is that
// infinity. avg is the rounded sum divided by the count (set.dbl: 0.6 / 3 is
// 0.19999999999999998; set.big: (2^53 + 2) / 2 is 4503599627370497).
type Aggregator struct {
	aggs []Aggregation
	acc  []accumulator
}

// sumPrecision holds any sum of int64s and finite doubles exactly: a double's bits span 2^-1074
// to 2^1023, and 64 more bits leave room for 2^64 terms.
const sumPrecision = 1074 + 1024 + 64

type accumulator struct {
	n       int64 // the documents counted, or the numbers summed
	doubles bool  // a double was summed
	nan     bool
	posInf  bool
	negInf  bool
	sum     *big.Float // the exact sum of the finite numbers
	term    big.Float
}

// NewAggregator returns an empty aggregator for aggs.
func NewAggregator(aggs []Aggregation) *Aggregator {
	return &Aggregator{aggs: aggs, acc: make([]accumulator, len(aggs))}
}

// Add accumulates one result, and reports whether nothing later can change the outcome: every
// aggregation is a count that reached its up_to.
func (a *Aggregator) Add(d *Doc) (done bool) {
	vals := make([]value.Value, len(a.aggs))
	for i, agg := range a.aggs {
		if agg.Op == index.AggCount {
			continue
		}
		v, ok := d.field(agg.Field)
		if !ok {
			return false
		}
		vals[i] = v
	}
	done = true
	for i, agg := range a.aggs {
		acc := &a.acc[i]
		switch agg.Op {
		case index.AggCount:
			if !agg.Bounded || acc.n < agg.UpTo {
				acc.n++
			}
			done = done && agg.Bounded && acc.n >= agg.UpTo
		default:
			done = false
			acc.add(vals[i])
		}
	}
	return done
}

func (acc *accumulator) add(v value.Value) {
	switch v.Kind() {
	case value.KindInteger:
		acc.term.SetPrec(sumPrecision).SetInt64(v.IntValue())
	case value.KindDouble:
		acc.doubles = true
		x := v.DoubleValue()
		switch {
		case math.IsNaN(x):
			acc.n++
			acc.nan = true
			return
		case math.IsInf(x, 1):
			acc.n++
			acc.posInf = true
			return
		case math.IsInf(x, -1):
			acc.n++
			acc.negInf = true
			return
		}
		acc.term.SetPrec(sumPrecision).SetFloat64(x)
	default:
		return
	}
	acc.n++
	if acc.sum == nil {
		acc.sum = new(big.Float).SetPrec(sumPrecision)
	}
	acc.sum.Add(acc.sum, &acc.term)
}

// total is the sum as a double, rounded once, with the NaN and infinity rules.
func (acc *accumulator) total() float64 {
	switch {
	case acc.nan || acc.posInf && acc.negInf:
		return math.NaN()
	case acc.posInf:
		return math.Inf(1)
	case acc.negInf:
		return math.Inf(-1)
	case acc.sum == nil:
		return 0
	}
	f, _ := acc.sum.Float64()
	return f
}

// Result is the aggregations' values by alias: count an integer; sum an integer when only
// integers were summed and their sum fits, 0 for none, else a double; avg a double, null for
// none.
func (a *Aggregator) Result() map[string]value.Value {
	out := make(map[string]value.Value, len(a.aggs))
	for i, agg := range a.aggs {
		acc := &a.acc[i]
		switch agg.Op {
		case index.AggCount:
			out[agg.Alias] = value.Int(acc.n)
		case index.AggSum:
			out[agg.Alias] = value.Double(acc.total())
			if !acc.doubles {
				if acc.sum == nil {
					out[agg.Alias] = value.Int(0)
				} else if n, exact := acc.sum.Int64(); exact == big.Exact {
					out[agg.Alias] = value.Int(n)
				}
			}
		case index.AggAvg:
			if acc.n == 0 {
				out[agg.Alias] = value.Null()
			} else {
				out[agg.Alias] = value.Double(acc.total() / float64(acc.n))
			}
		}
	}
	return out
}

// ResultProto is Result as an AggregationResult.
func (a *Aggregator) ResultProto() *pb.AggregationResult {
	r := a.Result()
	out := &pb.AggregationResult{AggregateFields: make(map[string]*pb.Value, len(r))}
	for k, v := range r {
		out.AggregateFields[k] = wire.ValueToProto(v)
	}
	return out
}
