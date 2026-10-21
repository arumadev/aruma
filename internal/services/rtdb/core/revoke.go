package core

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/arumadev/aruma/internal/platform/idtoken"
)

// revocations ends the sign-in of user sessions whose tokens stop being valid while they are
// connected: a user disabled or deleted, their tokens revoked, their password or email
// changed. Auth announces each through idtoken.Revocations; this subscribes once per project
// that has user sessions, and for each session a revocation matches (Revocation.Revokes) the
// client is told with ac invalid_token, the session becomes unauthenticated, and the listens it
// may then no longer read are cancelled with c (Session.revoke). The SDKs answer ac by
// reconnecting with a fresh token, which a disabled or deleted user cannot get.
//
// A subscription that falls behind ends with idtoken.ErrOverflow: revocations were lost, so
// every session of the project has its token verified again, after subscribing anew.
type revocations struct {
	src      idtoken.Revocations // nil when Auth does not announce revocations
	verifier idtoken.Verifier
	log      *slog.Logger

	mu       sync.Mutex
	projects map[int64]*watch
}

// watch is one project's subscription and the user sessions it serves.
type watch struct {
	project  int64
	sub      *idtoken.Subscription
	sessions map[*Session]bool
	// holds are the sign-ins in progress (hold), and log what the subscription delivered
	// while there were any, numbered by seq, so a sign-in can tell whether a revocation
	// landed between its verification and its taking effect.
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

// hold is a sign-in in progress on a project's watch.
type hold struct {
	r    *revocations
	w    *watch
	from uint64
}

func newRevocations(src idtoken.Revocations, verifier idtoken.Verifier, log *slog.Logger) *revocations {
	return &revocations{src: src, verifier: verifier, log: log, projects: map[int64]*watch{}}
}

// watchLocked returns the project's watch, subscribing if needed. Callers hold r.mu.
func (r *revocations) watchLocked(project int64) *watch {
	w := r.projects[project]
	if w == nil {
		w = &watch{project: project, sub: r.src.Subscribe(project), sessions: map[*Session]bool{}}
		r.projects[project] = w
		go r.run(w, w.sub)
	}
	return w
}

// closeIfIdleLocked ends a watch that serves nobody. Callers hold r.mu.
func (r *revocations) closeIfIdleLocked(w *watch) {
	if len(w.sessions) == 0 && w.holds == 0 && r.projects[w.project] == w {
		delete(r.projects, w.project)
		w.sub.Close()
	}
}

// hold keeps the project's revocations from now on until release, for a sign-in in progress.
func (r *revocations) hold(project int64) *hold {
	if r.src == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.watchLocked(project)
	w.holds++
	return &hold{r: r, w: w, from: w.seq}
}

func (r *revocations) release(h *hold) {
	if h == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if h.w.holds--; h.w.holds == 0 {
		h.w.log = nil
	}
	r.closeIfIdleLocked(h.w)
}

// revokedSince reports whether a revocation delivered since the hold began ends the session
// that claims would open.
func (h *hold) revokedSince(c *idtoken.Claims) bool {
	if h == nil {
		return false
	}
	h.r.mu.Lock()
	defer h.r.mu.Unlock()
	for _, l := range h.w.log {
		if l.seq > h.from && (l.lost || l.r.Revokes(h.w.project, c)) {
			return true
		}
	}
	return false
}

// join starts watching a user's session; it may already be watched.
func (r *revocations) join(s *Session) {
	if r.src == nil {
		return
	}
	r.mu.Lock()
	r.watchLocked(s.target.Project).sessions[s] = true
	r.mu.Unlock()
	// A session that closed meanwhile has left already, or leaves now.
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		r.leave(s)
	}
}

// leave stops watching a session; it may not be watched.
func (r *revocations) leave(s *Session) {
	if r.src == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if w := r.projects[s.target.Project]; w != nil && w.sessions[s] {
		delete(w.sessions, s)
		r.closeIfIdleLocked(w)
	}
}

// run delivers a watch's revocations until its subscription is closed.
func (r *revocations) run(w *watch, sub *idtoken.Subscription) {
	for {
		rev, ok := <-sub.C()
		if !ok {
			if sub.Err() == nil {
				return // closed: the watch serves nobody
			}
			r.log.Warn("rtdb: revocations lost, verifying every session again", "project", w.project, "error", sub.Err())
			r.mu.Lock()
			if r.projects[w.project] != w {
				r.mu.Unlock()
				return
			}
			w.sub = r.src.Subscribe(w.project)
			sub = w.sub
			r.recordLocked(w, logged{lost: true})
			sessions := w.list()
			r.mu.Unlock()
			r.reverify(w.project, sessions)
			continue
		}
		r.mu.Lock()
		r.recordLocked(w, logged{r: rev})
		sessions := w.list()
		r.mu.Unlock()
		for _, s := range sessions {
			if gen, c, _ := s.user(); c != nil && rev.Revokes(w.project, c) {
				s.revoke(gen, statusInvalidToken, "Auth token is no longer valid.")
			}
		}
	}
}

func (r *revocations) recordLocked(w *watch, l logged) {
	w.seq++
	if w.holds > 0 {
		l.seq = w.seq
		w.log = append(w.log, l)
	}
}

func (w *watch) list() []*Session {
	out := make([]*Session, 0, len(w.sessions))
	for s := range w.sessions {
		out = append(out, s)
	}
	return out
}

// reverify verifies the token of each user session again and ends the sign-in of those it
// refuses. A session whose token cannot be checked (an outage) keeps its sign-in.
func (r *revocations) reverify(project int64, sessions []*Session) {
	for _, s := range sessions {
		gen, c, token := s.user()
		if c == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := r.verifier.Verify(ctx, project, token)
		cancel()
		switch status := idtoken.Status(err); {
		case status == statusExpiredToken:
			s.revoke(gen, status, "Auth token is expired.")
		case status != "":
			s.revoke(gen, status, "Auth token is no longer valid.")
		case err != nil:
			r.log.Warn("rtdb: verifying a session's token again", "project", project, "error", err)
		}
	}
}

// user returns the session's sign-in as a user: its number, claims and token; nil claims for
// any other session.
func (s *Session) user() (gen uint64, c *idtoken.Claims, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signIn, s.auth.User, s.auth.Token
}
