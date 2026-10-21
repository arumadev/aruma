package core

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/osama300/aurma-project/internal/platform/idtoken"
	"github.com/osama300/aurma-project/internal/services/rtdb/rules"
)

// Session is one client's state on one database, apart from any socket: who it acts as and
// the writes it left for its disconnection. The transport above it (a WebSocket, later a long
// poll that may hand over to a WebSocket) can change without the session noticing. A session
// holds a reference on its database's writer until Close.
//
// The methods taking a done callback run the request on the writer and call done there,
// after the commit, in the order the requests were made. done must not block. Errors found
// before the writer is involved (an invalid path, malformed data) are passed to done at once.
// The writer checks the rules, as the session was when the request was made (access.go).
type Session struct {
	svc      *Service
	db       *Database
	target   Target
	loopback bool
	sink     Sink

	mu   sync.Mutex
	auth Auth
	// principal is auth as the rules take it.
	principal rules.Auth
	// signIn counts the changes of auth, so that an expiry or a revocation meant for an
	// earlier sign-in does nothing.
	signIn uint64
	// expiry ends a user's sign-in when its ID token expires.
	expiry *time.Timer
	// pending counts the revocations under way (revoke), which Close waits for.
	pending    sync.WaitGroup
	disconnect onDisconnect
	closed     bool
	// admitted is set once the session counts against its project's connection limit: on
	// its first operation the rules permit, or when it signs in.
	admitted    bool
	releaseSlot func()
}

// OpenOptions describe the client a session serves.
type OpenOptions struct {
	// Loopback is a client whose connection comes from this machine: the only clients the
	// dev_open_rules setting opens the data to.
	Loopback bool
	// Sink receives the session's pushes; nil for a session that never listens (REST).
	Sink Sink
}

// Database is the database the session works on.
func (s *Session) Database() *Database { return s.db }

// Auth returns who the session acts as.
func (s *Session) Auth() Auth {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auth
}

// SetAuth changes who the session acts as. A session that signs in is admitted against the
// project's connection limit at once; ErrTooManyConnections, or an auth variable that is not
// an object, leaves the session as it was. The listens are not checked again here: the
// caller does that with RevokeListens, ordered with its reply.
func (s *Session) SetAuth(a Auth) error {
	_, err := s.setAuth(a, nil)
	return err
}

// setAuth is SetAuth, which with ifSignIn applies only while the session's sign-in is still
// *ifSignIn, and reports whether it applied.
func (s *Session) setAuth(a Auth, ifSignIn *uint64) (bool, error) {
	p, err := a.principal()
	if err != nil {
		return false, err
	}
	if a.signedIn() {
		if err := s.admit(); err != nil {
			return false, err
		}
	}
	s.mu.Lock()
	if s.closed || (ifSignIn != nil && *ifSignIn != s.signIn) {
		s.mu.Unlock()
		return false, nil
	}
	wasUser := s.auth.User != nil
	s.auth, s.principal = a, p
	s.signIn++
	if s.expiry != nil {
		s.expiry.Stop()
		s.expiry = nil
	}
	if a.User != nil && !a.User.Expires.IsZero() {
		gen := s.signIn
		s.expiry = time.AfterFunc(time.Until(a.User.Expires), func() {
			s.revoke(gen, statusExpiredToken, "Auth token is expired.")
		})
	}
	s.mu.Unlock()
	switch {
	case a.User != nil:
		s.svc.revocations.join(s)
	case wasUser:
		s.svc.revocations.leave(s)
	}
	return true, nil
}

// SignIn verifies a user's ID token for the session's project and signs the session in as
// that user (auth is {provider, uid, token}). A token the verifier refuses fails with its
// idtoken error, whose Status is the reply's status; any other error is an outage. Either way
// the session is left as it was.
func (s *Session) SignIn(ctx context.Context, token string) error {
	if s.svc.idTokens == nil {
		return idtoken.ErrInvalid
	}
	// Hold the project's revocations from before the verification on, so that one that
	// lands between the two is not missed (revoke.go).
	w := s.svc.revocations.hold(s.target.Project)
	defer s.svc.revocations.release(w)
	claims, err := s.svc.idTokens.Verify(ctx, s.target.Project, token)
	if err != nil {
		return err
	}
	if w.revokedSince(claims) {
		return idtoken.ErrRevoked
	}
	return s.SetAuth(Auth{User: claims, Token: token})
}

// RevokeIf ends the session's sign-in if it is one that which accepts, as the server does when
// a credential stops being valid: the client is told with an ac push of status and reason,
// the session becomes unauthenticated, and the listens it may then no longer read are
// cancelled with c.
func (s *Session) RevokeIf(which func(Auth) bool, status, reason string) {
	s.mu.Lock()
	gen, ok := s.signIn, which(s.auth)
	s.mu.Unlock()
	if ok {
		s.revoke(gen, status, reason)
	}
}

