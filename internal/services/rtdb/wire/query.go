package wire

import (
	"encoding/json"
	"fmt"
)

// Query is the query object of q, n and g (QueryParams.ts:35-47,386-420; Android and iOS use
// the same keys). Values stay raw JSON: they are data, compared under the data model's order.
// A default query (no bounds, no limit, priority order) is never sent with q, and is sent as
// {} with g.
type Query struct {
	StartValue     json.RawMessage `json:"sp,omitempty"`  // startAt / startAfter value
	StartName      *string         `json:"sn,omitempty"`  // startAt key; absent means the first key
	StartInclusive *bool           `json:"sin,omitempty"` // false for startAfter
	EndValue       json.RawMessage `json:"ep,omitempty"`  // endAt / endBefore value
	EndName        *string         `json:"en,omitempty"`  // endAt key; absent means the last key
	EndInclusive   *bool           `json:"ein,omitempty"` // false for endBefore
	Limit          *int64          `json:"l,omitempty"`
	ViewFrom       string          `json:"vf,omitempty"` // "l" limitToFirst, "r" limitToLast
	Index          string          `json:"i,omitempty"`  // absent: priority; ".key", ".value", or a child path
}

// Index names of the query object.
const (
	IndexPriority = ".priority"
	IndexKey      = ".key"
	IndexValue    = ".value"
)

// ViewFrom values.
const (
	ViewFromLeft  = "l"
	ViewFromRight = "r"
)

// OrderBy returns the index the query orders by: IndexPriority when none is named.
func (q *Query) OrderBy() string {
	if q == nil || q.Index == "" {
		return IndexPriority
	}
	return q.Index
}

// HasStart reports whether the query has a lower bound.
func (q *Query) HasStart() bool { return q != nil && (q.StartValue != nil || q.StartName != nil) }

// HasEnd reports whether the query has an upper bound.
func (q *Query) HasEnd() bool { return q != nil && (q.EndValue != nil || q.EndName != nil) }

// IsDefault reports whether the query selects the whole node in priority order, the query
// the SDKs leave out of q.
func (q *Query) IsDefault() bool {
	return q == nil || (!q.HasStart() && !q.HasEnd() && q.Limit == nil && q.OrderBy() == IndexPriority)
}

// Validate checks what the SDKs would never send.
func (q *Query) Validate() error {
	if q == nil {
		return nil
	}
	if q.Limit != nil && *q.Limit <= 0 {
		return fmt.Errorf("%w: limit must be positive", ErrMalformed)
	}
	switch q.ViewFrom {
	case "", ViewFromLeft, ViewFromRight:
	default:
		return fmt.Errorf("%w: vf must be l or r", ErrMalformed)
	}
	if q.ViewFrom != "" && q.Limit == nil {
		return fmt.Errorf("%w: vf without a limit", ErrMalformed)
	}
	return nil
}
