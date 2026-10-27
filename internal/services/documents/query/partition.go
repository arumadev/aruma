package query

import (
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// PartitionQuery's checks (firestore.proto, PartitionQueryRequest.structured_query): a
// collection group query, ordered by __name__ ascending alone, with no filter. The texts are
// production's where recorded (f0-descendants.json rest.partition.*), else provisional.
var (
	errPartitionNotGroup = wire.InvalidArgument("Query must select all descendant collections.")
	errPartitionAncestor = wire.InvalidArgument("Ancestor queries are not supported.")
	errPartitionKindless = wire.InvalidArgument("A partition query must select a collection group by its collection id.")
	errPartitionOrder    = wire.InvalidArgument("A partition query may only be ordered by __name__ ascending.")
	errPartitionFilter   = wire.InvalidArgument("A partition query cannot have filters.")
	errPartitionCount    = wire.InvalidArgument("partition_count must be positive.")
	errPartitionPageSize = wire.InvalidArgument("page_size must be non-negative.")
)

// CheckPartition checks a PartitionQuery's query and partition count under a parent. An
// inequality filter meets the order rule first: __name__ ascending explicitly, then the
// filter's field (rest.partition.with_filter).
func CheckPartition(req *pb.PartitionQueryRequest, parent wire.Name) error {
	sq := req.GetStructuredQuery()
	if sq == nil {
		return errNoQuery
	}
	if len(sq.GetFrom()) != 1 {
		return errNoFrom
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
	if err := wire.CheckID(from.GetCollectionId(), false); err != nil {
		return err
	}
	var where *Filter
	if sq.GetWhere() != nil {
		f, err := filterOf(sq.GetWhere())
		if err != nil {
			return err
		}
		where = &f
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
	case where != nil:
		return errPartitionFilter
	case req.GetPartitionCount() < 1:
		return errPartitionCount
	case req.GetPageSize() < 0:
		return errPartitionPageSize
	}
	return nil
}
