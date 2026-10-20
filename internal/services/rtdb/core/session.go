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
type Session struct {
	svc *Service
	db  *Database

	mu         sync.Mutex
	auth       Auth
	disconnect onDisconnect
	closed     bool
}

// Database is the database the session works on.
func (s *Session) Database() *Database { return s.db }

// Auth returns who the session acts as.
func (s *Session) Auth() Auth {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auth
}

// SetAuth changes who the session acts as.
func (s *Session) SetAuth(a Auth) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auth = a
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
	w, err := ParseSet(path, data, hash, int(s.svc.limits.StringBytes))
	if err == nil {
		err = s.canWrite(w.Path)
	}
	if err != nil {
		done(nil, err)
		return
	}
	s.db.submit(&command{write: w, done: func(r Result) { done(r.Node, r.Err) }})
}

// Update writes each child of data, an object keyed by paths relative to path, at once.
func (s *Session) Update(path string, data json.RawMessage, done func(error)) {
	w, err := ParseUpdate(path, data, int(s.svc.limits.StringBytes))
	if err == nil {
		err = s.canWrite(w.Path)
	}
	if err != nil {
		done(err)
		return
	}
	s.db.submit(&command{write: w, done: func(r Result) { done(r.Err) }})
}

// OnDisconnectSet registers a set to run when the session closes. As in Firebase,
// permission is checked when it is registered (and, with rules, again when it runs).
func (s *Session) OnDisconnectSet(path string, data json.RawMessage) error {
	w, err := ParseSet(path, data, nil, int(s.svc.limits.StringBytes))
	if err == nil {
		err = s.canWrite(w.Path)
	}
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
	w, err := ParseUpdate(path, data, int(s.svc.limits.StringBytes))
	if err == nil {
		err = s.canWrite(w.Path)
	}
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

// OnDisconnectCancel drops the registered writes at path and below.
func (s *Session) OnDisconnectCancel(path string) error {
	p, err := ParsePath(path)
	if err == nil {
		err = s.canWrite(p)
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disconnect.forget(p)
	return nil
}

// Close runs the registered onDisconnect writes through the writer, waits for them, and
// releases the database. It is idempotent.
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	w := s.disconnect.write()
	s.mu.Unlock()

	if w != nil {
		wait := make(chan Result, 1)
		s.db.submit(&command{write: w, done: func(r Result) { wait <- r }})
		if r := <-wait; r.Err != nil {
			s.svc.log.Warn("rtdb: onDisconnect writes failed", "database", s.db.name, "error", r.Err)
		}
	}
	s.svc.registry.release(s.db)
}
