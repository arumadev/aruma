package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/osama300/aurma-project/internal/platform/idtoken"
)

// userAuth is a user's sign-in, as a verified ID token gives it.
func userAuth(uid string, claims map[string]any) Auth {
	token := map[string]any{"sub": uid, "user_id": uid, "firebase": map[string]any{"sign_in_provider": "password"}}
	for k, v := range claims {
		token[k] = v
	}
	return Auth{User: &idtoken.Claims{UID: uid, SignInProvider: "password", Token: token, Expires: time.Now().Add(time.Hour)}}
}

const profileRules = `{"rules": {
	"users": {"$uid": {
		".read": "auth != null && auth.uid == $uid",
		".write": "auth != null && auth.uid == $uid && root.child('banned').child($uid).val() != true",
		"age": {".validate": "newData.isNumber() && newData.val() >= 0"},
		"seen": {".validate": "newData.val() == now"}
	}},
	"admins": {".read": "auth.token.admin == true", ".indexOn": ["score"]},
	"top": {".read": "query.orderByChild == 'score' && query.limitToFirst <= 2", ".indexOn": "score"},
	"banned": {".read": false},
	"open": {".read": true, ".write": true}
}}`

func rulesService(t *testing.T, doc string) (*Service, *memStore) {
	t.Helper()
	store := newMemStore()
	store.rules = map[string]string{"chat-app-default-rtdb": doc}
	return newTestService(t, store, Options{}), store
}

// Every operation of a client answers to the rules: reads and listens with their query,
// writes with their resolved data, .validate, root lookups, and onDisconnect when registered.
func TestRulesGovernClients(t *testing.T) {
	s, _ := rulesService(t, profileRules)
	adm := open(t, s, "chat-app-default-rtdb", admin)
	set(adm, "/", `{"users":{"u1":{"name":"one"},"u2":{"name":"two"}},"banned":{"u3":true},"top":{"a":{"score":1},"b":{"score":2},"c":{"score":3}}}`, nil)
	u1 := open(t, s, "chat-app-default-rtdb", userAuth("u1", nil))
	u3 := open(t, s, "chat-app-default-rtdb", userAuth("u3", map[string]any{"admin": true}))
	anon := open(t, s, "chat-app-default-rtdb", Auth{})

	allowed := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Errorf("%s: %v, want allowed", what, err)
		}
	}
	denied := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, ErrPermissionDenied) {
			t.Errorf("%s: %v, want permission_denied", what, err)
		}
	}
	_, err := get(u1, "/users/u1")
	allowed("u1 reads its own", err)
	_, err = get(u1, "/users/u2")
	denied("u1 reads another's", err)
	_, err = get(anon, "/users/u1")
	denied("anonymous reads", err)
	_, err = get(u1, "/users")
	denied("a grant below does not open the parent", err)
	_, err = set(u1, "/users/u1/name", `"uno"`, nil)
	allowed("u1 writes its own", err)
	_, err = set(u1, "/users/u2/name", `"x"`, nil)
	denied("u1 writes another's", err)
	_, err = set(u3, "/users/u3/name", `"x"`, nil)
	denied("root.child: a banned user", err)
	_, err = set(u1, "/users/u1/age", `"old"`, nil)
	denied(".validate", err)
	_, err = set(u1, "/users/u1/age", `-1`, nil)
	denied(".validate on the value", err)
	_, err = set(u1, "/users/u1/age", `30`, nil)
	allowed(".validate passes", err)
	_, err = set(u1, "/users/u1/seen", `{".sv":"timestamp"}`, nil)
	allowed("rules see the resolved server value", err)
	_, err = set(u1, "/users/u1", `null`, nil)
	allowed("a deletion skips .validate", err)
	allowed("an update of its own children", update(u1, "/users/u1", `{"name":"one","age":5}`))
	denied("an update reaching another's", update(u1, "/users", `{"u1/name":"a","u2/name":"b"}`))
	denied("an update failing .validate", update(u1, "/users/u1", `{"age":"x"}`))
	allowed("an empty update", update(u1, "/users/u1", `{}`))
	_, err = set(u1, "/users/u1/.priority", `5`, nil)
	allowed("a priority, as a write of its node", err)
	_, err = set(u1, "/users/u2/.priority", `5`, nil)
	denied("another's priority", err)
	_, err = get(u3, "/admins")
	allowed("auth.token claims", err)
	_, err = get(u1, "/admins")
	denied("auth.token claims missing", err)

	// The query variable is the query asked: a get with a query, a listen without one.
	sp := spec(t, `{"i":"score","l":2,"vf":"l"}`)
	ch := make(chan error, 1)
	u1.Get("/top", &sp, func(_ *Node, err error) { ch <- err })
	allowed("a query the rules accept", <-ch)
	sp = spec(t, `{"i":"score","l":3,"vf":"l"}`)
	u1.Get("/top", &sp, func(_ *Node, err error) { ch <- err })
	denied("a query the rules refuse", <-ch)
	u1.Listen("/top", nil, 0, "", func(_ []string, err error) { ch <- err })
	denied("a listen without a query: ordered by priority", <-ch)

	// onDisconnect is checked as it is registered; cancelling checks nothing.
	denied("onDisconnect set elsewhere", onDisconnectSet(u1, "/users/u2/x", `1`))
	denied("onDisconnect failing .validate", onDisconnectSet(u1, "/users/u1/age", `"x"`))
	allowed("onDisconnect set", onDisconnectSet(u1, "/users/u1/x", `1`))
	denied("onDisconnect update with a child elsewhere", onDisconnectUpdate(u1, "/users", `{"u1/a":1,"u2/a":1}`))
	allowed("onDisconnect update", onDisconnectUpdate(u1, "/users/u1", `{"a":1,"b":2}`))
	allowed("onDisconnect cancel", anon.OnDisconnectCancel("/users/u2"))
}

