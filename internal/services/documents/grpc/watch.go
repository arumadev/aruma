package grpc

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/services/documents/api"
	"github.com/arumadev/aruma/internal/services/documents/wire"
)

// Errors that close an admitted stream, or refuse one at its first message.
var (
	// errTooManyProjectStreams refuses a stream past streams_per_project (ADR-026 §3); our text.
	errTooManyProjectStreams = wire.NewError(wire.CodeResourceExhausted,
		"Too many streams are open on this project.")
	// The texts of an end user's token refused (api/rules.go): errTokenExpired closes a stream at
	// its token's exp (ADR-026 §4); errTokenInvalid refuses or closes a stream whose token is no
	// longer valid (the user disabled or deleted, their tokens revoked); errTokenUnchecked
	// refuses one whose token could not be verified (an outage).
	errTokenExpired   = api.ErrTokenExpired
	errTokenInvalid   = api.ErrTokenInvalid
	errTokenUnchecked = api.ErrTokenUnchecked
	// errInstanceLost closes every stream when the single-instance lock is lost (ADR-026 §5),
	// and refuses new ones until it is taken again; core.ErrNotServing's text.
	errInstanceLost = wire.NewError(wire.CodeUnavailable, "The Document Database is not served by this Aruma instance: "+
		"another instance holds it on this Postgres database, or the connection to Postgres was lost. "+
		"The Document Database runs as a single instance.")
)

// streamWatch holds the admitted Listen and Write streams (ADR-026 §3-§5): their count per
// project against streams_per_project, their callers' tokens, and the single-instance lock.
//
// An end user's token is verified when the stream joins, and the stream closes at its exp. Its
// revocations follow rtdb/core/revoke.go: one idtoken.Revocations subscription per project with
// end users' streams; a hold for each join in progress, so that a revocation landing between the
// token's verification and the stream's registration is not lost; and a subscription that falls
// behind (idtoken.ErrOverflow) verifies every end user's token of the project again. An admin's
// credential is watched by api.Service.Supervise: its exp, and a recheck every minute.
type streamWatch struct {
	h        *Handler
	verifier idtoken.Verifier
	src      idtoken.Revocations

	mu sync.Mutex
	// lost is the single-instance lock's loss: no stream joins until it is taken again.
	lost     bool
	projects map[int64]*projectStreams
}

// projectStreams are one project's streams, and its revocations' subscription while it has end
// users' streams or joins.
type projectStreams struct {
	project int64
	streams map[*watched]struct{}
	users   map[*watched]struct{}
	sub     *idtoken.Subscription
	// holds are the end users' joins in progress, and log what the subscription delivered
	// while there were any, numbered by seq.
	holds int
	seq   uint64
	log   []logged
}

type logged struct {
	seq uint64
	r   idtoken.Revocation
	// lost marks an overflow: revocations went missing, which matches every token.
	lost bool
}

// watched is one admitted stream.
type watched struct {
	s       *serverStream
	project int64
	token   string          // an end user's ID token
	claims  *idtoken.Claims // its claims, verified; nil for any other caller
}

func (w *streamWatch) init(h *Handler, v idtoken.Verifier, src idtoken.Revocations) {
	w.h, w.verifier, w.src = h, v, src
	w.projects = map[int64]*projectStreams{}
}

// projectLocked returns the project's streams, made if needed. Callers hold w.mu.
func (w *streamWatch) projectLocked(project int64) *projectStreams {
	p := w.projects[project]
	if p == nil {
		p = &projectStreams{project: project, streams: map[*watched]struct{}{}, users: map[*watched]struct{}{}}
		w.projects[project] = p
	}
	return p
}

// tidyLocked ends the subscription of a project that no longer needs it, and forgets a project
// without streams. Callers hold w.mu.
func (w *streamWatch) tidyLocked(p *projectStreams) {
	if len(p.users) == 0 && p.holds == 0 && p.sub != nil {
		p.sub.Close()
		p.sub = nil
		p.log = nil
	}
	if len(p.streams) == 0 && p.holds == 0 && w.projects[p.project] == p {
		delete(w.projects, p.project)
	}
}

// join admits a stream to the watch: refused past streams_per_project, or while the lock is
// lost, or when its end user's token is not valid. release ends the watch of it.
func (w *streamWatch) join(s *serverStream, a api.Admission) (release func(), err error) {
	ws := &watched{s: s, project: a.Project}
	user := a.Caller != nil && a.Caller.Role == authn.RoleEndUser && w.verifier != nil
	w.mu.Lock()
	if w.lost {
		w.mu.Unlock()
		return nil, errInstanceLost
	}
	p := w.projectLocked(a.Project)
	if len(p.streams) >= w.h.limits.StreamsPerProject {
		w.tidyLocked(p)
		w.mu.Unlock()
		w.h.overflow(s, "streams_per_project", "project", a.Project)
		return nil, errTooManyProjectStreams
	}
	p.streams[ws] = struct{}{}
	var from uint64
	held := user && w.src != nil
	if held {
		w.subscribeLocked(p)
		p.holds++
		from = p.seq
	}
	w.mu.Unlock()

	ctx, stop := context.WithCancel(s.ctx)
	var jobs sync.WaitGroup
	release = sync.OnceFunc(func() {
		stop()
		jobs.Wait()
		w.mu.Lock()
		defer w.mu.Unlock()
		delete(p.streams, ws)
		delete(p.users, ws)
		w.tidyLocked(p)
	})
	unhold := func() {
		if !held {
			return
		}
		w.mu.Lock()
		if p.holds--; p.holds == 0 {
			p.log = nil
		}
		w.mu.Unlock()
	}

	switch {
	case user:
		claims, err := w.verifier.Verify(ctx, a.Project, a.Caller.UnverifiedIDToken)
		if err != nil {
			unhold()
			release()
			return nil, w.refusal(s, a.Project, err)
		}
		ws.token, ws.claims = a.Caller.UnverifiedIDToken, claims
		revoked := false
		w.mu.Lock()
		if held {
			p.users[ws] = struct{}{}
			for _, l := range p.log {
				if l.seq > from && (l.lost || l.r.Revokes(a.Project, claims)) {
					revoked = true
				}
			}
		}
		w.mu.Unlock()
		unhold()
		if revoked {
			release()
			w.h.log.Info("documents: refused a stream whose token was revoked as it opened", "project", a.Project)
			return nil, errTokenInvalid
		}
		expiry := time.AfterFunc(time.Until(claims.Expires), func() { w.close(ws, errTokenExpired, "token_expired") })
		jobs.Go(func() {
			<-ctx.Done()
			expiry.Stop()
		})
	case a.Caller != nil && a.Caller.Role == authn.RoleAdmin && !a.Caller.IsDevOwner:
		jobs.Go(func() {
			if err := w.h.svc.Supervise(ctx, a); err != nil && ctx.Err() == nil {
				if we, ok := errors.AsType[*wire.Error](err); ok {
					w.close(ws, we, "credential_lapsed")
				}
			}
		})
	}
	return release, nil
}

