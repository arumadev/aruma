package core

import (
	"context"
	"time"
)

// The security rules' hold on a commit (F5, ADR-021 §3). The service layer judges a commit's
// writes by the documents they change, as they were and as the commit would leave them; core
// knows nothing of rules and asks the Authorizer the context carries (WithAuthorizer).
//
// The judgement runs once the commit holds the lock manager's exclusive locks on the documents
// it writes, before it is marked committing: no other commit can change them until it ends, so
// the documents judged are the ones the commit applies to; the sequencer checks it still
// (checkJudged). No Postgres connection is held meanwhile, so the rules' own reads take one
// from the pool like any read. A wound or a retry of the locks judges again.

// Proposal is one document a commit writes, as the rules judge it: Before as stored, After as
// the commit's writes leave it, folded without their preconditions (the rules decide before
// whether the document exists does). Exists is false on either side for no document. A document
// the commit only verifies is not proposed.
type Proposal struct {
	Path   Path
	Before Document
	After  Document
}

// Authorizer judges a commit's proposals, in lock order; now is the request time the writes'
// REQUEST_TIME transforms were given. A non-nil error fails the commit as it is, before
// anything is written.
type Authorizer func(ctx context.Context, proposals []Proposal, now time.Time) error

type authorizerKey struct{}

// WithAuthorizer returns a context whose commits a judges (Commit and CommitTransaction).
func WithAuthorizer(ctx context.Context, a Authorizer) context.Context {
	return context.WithValue(ctx, authorizerKey{}, a)
}

func authorizerOf(ctx context.Context) Authorizer {
	a, _ := ctx.Value(authorizerKey{}).(Authorizer)
	return a
}

// authorize judges p's writes when ctx carries an Authorizer; its callers hold the commit's
// write locks. It reads the documents written at the latest commit, folds the writes over them
// in preview, and asks the Authorizer; the sequencer then checks that the documents it locks
// are those judged (checkJudged).
func (s *PGStore) authorize(ctx context.Context, db DB, p *plan) error {
	judge := authorizerOf(ctx)
	if judge == nil {
		return nil
	}
	paths := make([]Path, len(p.docs))
	for i, d := range p.docs {
		paths[i] = d.path
	}
	docs, _, err := s.BatchGet(ctx, db, paths, ReadOptions{})
	if err != nil {
		return err
	}
	locked := make(map[string]*lockedDoc, len(docs))
	judged := make(map[string]time.Time, len(docs))
	for i, d := range docs {
		key := string(p.docs[i].keys.stored)
		judged[key] = d.UpdateTime
		if d.Exists() {
			locked[key] = &lockedDoc{fields: d.Fields, createTime: d.CreateTime, updateTime: d.UpdateTime}
		}
	}
	// REQUEST_TIME's value (RequestTime): request.time == request.resource.data.ts holds of a
	// server timestamp, as production's rules guides write it.
	now := time.Now().UTC().Truncate(time.Millisecond)
	p.preview = true
	err = p.apply(db, locked, now)
	p.preview = false
	if err != nil {
		return err
	}
	proposals := make([]Proposal, 0, len(p.docs))
	for _, d := range p.docs {
		if !d.mutates {
			continue
		}
		pr := Proposal{Path: d.path, Before: Document{Path: d.path}, After: Document{Path: d.path}}
		if d.before != nil {
			pr.Before.Fields, pr.Before.CreateTime, pr.Before.UpdateTime = d.before.fields, d.before.createTime, d.before.updateTime
		}
		if d.exists {
			pr.After.Fields, pr.After.CreateTime, pr.After.UpdateTime = d.fields, now, now
			if d.before != nil {
				pr.After.CreateTime = d.before.createTime
			}
		}
		proposals = append(proposals, pr)
	}
	if err := judge(ctx, proposals, now); err != nil {
		return err
	}
	p.judged = judged
	return nil
}

// checkJudged checks that the documents a commit locked are the ones its writes were judged
// on: only a write outside the lock manager (none today) could change one between the two, and
// the commit is then ABORTED with the contention text, which the clients retry.
func (p *plan) checkJudged(locked map[string]*lockedDoc) error {
	for k, t := range p.judged {
		var now time.Time
		if row := locked[k]; row != nil {
			now = row.updateTime
		}
		if !now.Equal(t) {
			return ErrContention
		}
	}
	return nil
}