// Rules read the data as the writer has it when the command runs: after the writes queued
// before it, in the same transaction.
func TestRulesSeeTheWritersView(t *testing.T) {
	s, _ := rulesService(t, `{"rules": {
		"gate": {".read": true, ".write": true},
		"x": {".write": "root.child('gate').val() == true"}
	}}`)
	c := open(t, s, "chat-app-default-rtdb", Auth{})
	first, second := make(chan error, 1), make(chan error, 1)
	c.Set("/gate", json.RawMessage(`true`), nil, func(_ *Node, err error) { first <- err })
	c.Set("/x", json.RawMessage(`1`), nil, func(_ *Node, err error) { second <- err })
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Errorf("a write allowed by the one queued before it: %v", err)
	}
	c.Set("/gate", json.RawMessage(`false`), nil, func(_ *Node, err error) { first <- err })
	c.Set("/x", json.RawMessage(`2`), nil, func(_ *Node, err error) { second <- err })
	<-first
	if err := <-second; !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("a write denied by the one queued before it: %v", err)
	}
}

// A failure to read the data while checking the rules is an outage, never a denial.
func TestRulesSourceErrorIsUnavailable(t *testing.T) {
	s, store := rulesService(t, `{"rules": {".read": "root.child('open').val() == true", ".write": "data.exists()"}}`)
	c := open(t, s, "chat-app-default-rtdb", Auth{})
	store.set(func() { store.peekErr = errors.New("connection refused") })
	if _, err := get(c, "/a"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("read: %v", err)
	}
	if _, err := set(c, "/a", `1`, nil); !errors.Is(err, ErrUnavailable) {
		t.Errorf("write: %v", err)
	}
	store.set(func() { store.peekErr = nil })
	if _, err := get(c, "/a"); !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("read once the database is back: %v", err)
	}
}

