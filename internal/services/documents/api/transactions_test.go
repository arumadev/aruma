package api

import (
	"encoding/base64"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// Core's transaction errors carry wire's texts, which are production's.
func TestTransactionTexts(t *testing.T) {
	for _, c := range []struct {
		core *core.Error
		wire *wire.Error
	}{
		{core.ErrInvalidTransaction, wire.InvalidTransaction()},
		{core.ErrTransactionExpired, wire.TransactionExpired()},
		{core.ErrWounded, wire.NewError(wire.CodeAborted, wire.MsgContentionWounded)},
		{core.ErrContention, wire.NewError(wire.CodeAborted, wire.MsgContentionTimeout)},
	} {
		if got := wire.Code(c.core.Code); got != c.wire.Code || c.core.Message != c.wire.Message {
			t.Errorf("core %v %q, wire %v %q", got, c.core.Message, c.wire.Code, c.wire.Message)
		}
	}
}

func TestPrincipalOf(t *testing.T) {
	account := uuid.New()
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user-1","iss":"x"}`))
	for _, c := range []struct {
		caller *authn.Caller
		want   core.Principal
	}{
		{&authn.Caller{Role: authn.RoleAdmin, IsDevOwner: true}, core.Principal{Admin: true}},
		{&authn.Caller{Role: authn.RoleAdmin, Principal: &authn.Principal{Kind: authn.KindServiceAccount, Email: "sa@p.iam"}},
			core.Principal{Admin: true, ID: "sa@p.iam"}},
		{&authn.Caller{Role: authn.RoleAdmin, Principal: &authn.Principal{Kind: authn.KindAccount, AccountID: account}},
			core.Principal{Admin: true, ID: account.String()}},
		{&authn.Caller{Role: authn.RoleEndUser, UnverifiedIDToken: "e30." + payload + ".c2ln"}, core.Principal{ID: "user-1"}},
		{&authn.Caller{Role: authn.RoleAnonymous}, core.Principal{}},
	} {
		if got := principalOf(c.caller); got != c.want {
			t.Errorf("%+v: %+v, want %+v", c.caller, got, c.want)
		}
	}
}

func TestTransactionOptions(t *testing.T) {
	rt := timestamppb.Now()
	rt.Nanos -= rt.Nanos % 1000
	for _, c := range []struct {
		in   *pb.TransactionOptions
		want core.TransactionOptions
	}{
		{nil, core.TransactionOptions{}},
		{&pb.TransactionOptions{}, core.TransactionOptions{}},
		{&pb.TransactionOptions{Mode: &pb.TransactionOptions_ReadOnly_{ReadOnly: &pb.TransactionOptions_ReadOnly{}}},
			core.TransactionOptions{ReadOnly: true}},
		{&pb.TransactionOptions{Mode: &pb.TransactionOptions_ReadOnly_{ReadOnly: &pb.TransactionOptions_ReadOnly{
			ConsistencySelector: &pb.TransactionOptions_ReadOnly_ReadTime{ReadTime: rt}}}},
			core.TransactionOptions{ReadOnly: true, ReadTime: rt.AsTime()}},
		{&pb.TransactionOptions{Mode: &pb.TransactionOptions_ReadWrite_{ReadWrite: &pb.TransactionOptions_ReadWrite{
			ConcurrencyMode: pb.TransactionOptions_OPTIMISTIC}}}, core.TransactionOptions{Concurrency: core.ConcurrencyOptimistic}},
		{&pb.TransactionOptions{Mode: &pb.TransactionOptions_ReadWrite_{ReadWrite: &pb.TransactionOptions_ReadWrite{
			ConcurrencyMode: pb.TransactionOptions_PESSIMISTIC}}}, core.TransactionOptions{Concurrency: core.ConcurrencyPessimistic}},
	} {
		got, err := transactionOptions(c.in)
		if err != nil || got.ReadOnly != c.want.ReadOnly || !got.ReadTime.Equal(c.want.ReadTime) ||
			got.Concurrency != c.want.Concurrency || got.Retry != nil {
			t.Errorf("%v: %+v %v, want %+v", c.in, got, err, c.want)
		}
	}
	retry := []byte("attempt")
	got, err := transactionOptions(&pb.TransactionOptions{Mode: &pb.TransactionOptions_ReadWrite_{ReadWrite: &pb.TransactionOptions_ReadWrite{RetryTransaction: retry}}})
	if err != nil || string(got.Retry) != "attempt" {
		t.Errorf("retry_transaction: %+v %v", got, err)
	}
}
