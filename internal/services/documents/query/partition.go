package query

import (
	"errors"

	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// PartitionQuery's checks (firestore.proto, PartitionQueryRequest.structured_query): a
// collection group query, ordered by __name__ ascending alone, with no filter but on __name__,
// and no limit, offset, cursor or projection. The texts are production's where recorded
// (f0-descendants.json rest.partition.*, f3-partition-query.json invalid.*), else provisional.
var (
	errPartitionNoQuery   = wire.InvalidArgument("Query is required.")
	errPartitionNotGroup  = wire.InvalidArgument("Query must select all descendant collections.")
	errPartitionAncestor  = wire.InvalidArgument("Ancestor queries are not supported.")
	errPartitionLimit     = wire.InvalidArgument("Query limit is not supported.")
	errPartitionOffset    = wire.InvalidArgument("Query offset is not supported.")
	errPartitionStart     = wire.InvalidArgument("Query start cursors are not supported.")
	errPartitionEnd       = wire.InvalidArgument("Query end cursors are not supported.")
	errPartitionSelect    = wire.InvalidArgument("Property masks are not supported.")
	errPartitionCount     = wire.InvalidArgument("Partition count must be greater than zero.")
	errPartitionPageSize  = wire.InvalidArgument("Page size must be nonnegative.")
	errPartitionPageToken = wire.InvalidArgument("invalid page token")

	// Provisional texts.
	errPartitionKindless = wire.InvalidArgument("A partition query must select a collection group by its collection id.")
	errPartitionOrder    = wire.InvalidArgument("A partition query may only be ordered by __name__ ascending.")
	errPartitionFilter   = wire.InvalidArgument("A partition query cannot have filters.")
)

// PartitionShapeError reports an error of CheckPartition for an order or a filter that a
// partition query may not have, which production's index rule may answer first.
func PartitionShapeError(err error) bool {
	return errors.Is(err, errPartitionOrder) || errors.Is(err, errPartitionFilter)
}

// CheckPartition checks a PartitionQuery's query, partition count, page size and page token
// under a parent. An inequality filter meets the order rule first: __name__ ascending
// explicitly, then the filter's field (rest.partition.with_filter). A range on __name__ is
// accepted (invalid.filter_name). This server answers no partition (api.PartitionQuery), so
// sends no page token: any page token is not one of its own.
func CheckPartition(req *pb.PartitionQueryRequest, parent wire.Name) error {
	sq := req.GetStructuredQuery()
	if sq == nil {
		return errPartitionNoQuery
	}
	switch len(sq.GetFrom()) {
	case 0:
		return errPartitionNotGroup
	case 1:
	default:
		return errTwoFrom
	}
	from := sq.GetFrom()[0]
	switch {
	case !from.GetAllDescendants():
		return errPartitionNotGroup
	case !parent.IsRoot():
		return errPartitionAncestor
	case from.GetCollectionId() == "":
		return errPartitionKindless
	}
	if err := checkCollectionID(from.GetCollectionId()); err != nil {
		return err
	}
	var where *Filter
	if sq.GetWhere() != nil {
		f, empty, err := filterOf(sq.GetWhere(), parent.Database)
		if err != nil {
			return err
		}
		if !empty {
			where = &f
		}
	}
	explicit, err := ordersOf(sq.GetOrderBy())
	if err != nil {
		return err
	}
	order, err := normalizeOrder(explicit, where)
	if err != nil {
		return err
	}
	switch {
	case !byNameAsc(order):
		return errPartitionOrder
	case where != nil && hasFieldFilter(where):
		return errPartitionFilter
	case sq.GetLimit() != nil:
		return errPartitionLimit
	case sq.GetOffset() != 0:
		return errPartitionOffset
	case sq.GetStartAt() != nil:
		return errPartitionStart
	case sq.GetEndAt() != nil:
		return errPartitionEnd
	case sq.GetSelect() != nil:
		return errPartitionSelect
	case req.GetPartitionCount() < 1:
		return errPartitionCount
	case req.GetPageSize() < 0:
		return errPartitionPageSize
	case req.GetPageToken() != "":
		return errPartitionPageToken
	}
	return nil
}