// Admins bypass the rules, .validate included; dev_open_rules opens a loopback client's
// reads and writes but not other clients'.
func TestRulesBypassAndDevOpen(t *testing.T) {
	doc := `{"rules": {".read": false, ".write": false, "v": {".validate": false}, "q": {".indexOn": "s"}}}`
	s, _ := rulesService(t, doc)
	a := open(t, s, "chat-app-default-rtdb", admin)
	if _, err := set(a, "/v", `1`, nil); err != nil {
		t.Errorf("admin past .validate: %v", err)
	}
	store := newMemStore()
	store.rules = map[string]string{"chat-app-default-rtdb": doc}
	dev := newTestService(t, store, Options{DevOpenRules: true})
	local, rec := listenWith(t, dev, "chat-app-default-rtdb", Auth{})
	if _, err := set(local, "/v", `1`, nil); err != nil {
		t.Errorf("dev open past .validate: %v", err)
	}
	set(local, "/q", `{"a":{"s":2},"b":{"s":1}}`, nil)
	// .indexOn still comes from the database's rules: the listen is filtered and tagged.
	if w, err := listenSync(t, local, rec, "/q", `{"i":"s","l":1,"vf":"l"}`, 4, ""); err != nil || w != nil {
		t.Fatalf("dev open listen: %v %v", w, err)
	}
	want(t, "dev open listen", rec.take(), `d q t4 {"b":{"s":1}}`, "ok listen /q")
}

// Changing the rules checks every open listen again: those the new rules refuse are cancelled
// with c, the others are left as they were, and the new .indexOn takes effect with them.
func TestRulesChangeCancelsListens(t *testing.T) {
	s, _ := rulesService(t, `{"rules": {"a": {".read": true}, "b": {".read": "auth != null"}, "q": {".read": true}}}`)
	adm := open(t, s, "chat-app-default-rtdb", admin)
	set(adm, "/", `{"a":1,"b":2,"q":{"x":{"s":2},"y":{"s":1}}}`, nil)
	c, rec := listenWith(t, s, "chat-app-default-rtdb", userAuth("u1", nil))
	listenSync(t, c, rec, "/a", "", 0, "")
	listenSync(t, c, rec, "/b", "", 0, "")
	listenSync(t, c, rec, "/b", `{"l":1,"vf":"l"}`, 7, "")
	rec.take()

	tg, _ := s.Resolve(context.Background(), "chat-app-default-rtdb")
	if err := s.SetRules(context.Background(), tg, `{"rules": {"a": {".read": true}, "b": {".read": false}, "q": {".read": true, ".indexOn": "s"}}}`); err != nil {
		t.Fatal(err)
	}
	want(t, "after the change", rec.take(), "c b q", "c b")
	set(adm, "/a", `2`, nil)
	set(adm, "/b", `3`, nil)
	want(t, "pushes after", rec.take(), "d a 2")
	if w, err := listenSync(t, c, rec, "/q", `{"i":"s","l":1,"vf":"l"}`, 8, ""); err != nil || w != nil {
		t.Errorf("the new .indexOn: %v %v", w, err)
	}
}

// onDisconnect writes are checked again when they run, each on its own, as the session is
// then: a write the rules now refuse is dropped and the others run.
func TestOnDisconnectCheckedWhenItRuns(t *testing.T) {
	doc := `{"rules": {"users": {"$uid": {".read": true, ".write": "auth != null && auth.uid == $uid"}}, "open": {".read": true, ".write": true}}}`
	s, _ := rulesService(t, doc)
	adm := open(t, s, "chat-app-default-rtdb", admin)
	tg, _ := s.Resolve(context.Background(), "chat-app-default-rtdb")

	// Registered, then the rules change.
	a, _ := s.Open(tg, userAuth("u1", nil), OpenOptions{Loopback: true})
	if err := onDisconnectSet(a, "/users/u1/a", `"A"`); err != nil {
		t.Fatal(err)
	}
	if err := onDisconnectUpdate(a, "/", `{"open/a":"A","users/u1/b":"B"}`); err != nil {
		t.Fatal(err)
	}
	s.SetRules(context.Background(), tg, `{"rules": {"users": {"$uid": {".read": true, ".write": false}}, "open": {".read": true, ".write": true}}}`)
	a.Close()
	if got, _ := get(adm, "/"); got != `{"open":{"a":"A"}}` {
		t.Errorf("after the rules changed: %s", got)
	}

	// Registered, then signed out.
	s.SetRules(context.Background(), tg, doc)
	b, _ := s.Open(tg, userAuth("u1", nil), OpenOptions{Loopback: true})
	if err := onDisconnectSet(b, "/users/u1/c", `"C"`); err != nil {
		t.Fatal(err)
	}
	b.SetAuth(Auth{})
	b.Close()
	if got, _ := get(adm, "/users"); got != `null` {
		t.Errorf("after signing out: %s", got)
	}
}
