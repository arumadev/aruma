package api

import (
	"cmp"
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// The transactions (ADR-025 §7): BeginTransaction and Rollback here; a read's transaction or
// new_transaction in readOptions and newTransaction; Commit's transaction in writes.go. Core
// checks every id against the database and the principal (core.Transactions), and its errors
// carry production's texts (wire.InvalidTransaction, wire.TransactionExpired,
// wire.MsgContentionWounded, wire.MsgContentionTimeout).
//
// RunQuery (F3) takes its transaction as GetDocument does (readOptions) and its new_transaction
// as BatchGetDocuments does (newTransaction): the id goes in its first response, and the
// transaction is rolled back if the query fails (production's rest.txn_unknown.runQuery
// answers "Invalid transaction." in an array, f0-errors.json).

// principalOf is the caller as transactions know it (ADR-025 §7.1): an admin (a service account,
// the project's owner, the dev owner) or an end user (an ID token, or none), named stably across
// its requests. An end user's uid is its ID token's sub, unverified until the rules verify the
// token (F5): before F5 only documents.dev_open_rules admits an end user at all.
func principalOf(c *authn.Caller) core.Principal {
	switch {
	case c.Role == authn.RoleAdmin && c.Principal == nil: // the dev owner
		return core.Principal{Admin: true}
	case c.Role == authn.RoleAdmin && c.Principal.Kind == authn.KindServiceAccount:
		return core.Principal{Admin: true, ID: cmp.Or(c.Principal.Email, c.Principal.ServiceAccountID.String())}
	case c.Role == authn.RoleAdmin:
		return core.Principal{Admin: true, ID: c.Principal.AccountID.String()}
	}
	return core.Principal{ID: c.UnverifiedSubject()}
}

// transaction names a transaction the request's caller uses.
func (a admitted) transaction(id []byte) *core.TransactionRef {
	return &core.TransactionRef{ID: id, By: a.by}
}

// transactionOptions reads BeginTransaction's options and a read's new_transaction: read-only,
// at a read_time checked as a read's; or read-write (the default, options or no), with its
// retry_transaction and concurrency_mode.
func transactionOptions(o *pb.TransactionOptions) (core.TransactionOptions, error) {
	switch m := o.GetMode().(type) {
	case *pb.TransactionOptions_ReadOnly_:
		out := core.TransactionOptions{ReadOnly: true}
		if rt := m.ReadOnly.GetReadTime(); rt != nil {
			if err := wire.CheckReadTime(rt); err != nil {
				return core.TransactionOptions{}, err
			}
			out.ReadTime = rt.AsTime()
		}
		return out, nil
	case *pb.TransactionOptions_ReadWrite_:
		out := core.TransactionOptions{}
		if r := m.ReadWrite.GetRetryTransaction(); len(r) > 0 {
			out.Retry = r
		}
		switch m.ReadWrite.GetConcurrencyMode() {
		case pb.TransactionOptions_OPTIMISTIC:
			out.Concurrency = core.ConcurrencyOptimistic
		case pb.TransactionOptions_PESSIMISTIC:
			out.Concurrency = core.ConcurrencyPessimistic
		}
		return out, nil
	}
	return core.TransactionOptions{}, nil
}

// BeginTransaction begins a transaction and answers its id (f1-get.json rest.txn.begin_*,
// f0-errors.json txn_reused.begin).
func (s *Service) BeginTransaction(ctx context.Context, c Call, req *pb.BeginTransactionRequest) (*pb.BeginTransactionResponse, error) {
	a, err := s.admit(ctx, c, req, databaseOf(req.GetDatabase()))
	if err != nil {
		return nil, err
	}
	o, err := transactionOptions(req.GetOptions())
	if err != nil {
		return nil, err
	}
	id, err := s.o.Store.BeginTransaction(ctx, a.db, a.by, o)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return &pb.BeginTransactionResponse{Transaction: id}, nil
}

// Rollback ends a transaction; one that is already over is no error (f1-get.json
// rest.txn.read_only_rollback; f0-errors.json rest.txn_unknown.rollback for a forged id).
func (s *Service) Rollback(ctx context.Context, c Call, req *pb.RollbackRequest) (*emptypb.Empty, error) {
	a, err := s.admit(ctx, c, req, databaseOf(req.GetDatabase()))
	if err != nil {
		return nil, err
	}
	if err := s.o.Store.Rollback(ctx, a.db, *a.transaction(req.GetTransaction())); err != nil {
		return nil, s.fail(ctx, err)
	}
	return &emptypb.Empty{}, nil
}

// newTransaction begins the transaction a read's new_transaction asks for. The read then runs in
// it, and its first response carries the id (ADR-025 §7.5; f1-batch-get.json new_transaction.*);
// end must follow with the read's outcome: a failed read rolls the transaction back, so that no
// transaction the client never learned of holds locks.
func (s *Service) newTransaction(ctx context.Context, a admitted, o *pb.TransactionOptions) (id []byte, end func(error), err error) {
	opts, err := transactionOptions(o)
	if err != nil {
		return nil, nil, err
	}
	id, err = s.o.Store.BeginTransaction(ctx, a.db, a.by, opts)
	if err != nil {
		return nil, nil, s.fail(ctx, err)
	}
	return id, func(err error) {
		if err == nil {
			return
		}
		if err := s.o.Store.Rollback(context.WithoutCancel(ctx), a.db, *a.transaction(id)); err != nil {
			s.log.Warn("documents: rolling back a failed read's new transaction", "error", err)
		}
	}, nil
}
