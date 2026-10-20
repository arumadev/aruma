package core

import (
	"encoding/json"
	"sync"
)

// Session is one client's state on one database, apart from any socket: who it acts as and
// the writes it left for its disconnection. The transport above it (a WebSocket, later a long
// poll that may hand over to a WebSocket) can change without the session noticing. A session
// holds a reference on its database's writer until Close.
//
// The methods taking a done callback run the request on the writer and call done there,
// after the commit, in the order the requests were made. done must not block. Errors found
// before the writer is involved (an invalid path, permission) are passed to done at once.
// Permission is checked on the path before the data is parsed, so a client who may not write
// cannot make the server parse anything large.
type Session struct {
	svc      *Service
	db       *Database
	target   Target
	loopback bool

	mu         sync.Mutex
	auth       Auth
	disconnect onDisconnect
	closed     bool
	// admitted is set once the session counts against its project's connection limit: on
	// its first permitted operation, or when it signs in as an admin.
	admitted    bool
	releaseSlot func()
}

// OpenOptions describe the client a session serves.
type OpenOptions struct {
	// Loopback is a client whose connection comes from this machine: the only clients the
	// dev_open_rules setting opens the data to.
	Loopback bool
}

// Database is the database the session works on.
func (s *Session) Database() *Database { return s.db }

// Auth returns who the session acts as.
func (s *Session) Auth() Auth {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auth
}

// SetAuth changes who the session acts as. An admin is admitted against the project's
// connection limit at once; ErrTooManyConnections leaves the session as it was.
func (s *Session) SetAuth(a Auth) error {
	if a.Admin {
		if err := s.admit(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auth = a
	return nil
}

// Authenticated reports whether the session has signed in, or works under the open rules of
// development: such sessions may send messages up to the full message limit.
func (s *Session) Authenticated() bool {
	a := s.Auth()
	return a.Admin || a.Variable != nil || s.devOpen()
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

// Get reads the value at path.
func (s *Session) Get(path string, done func(*Node, error)) {
	p, err := ParsePath(path)
	if err == nil {
		err = s.canRead(p)
	}
	if err != nil {
		done(nil, err)
		return
	}
	s.db.submit(&command{read: p, done: func(r Result) { done(r.Node, r.Err) }})
}

// Set writes data, in export format, at path. With hash it applies only while the value at
// path still hashes to *hash; done then receives the value written.
func (s *Session) Set(path string, data json.RawMessage, hash *string, done func(*Node, error)) {
	w, err := s.parseSet(path, data, hash)
	if err != nil {
		done(nil, err)
		return
	}
	s.db.submit(&command{write: w, done: func(r Result) { done(r.Node, r.Err) }})
}

// Update writes each child of data, an object keyed by paths relative to path, at once.
func (s *Session) Update(path string, data json.RawMessage, done func(error)) {
	w, err := s.parseUpdate(path, data)
	if err != nil {
		done(err)
		return
	}
	s.db.submit(&command{write: w, done: func(r Result) { done(r.Err) }})
}

func (s *Session) parseSet(path string, data json.RawMessage, hash *string) (*Write, error) {
	p, err := ParseWritePath(path)
	if err != nil {
		return nil, err
	}
	if err := s.canWrite(p); err != nil {
		return nil, err
	}
	return ParseSet(p, data, hash, int(s.svc.limits.StringBytes))
}

func (s *Session) parseUpdate(path string, data json.RawMessage) (*Write, error) {
	p, err := ParsePath(path)
	if err != nil {
		return nil, err
	}
	// Until rules (R4) check each child, the gate is the same for every path.
	if err := s.canWrite(p); err != nil {
		return nil, err
	}
	return ParseUpdate(p, data, int(s.svc.limits.StringBytes))
}

// OnDisconnectSet registers a set to run when the session closes. As in Firebase,
// permission is checked when it is registered (and, with rules, again when it runs).
func (s *Session) OnDisconnectSet(path string, data json.RawMessage) error {
	w, err := s.parseSet(path, data, nil)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disconnect.remember(w.Path, w.Value)
	return nil
}

// OnDisconnectUpdate registers an update to run when the session closes.
func (s *Session) OnDisconnectUpdate(path string, data json.RawMessage) error {
	w, err := s.parseUpdate(path, data)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range w.Update {
		s.disconnect.remember(w.Path.Join(c.Path), c.Value)
	}
	return nil
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
	w := s.disconnect.write()
	release := s.releaseSlot
	s.mu.Unlock()

	if w != nil {
		wait := make(chan Result, 1)
		s.db.submit(&command{write: w, done: func(r Result) { wait <- r }})
		if r := <-wait; r.Err != nil {
			s.svc.log.Warn("rtdb: onDisconnect writes failed", "database", s.db.name, "error", r.Err)
		}
	} else {
		// A barrier: the writes this session queued before closing have all run before the
		// reference goes, so a writer retired after this never leaves one behind.
		wait := make(chan struct{})
		s.db.submit(&command{barrier: true, done: func(Result) { close(wait) }})
		<-wait
	}
	if release != nil {
		release()
	}
	s.svc.registry.release(s.db)
}