// revoke is Revoke for sign-in gen only.
func (s *Session) revoke(gen uint64, status, reason string) {
	s.mu.Lock()
	if s.closed || s.signIn != gen {
		s.mu.Unlock()
		return
	}
	// Close waits for this to have queued its commands, while it still holds the database.
	s.pending.Add(1)
	defer s.pending.Done()
	s.mu.Unlock()
	if s.sink != nil {
		s.sink.Push(Push{Action: PushAuthRevoked, Status: status, Reason: reason})
	}
	if ok, _ := s.setAuth(Auth{}, &gen); ok {
		s.RevokeListens(func() {})
	}
}

// Authenticated reports whether the session has signed in, or works under the open rules of
// development: such sessions may send messages up to the full message limit.
func (s *Session) Authenticated() bool {
	return s.Auth().signedIn() || s.devOpen()
}

func (s *Session) devOpen() bool { return s.svc.openRules && s.loopback }

// admit counts the session against its project's connection limit, once.
func (s *Session) admit() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.admitted {
		return nil
	}
	release, err := s.svc.connect(s.target)
	if err != nil {
		return err
	}
	s.admitted, s.releaseSlot = true, release
	return nil
}

// Get reads the value at path, or with a query the part of it a query selects. A query
// ordered by a child or by value needs the .indexOn that covers it (NoIndexError).
func (s *Session) Get(path string, spec *QuerySpec, done func(*Node, error)) {
	p, q, err := parseRead(path, spec)
	if err != nil {
		done(nil, err)
		return
	}
	c := &command{acc: s.access(), read: p, done: func(r Result) { done(r.Node, r.Err) }}
	if !q.IsDefault() {
		c.query = &q
	}
	s.db.submit(c)
}

// GetREST reads as a REST GET does: as Get, except that rules see a read without a query as a
// plain REST read, ordered by key (rules.PlainREST, emulator 4.11.2). With probe it only checks
// the read, rules and index, and done receives no data: a shallow read takes the rows itself
// (Service.Shallow).
func (s *Session) GetREST(path string, spec *QuerySpec, probe bool, done func(*Node, error)) {
	p, q, err := parseRead(path, spec)
	if err != nil {
		done(nil, err)
		return
	}
	c := &command{acc: s.access(), read: p, rest: true, probe: probe, done: func(r Result) { done(r.Node, r.Err) }}
	if spec != nil {
		c.query = &q
	}
	s.db.submit(c)
}

func parseRead(path string, spec *QuerySpec) (Path, Query, error) {
	p, err := ParsePath(path)
	if err != nil {
		return nil, Query{}, err
	}
	q := DefaultQuery
	if spec != nil {
		if q, err = NewQuery(*spec); err != nil {
			return nil, Query{}, err
		}
	}
	return p, q, nil
}

// Listen starts a listen at path with a query (nil for the default listen), tagged tag by
// the client. The data go to the session's sink before done, which receives the listen's
// warnings (no_index when the query is served unfiltered). hash is the hash of what the
// client already has; when it matches, no data are sent. A second listen with the same path
// and query replaces the first.
func (s *Session) Listen(path string, spec *QuerySpec, tag int64, hash string, done func(warnings []string, err error)) {
	p, q, err := parseRead(path, spec)
	if err != nil {
		done(nil, err)
		return
	}
	if q.IsDefault() {
		spec, tag = nil, 0
	}
	l := &listen{sess: s, path: p, query: q, spec: spec, tag: tag, id: tag}
	s.db.submit(&command{acc: s.access(), listen: &listenCmd{l: l, hash: hash}, done: func(r Result) { done(r.Warnings, r.Err) }})
}

// Unlisten stops the listen at path with the query (nil for the default listen) and the tag
// it was started with. Stopping a listen that does not exist is not an error.
func (s *Session) Unlisten(path string, spec *QuerySpec, tag int64, done func(error)) {
	p, q, err := parseRead(path, spec)
	if err != nil {
		done(err)
		return
	}
	if q.IsDefault() {
		tag = 0
	}
	s.db.submit(&command{unlisten: &unlistenCmd{sess: s, key: listenKey(p, q, tag)}, done: func(Result) { done(nil) }})
}

// RevokeListens checks the session's listens against the rules again, as the session is now,
// and cancels with a c push each those it may no longer read: after it signed out or in as
// someone else. done runs once the pushes are queued.
func (s *Session) RevokeListens(done func()) {
	s.db.submit(&command{revoke: s, done: func(Result) { done() }})
}

