package query

import (
	"slices"
	"strconv"

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
	UpTo  int64           // count: at most this many; 0 for no bound
	Alias string
}

// maxAggregations is production's bound: "A minimum of one and maximum of five aggregations per
// query" (query.proto).
const maxAggregations = 5

// The aggregations' errors, provisional.
var (
	errAggregationCount = wire.InvalidArgument("A query must have between 1 and 5 aggregations.")
	errAggregationAlias = wire.InvalidArgument("Aggregation aliases must be unique.")
	errAggregationOp    = wire.InvalidArgument("An aggregation must be count, sum or avg.")
	errAggregationUpTo  = wire.InvalidArgument("count up_to must be greater than zero.")
	errAggregationField = wire.InvalidArgument("An aggregation's field must be set.")
)

// AggregationsOf reads a StructuredAggregationQuery's aggregations. An alias left empty is
// field_1, field_2, … in the order of the aggregations without one (query.proto,
// Aggregation.alias).
func AggregationsOf(as []*pb.StructuredAggregationQuery_Aggregation) ([]Aggregation, error) {
	if len(as) == 0 || len(as) > maxAggregations {
		return nil, errAggregationCount
	}
	out := make([]Aggregation, 0, len(as))
	unnamed := 0
	for _, a := range as {
		agg := Aggregation{Alias: a.GetAlias()}
		if agg.Alias == "" {
			unnamed++
			agg.Alias = "field_" + strconv.Itoa(unnamed)
		}
		var ref *pb.StructuredQuery_FieldReference
		switch op := a.GetOperator().(type) {
		case *pb.StructuredAggregationQuery_Aggregation_Count_:
			agg.Op = index.AggCount
			if u := op.Count.GetUpTo(); u != nil {
				if u.GetValue() < 1 {
					return nil, errAggregationUpTo
				}
				agg.UpTo = u.GetValue()
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
				return nil, errAggregationField
			}
			p, err := fieldPathOf(ref)
			if err != nil {
				return nil, err
			}
			agg.Field = p
		}
		if slices.ContainsFunc(out, func(o Aggregation) bool { return o.Alias == agg.Alias }) {
			return nil, errAggregationAlias
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
type Aggregator struct {
	aggs []Aggregation
	acc  []accumulator
}

type accumulator struct {
	n       int64 // the documents counted, or the numbers summed
	ints    bool  // every number summed so far is an integer, and their sum fits
	intSum  int64
	realSum float64
}

// NewAggregator returns an empty aggregator for aggs.
func NewAggregator(aggs []Aggregation) *Aggregator {
	a := &Aggregator{aggs: aggs, acc: make([]accumulator, len(aggs))}
	for i := range a.acc {
		a.acc[i].ints = true
	}
	return a
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
			if agg.UpTo == 0 || acc.n < agg.UpTo {
				acc.n++
			}
			done = done && agg.UpTo > 0 && acc.n >= agg.UpTo
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
		acc.n++
		x := v.IntValue()
		if acc.ints {
			if s := acc.intSum + x; (x >= 0) == (s >= acc.intSum) {
				acc.intSum = s
				return
			}
			// The integer sum overflows: from here it is a double.
			acc.ints = false
			acc.realSum = float64(acc.intSum)
		}
		acc.realSum += float64(x)
	case value.KindDouble:
		acc.n++
		if acc.ints {
			acc.ints = false
			acc.realSum = float64(acc.intSum)
		}
		acc.realSum += v.DoubleValue()
	}
}

// Result is the aggregations' values by alias: count an integer; sum an integer unless a double
// was summed or the integers overflowed, 0 for none; avg a double, null for none.
func (a *Aggregator) Result() map[string]value.Value {
	out := make(map[string]value.Value, len(a.aggs))
	for i, agg := range a.aggs {
		acc := a.acc[i]
		switch agg.Op {
		case index.AggCount:
			out[agg.Alias] = value.Int(acc.n)
		case index.AggSum:
			if acc.ints {
				out[agg.Alias] = value.Int(acc.intSum)
			} else {
				out[agg.Alias] = value.Double(acc.realSum)
			}
		case index.AggAvg:
			switch {
			case acc.n == 0:
				out[agg.Alias] = value.Null()
			case acc.ints:
				out[agg.Alias] = value.Double(float64(acc.intSum) / float64(acc.n))
			default:
				out[agg.Alias] = value.Double(acc.realSum / float64(acc.n))
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