// refusal is the status of a join whose end user's token Verify refused, or could not check.
func (w *streamWatch) refusal(s *serverStream, project int64, err error) error {
	switch idtoken.Status(err) {
	case "":
		w.h.log.Warn("documents: verifying a stream's token", "project", project, "error", err, "client_ip", s.info.client.String())
		return errTokenUnchecked
	case "expired_token":
		return errTokenExpired
	}
	return errTokenInvalid
}

// close ends a watched stream with status err, counted and logged by reason.
func (w *streamWatch) close(ws *watched, err *wire.Error, reason string) {
	if ws.s.ctx.Err() != nil {
		return
	}
	w.h.metrics.closed(reason)
	w.h.log.Info("documents: closed a stream", "reason", reason, "project", ws.project, "client_ip", ws.s.info.client.String())
	ws.s.cancel(err)
}

// subscribeLocked starts the project's revocations if needed. Callers hold w.mu.
func (w *streamWatch) subscribeLocked(p *projectStreams) {
	if p.sub == nil {
		p.sub = w.src.Subscribe(p.project)
		go w.run(p, p.sub)
	}
}

// run delivers a project's revocations until its subscription is closed.
func (w *streamWatch) run(p *projectStreams, sub *idtoken.Subscription) {
	for {
		rev, ok := <-sub.C()
		if !ok {
			if sub.Err() == nil {
				return // closed: the project has no end user's stream
			}
			w.h.log.Warn("documents: revocations lost, verifying every stream's token again", "project", p.project, "error", sub.Err())
			w.mu.Lock()
			if p.sub != sub {
				w.mu.Unlock()
				return
			}
			p.sub = w.src.Subscribe(p.project)
			sub = p.sub
			w.recordLocked(p, logged{lost: true})
			users := slices.Collect(maps.Keys(p.users))
			w.mu.Unlock()
			w.reverify(p.project, users)
			continue
		}
		w.mu.Lock()
		w.recordLocked(p, logged{r: rev})
		users := slices.Collect(maps.Keys(p.users))
		w.mu.Unlock()
		for _, u := range users {
			if rev.Revokes(p.project, u.claims) {
				w.close(u, errTokenInvalid, "token_revoked")
			}
		}
	}
}

func (w *streamWatch) recordLocked(p *projectStreams, l logged) {
	p.seq++
	if p.holds > 0 {
		l.seq = p.seq
		p.log = append(p.log, l)
	}
}

// reverify verifies each end user's token again and closes the streams of those it refuses. A
// token that cannot be checked (an outage) keeps its stream.
func (w *streamWatch) reverify(project int64, users []*watched) {
	for _, u := range users {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := w.verifier.Verify(ctx, project, u.token)
		cancel()
		switch status := idtoken.Status(err); {
		case status == "expired_token":
			w.close(u, errTokenExpired, "token_expired")
		case status != "":
			w.close(u, errTokenInvalid, "token_revoked")
		case err != nil:
			w.h.log.Warn("documents: verifying a stream's token again", "project", project, "error", err)
		}
	}
}

// InstanceLost closes every Listen and Write stream with UNAVAILABLE, and refuses new ones until
// InstanceRegained: the single-instance lock is lost (ADR-026 §5). The clients resume with their
// tokens once it is taken again.
func (h *Handler) InstanceLost() {
	w := &h.watch
	w.mu.Lock()
	w.lost = true
	var all []*watched
	for _, p := range w.projects {
		all = slices.AppendSeq(all, maps.Keys(p.streams))
	}
	w.mu.Unlock()
	for _, ws := range all {
		w.close(ws, errInstanceLost, "instance_lost")
	}
}

// InstanceRegained admits streams again, once the lock is taken and the hub rebuilt.
func (h *Handler) InstanceRegained() {
	h.watch.mu.Lock()
	h.watch.lost = false
	h.watch.mu.Unlock()
}

// overflow counts and logs a stream limit reached, with the stream's request and client.
func (h *Handler) overflow(s *serverStream, limit string, attrs ...any) {
	h.overflowed(s.ctx, s.info, limit, attrs...)
}

func (h *Handler) overflowed(ctx context.Context, info *rpc, limit string, attrs ...any) {
	h.metrics.overflow(limit)
	args := append([]any{"limit", limit, "request_id", httpx.RequestID(ctx), "client_ip", info.client.String()}, attrs...)
	h.log.Warn("documents: stream limit reached", args...)
}