// Set writes data, in export format, at path. With hash it applies only while the value at
// path still hashes to *hash. done receives the value written, server values resolved.
func (s *Session) Set(path string, data json.RawMessage, hash *string, done func(*Node, error)) {
	w, err := s.parseSet(path, data, hash)
	if err != nil {
		done(nil, err)
		return
	}
	s.db.submit(&command{acc: s.access(), write: w, done: func(r Result) { done(r.Node, r.Err) }})
}

// Update writes each child of data, an object keyed by paths relative to path, at once.
func (s *Session) Update(path string, data json.RawMessage, done func(error)) {
	s.UpdateWritten(path, data, func(_ []Child, err error) { done(err) })
}

// UpdateWritten is Update, whose done also receives the children as written, in key order,
// server values resolved and null ones kept: what REST echoes.
func (s *Session) UpdateWritten(path string, data json.RawMessage, done func([]Child, error)) {
	w, err := s.parseUpdate(path, data)
	if err != nil {
		done(nil, err)
		return
	}
	s.db.submit(&command{acc: s.access(), write: w, done: func(r Result) { done(r.Merge, r.Err) }})
}

func (s *Session) parseSet(path string, data json.RawMessage, hash *string) (*Write, error) {
	p, err := ParseWritePath(path)
	if err != nil {
		return nil, err
	}
	return ParseSet(p, data, hash, int(s.svc.limits.StringBytes))
}

func (s *Session) parseUpdate(path string, data json.RawMessage) (*Write, error) {
	p, err := ParsePath(path)
	if err != nil {
		return nil, err
	}
	return ParseUpdate(p, data, int(s.svc.limits.StringBytes))
}

// OnDisconnectSet registers a set to run when the session closes. As in Firebase, the rules
// check it when it is registered, and again when it runs.
func (s *Session) OnDisconnectSet(path string, data json.RawMessage, done func(error)) {
	w, err := s.parseSet(path, data, nil)
	if err != nil {
		done(err)
		return
	}
	s.register(w, func() { s.disconnect.remember(w.Path, w.Value) }, done)
}

// OnDisconnectUpdate registers an update to run when the session closes. The rules check
// each of its children on its own.
func (s *Session) OnDisconnectUpdate(path string, data json.RawMessage, done func(error)) {
	w, err := s.parseUpdate(path, data)
	if err != nil {
		done(err)
		return
	}
	s.register(w, func() {
		for _, c := range w.Update {
			s.disconnect.remember(w.Path.Join(c.Path), c.Value)
		}
	}, done)
}

// register has the writer check an onDisconnect write, then keeps it with keep, under s.mu.
// It is kept even when the session has begun to close: Close takes the writes only once
// every command queued before it has run.
func (s *Session) register(w *Write, keep func(), done func(error)) {
	s.db.submit(&command{acc: s.access(), register: w, done: func(r Result) {
		if r.Err == nil {
			s.mu.Lock()
			keep()
			s.mu.Unlock()
		}
		done(r.Err)
	}})
}

// OnDisconnectCancel drops the registered writes at path and below. It checks no rule, as
// in Firebase: it only withdraws what this session registered.
func (s *Session) OnDisconnectCancel(path string) error {
	p, err := ParsePath(path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disconnect.forget(p)
	return nil
}

// Close runs the registered onDisconnect writes through the writer and waits until every
// command the session queued has run, then releases the database. It is idempotent.
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.expiry != nil {
		s.expiry.Stop()
	}
	release := s.releaseSlot
	s.mu.Unlock()
	s.svc.revocations.leave(s)
	s.pending.Wait() // a revocation or an expiry under way, which queues on the writer

	// The listens go first, so the session's own onDisconnect writes are not pushed to it.
	// This also is a barrier: the writes and registrations this session queued before
	// closing have all run before its onDisconnect writes are taken and before the reference
	// goes, so a writer retired after this never leaves one behind.
	dropped := make(chan struct{})
	s.db.submit(&command{drop: s, done: func(Result) { close(dropped) }})
	<-dropped
	s.mu.Lock()
	w := s.disconnect.write()
	s.mu.Unlock()
	if w != nil {
		// The rules check each registered write again, as the session is now.
		w.Separately = true
		wait := make(chan Result, 1)
		s.db.submit(&command{acc: s.access(), write: w, done: func(r Result) { wait <- r }})
		if r := <-wait; r.Err != nil {
			s.svc.log.Warn("rtdb: onDisconnect writes failed", "database", s.db.name, "error", r.Err)
		}
	}
	if release != nil {
		release()
	}
	s.svc.registry.release(s.db)
}
