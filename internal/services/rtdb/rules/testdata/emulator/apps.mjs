// Twenty rules files shaped like those of real apps (testdata/apps), after the categories of
// docs/research/rtdb/H-rules-usage-analysis.txt, each with data and operations a client of that
// app would send. capture.mjs records the emulator's verdicts in apps.golden.json.
import fs from 'node:fs';

const dir = new URL('../apps/', import.meta.url);
const rulesText = name => fs.readFileSync(new URL(name, dir), 'utf8');

const u1 = {uid: 'u1'};
const u2 = {uid: 'u2'};
const u3 = {uid: 'u3'};
const set = (path, value, auth = null) => ({op: 'set', path, value, auth});
const update = (path, value, auth = null) => ({op: 'update', path, value, auth});
const del = (path, auth = null) => ({op: 'delete', path, auth});
const read = (path, auth = null, query) => (query ? {op: 'read', path, auth, query} : {op: 'read', path, auth});
const odSet = (path, value, auth = null) => ({op: 'onDisconnectSet', path, value, auth});
const odUpdate = (path, value, auth = null) => ({op: 'onDisconnectUpdate', path, value, auth});
const future = 4000000000000;
const past = 1000000000000;

const apps = [
  ['01-open-dev.rules.json', {a: 1}, [
    read('/'), read('/a/b'), set('/a', 2), del('/'), update('/', {'x/y': 1, a: null}), read('/', u1),
  ]],
  ['02-locked.rules.json', {a: 1}, [
    read('/'), read('/a', u1), set('/a', 2, u1), del('/a'), update('/', {}), odSet('/a', 1, u1),
  ]],
  ['03-signed-in-only.rules.json', {a: {b: 1}}, [
    read('/a'), read('/a', u1), set('/a/b', 2), set('/a/b', 2, u1), update('/', {c: 1}, u2), del('/a', u1), odSet('/p', 1),
  ]],
  ['04-user-profiles.rules.json', {users: {u1: {name: 'Ann', age: 30}}, publicProfiles: {u1: {displayName: 'Ann'}}}, [
    read('/users/u1', u1), read('/users/u1', u2), read('/users'), read('/publicProfiles/u1'),
    set('/users/u2', {name: 'Bo', age: 20}, u2), set('/users/u2', {name: 'Bo'}, u2), set('/users/u2', {name: 'Bo', age: 9}, u2),
    set('/users/u2', {name: '', age: 20}, u2), set('/users/u2', {name: 'Bo', age: 20, admin: true}, u2),
    set('/users/u1/age', 31, u1), set('/users/u1/age', '31', u1), del('/users/u1/name', u1), del('/users/u1', u1),
    update('/users/u1', {bio: 'hello'}, u1), update('/users/u1', {bio: 'x'.repeat(600)}, u1), set('/users/u1/age', 31, u2),
    set('/publicProfiles/u2/displayName', 'Bo', u2), set('/publicProfiles/u2/displayName', 7, u2), set('/publicProfiles/u2/extra', 7, u2),
  ]],
  ['05-chat-rooms.rules.json', {
    rooms: {r1: {name: 'General', owner: 'u1'}},
    members: {r1: {u1: true, u2: true}},
    messages: {r1: {m1: {uid: 'u1', text: 'hi', ts: past}}},
  }, [
    read('/rooms', u3), read('/rooms'), read('/members/r1', u2), read('/members/r1', u3), read('/messages/r1', u2), read('/messages/r1', u3),
    set('/messages/r1/m2', {uid: 'u2', text: 'hello', ts: past}, u2), set('/messages/r1/m2', {uid: 'u1', text: 'hello', ts: past}, u2),
    set('/messages/r1/m2', {uid: 'u3', text: 'hello', ts: past}, u3), set('/messages/r1/m2', {uid: 'u2', text: '', ts: past}, u2),
    set('/messages/r1/m2', {uid: 'u2', text: 'hi', ts: future}, u2), set('/messages/r1/m2', {uid: 'u2', text: 'hi', ts: past, x: 1}, u2),
    set('/messages/r1/m1', {uid: 'u1', text: 'edit', ts: past}, u1), set('/members/r1/u3', true, u3), set('/members/r1/u3', true, u1),
    set('/members/r1/u3', 'yes', u1), set('/rooms/r2', {name: 'New', owner: 'u3'}, u3), set('/rooms/r1/name', 'Renamed', u2),
    set('/rooms/r1/name', 'Renamed', u1), update('/rooms/r1', {owner: 'u2'}, u1), del('/rooms/r1', u1),
    update('/', {'rooms/r3': {name: 'Three', owner: 'u2'}, 'members/r3/u2': true}, u2),
    read('/messages/r1', u2, {orderBy: 'ts', limitToLast: 10}),
  ]],
  ['06-todo-list.rules.json', {todos: {u1: {t1: {title: 'Milk', done: false}}}}, [
    read('/todos/u1', u1), read('/todos/u1', u2), read('/todos', u1),
    set('/todos/u1/t2', {title: 'Eggs', done: false, createdAt: past}, u1), set('/todos/u1/t2', {title: 'Eggs'}, u1),
    set('/todos/u1/t2', {title: 'Eggs', done: 'no'}, u1), set('/todos/u1/t2', {title: 'Eggs', done: false, color: 'red'}, u1),
    set('/todos/u1/t1/done', true, u1), set('/todos/u1/t1/done', true, u2), del('/todos/u1/t1', u1), del('/todos/u1/t1/title', u1),
    update('/todos/u1', {t1: null, t3: {title: 'Bread', done: true}}, u1), read('/todos/u1', u1, {orderBy: 'done', equalTo: false}),
  ]],
  ['07-blog.rules.json', {
    admins: {u3: true},
    posts: {p1: {title: 'Hello', body: '...', author: 'u1', publishedAt: past}},
  }, [
    read('/posts'), read('/admins', u3), read('/admins', u1),
    set('/posts/p2', {title: 'Mine', body: 'b', author: 'u2'}, u2), set('/posts/p2', {title: 'Mine', body: 'b', author: 'u1'}, u2),
    set('/posts/p2', {title: 'No', body: 'b', author: 'u2'}, u2), set('/posts/p1/title', 'Hello again', u1),
    set('/posts/p1/title', 'Hello again', u2), set('/posts/p1/title', 'Moderated', u3), set('/posts/p1/author', 'u2', u1),
    del('/posts/p1', u1), del('/posts/p1', u2), set('/posts/p1/comments/c1', {by: 'u2', text: 'nice'}, u2),
    set('/posts/p1/comments/c1', {by: 'u1', text: 'nice'}, u2), set('/posts/p1/comments/c1', {by: 'u2', text: 5}, u2),
    read('/posts', null, {orderBy: 'author', equalTo: 'u1'}),
  ]],
  ['08-custom-claims.rules.json', {config: {theme: 'dark'}, articles: {a1: {status: 'draft'}}, billing: {u1: {plan: 'pro'}}}, [
    read('/config'), read('/config', u1), set('/config/theme', 'light', u1), set('/config/theme', 'light', {uid: 'boss', claims: {admin: true}}),
    set('/articles/a1/status', 'draft', {uid: 'ed', claims: {role: 'editor'}}),
    set('/articles/a1/status', 'published', {uid: 'ed', claims: {role: 'editor'}}),
    set('/articles/a1/status', 'published', {uid: 'boss', claims: {admin: true}}),
    set('/articles/a1/status', 'draft', {uid: 'x', claims: {role: 'viewer'}}),
    read('/billing/u1', {uid: 'u1', claims: {subscriptionStatus: 'active'}}), read('/billing/u1', {uid: 'u1', claims: {subscriptionStatus: 'past_due'}}),
    read('/billing/u1', u1), set('/billing/u1/plan', 'free', {uid: 'boss', claims: {admin: true}}),
  ]],
  ['09-company-email.rules.json', {staff: {u1: {email: 'ann@example.com'}}, guests: {}}, [
    read('/', {uid: 'u1', claims: {email: 'ann@example.com', email_verified: true}}),
    read('/', {uid: 'u1', claims: {email: 'ann@example.com', email_verified: false}}),
    read('/', {uid: 'u1', claims: {email: 'ann@elsewhere.com', email_verified: true}}),
    read('/', {uid: 'u1', claims: {email: 'ann@example.computer', email_verified: true}}),
    read('/'), read('/guests', {uid: 'g', claims: {email: 'g@gmail.com'}}),
    set('/staff/u1/email', 'Ann@Example.com', {uid: 'u1', claims: {email: 'ann@example.com'}}),
    set('/staff/u1/email', 'bob@example.com', {uid: 'u1', claims: {email: 'ann@example.com'}}),
    set('/staff/u2/email', 'x@gmail.com', {uid: 'u2', claims: {email: 'x@gmail.com'}}),
    set('/guests/g', {name: 'G'}, {uid: 'g', claims: {email: 'g@gmail.com'}}),
    set('/guests/g', {name: 'G'}, {uid: 'g', claims: {email: 'g@example.com'}}),
    set('/guests/g', {name: 'G'}, u1),
  ]],
  ['10-presence.rules.json', {status: {u1: {state: 'online', last_changed: past}}}, [
    read('/status'), read('/status', u2), set('/status/u1', {state: 'offline', last_changed: past}, u1),
    set('/status/u1', {state: 'away', last_changed: past}, u1), set('/status/u1', {state: 'offline', last_changed: future}, u1),
    set('/status/u1', {state: 'offline'}, u1), set('/status/u2', {state: 'online', last_changed: past}, u1),
    odSet('/status/u1', {state: 'offline', last_changed: past}, u1), odSet('/status/u1', {state: 'gone', last_changed: past}, u1),
    odSet('/status/u2', {state: 'offline', last_changed: past}, u1), odUpdate('/status/u1', {state: 'offline'}, u1),
    odUpdate('/status/u1', {state: 'zzz'}, u1), {op: 'onDisconnectCancel', path: '/status/u1', auth: u1},
    set('/connections/u1/c1', true, u1), set('/connections/u1/c1', false, u1), odSet('/connections/u1/c1', null, u1),
    del('/connections/u1/c1', u1), read('/connections/u1', u1), read('/connections/u1', u2),
  ]],
  ['11-leaderboard.rules.json', {scores: {u1: 10, u2: 30}, likes: {p1: 5}, stats: {sum: 10, count: 4, average: 2.5}}, [
    read('/scores'), read('/scores', null, {orderBy: 'value', limitToLast: 3}), set('/scores/u1', 11, u1), set('/scores/u1', 9, u1),
    set('/scores/u1', 10.5, u1), set('/scores/u3', 1, u3), set('/scores/u2', 99, u1), set('/likes/p1', 6, u1),
    set('/likes/p1', 4, u1), set('/likes/p1', 7, u1), set('/likes/p2', 1, u1), set('/likes/p2', 2, u1), set('/likes/p1', 6),
    del('/likes/p1', u1), set('/stats/average', 2.5, u1), set('/stats/average', 3, u1),
  ]],
  ['12-marketplace-queries.rules.json', {listings: {l1: {owner: 'u1', price: 5, public: true}, l2: {owner: 'u2', price: 9}}, feed: {f1: 1}}, [
    read('/listings', u1), read('/listings', u1, {orderBy: 'owner', equalTo: 'u1'}), read('/listings', u1, {orderBy: 'owner', equalTo: 'u2'}),
    read('/listings', null, {orderBy: 'owner', equalTo: 'u1'}), read('/listings/l1'), read('/listings/l2'),
    read('/feed', null, {orderBy: 'key', limitToLast: 20}), read('/feed', null, {orderBy: 'key', limitToLast: 100}), read('/feed'),
    set('/listings/l3', {owner: 'u2', price: 3}, u2), set('/listings/l3', {owner: 'u1', price: 3}, u2), set('/listings/l3', {owner: 'u2', price: 'free'}, u2),
    set('/listings/l1/price', 6, u1), set('/listings/l1/price', 6, u2), del('/listings/l1', u1),
  ]],
  ['13-email-whitelist.rules.json', {whitelist: {'fred@gmail%2Ecom': true}}, [
    set('/users/fred', {email: 'fred@gmail.com'}, {uid: 'fred'}), set('/users/fred', {email: 'barney@aol.com'}, {uid: 'fred'}),
    set('/users/fred', {email: 'fred@gmail.com'}, {uid: 'other'}), set('/users/fred', {name: 'no email'}, {uid: 'fred'}),
    set('/users/fred', {email: 'fred@gmail%2Ecom'}, {uid: 'fred'}), read('/users/fred'), read('/whitelist'),
  ]],
  ['14-tic-tac-toe.rules.json', {games: {g1: {players: {x: 'u1', o: 'u2'}, turn: 'x', board: {}, moves: 0}}}, [
    read('/games/g1', u1), read('/games/g1', u3), set('/games/g2/players', {x: 'u1', o: 'u3'}, u1),
    set('/games/g2/players', {x: 'u1', o: 'u1'}, u1), set('/games/g1/players', {x: 'u3', o: 'u2'}, u3),
    set('/games/g1/turn', 'o', u1), set('/games/g1/turn', 'o', u2), set('/games/g1/turn', 'x', u1),
    set('/games/g1/board/c5', 'x', u1), set('/games/g1/board/c5', 'o', u2), set('/games/g1/board/c5', 'x', u2),
    set('/games/g1/moves', 1, u1), set('/games/g1/moves', 2, u1), update('/games/g1', {'board/c1': 'x', turn: 'o', moves: 1}, u1),
  ]],
  ['15-uploads-metadata.rules.json', {uploads: {u1: {f1: {name: 'cat.png', size: 100, contentType: 'image/png'}}}}, [
    read('/uploads/u1', u1), read('/uploads/u1', u2),
    set('/uploads/u1/f2', {name: 'my dog.JPG', size: 2048, contentType: 'image/jpeg'}, u1),
    set('/uploads/u1/f2', {name: 'evil.exe', size: 2048, contentType: 'image/jpeg'}, u1),
    set('/uploads/u1/f2', {name: 'big.png', size: 6 * 1024 * 1024, contentType: 'image/png'}, u1),
    set('/uploads/u1/f2', {name: 'doc.png', size: 10, contentType: 'application/pdf'}, u1),
    set('/uploads/u1/f2', {name: 'a.gif', size: 10, contentType: 'image/gif', path: 'images/u1/f2'}, u1),
    set('/uploads/u1/f2', {name: 'a.gif', size: 10, contentType: 'image/gif', path: 'images/u2/f2'}, u1),
    set('/uploads/u1/f2', {name: 'a.gif', size: 10, contentType: 'image/gif', owner: 'u1'}, u1),
    set('/uploads/u1/f1/size', 0, u1), del('/uploads/u1/f1', u1), set('/uploads/u1/f2', {name: 'x.png', size: 1, contentType: 'image/png'}, u2),
  ]],
  ['16-multi-tenant.rules.json', {tenants: {t1: {settings: {plan: 'pro'}, projects: {p1: {tenant: 't1', name: 'One'}}}}}, [
    read('/tenants/t1', {uid: 'a', claims: {tenantId: 't1'}}), read('/tenants/t1', {uid: 'b', claims: {tenantId: 't2'}}), read('/tenants/t1', u1),
    set('/tenants/t1/projects/p2', {tenant: 't1', name: 'Two'}, {uid: 'a', claims: {tenantId: 't1', firebase: {sign_in_provider: 'password'}}}),
    set('/tenants/t1/projects/p2', {tenant: 't1', name: 'Two'}, {uid: 'a', claims: {tenantId: 't1', firebase: {sign_in_provider: 'anonymous'}}}),
    set('/tenants/t1/projects/p2', {tenant: 't2', name: 'Two'}, {uid: 'a', claims: {tenantId: 't1', firebase: {sign_in_provider: 'password'}}}),
    set('/tenants/t1/projects/p2', {tenant: 't1', name: 'Two'}, {uid: 'a', claims: {tenantId: 't1'}}),
    set('/tenants/t1/settings/plan', 'free', {uid: 'a', claims: {tenantId: 't1', roleMap: {t1: 'owner'}, firebase: {sign_in_provider: 'anonymous'}}}),
    set('/tenants/t1/settings/plan', 'free', {uid: 'a', claims: {tenantId: 't1', roleMap: {t1: 'member'}}}),
    set('/tenants/t1/settings/plan', 'free', {uid: 'a', claims: {tenantId: 't1'}}),
  ]],
  ['17-social-graph.rules.json', {users: {u1: {name: 'A'}, u2: {name: 'B'}}, friends: {u1: {}}, requests: {u2: {u1: past}}}, [
    read('/users', u1), read('/friends/u1', u1), read('/friends/u1', u2), read('/requests/u2/u1', u2), read('/requests/u2/u1', u3),
    update('/', {'friends/u1/u2': true, 'friends/u2/u1': true, 'requests/u2/u1': null}, u2),
    update('/', {'friends/u1/u2': true}, u2), update('/', {'friends/u1/u3': true, 'friends/u3/u1': true}, u1),
    update('/', {'friends/u1/u1': true}, u1), set('/requests/u1/u3', past, u3), set('/requests/u1/u2', 'hi', u2),
    set('/users/u1/name', 'Z', u2), set('/users/u1/name', 'Z', u1),
  ]],
  ['18-orders.rules.json', {orders: {o1: {customer: 'u1', items: {i1: {sku: 'A', qty: 1}}, total: 9.5, status: 'pending'}}}, [
    read('/orders/o1', u1), read('/orders/o1', u2), read('/orders/o1', {uid: 's', claims: {staff: true}}),
    read('/orders', {uid: 's', claims: {staff: true}}, {orderBy: 'status', equalTo: 'paid'}),
    set('/orders/o2', {customer: 'u2', items: {i1: {sku: 'B', qty: 2}}, total: 3, status: 'pending'}, u2),
    set('/orders/o2', {customer: 'u2', items: {i1: {sku: 'B', qty: 0}}, total: 3, status: 'pending'}, u2),
    set('/orders/o2', {customer: 'u2', items: {}, total: 3, status: 'pending'}, u2),
    set('/orders/o2', {customer: 'u2', items: {i1: {sku: 'B', qty: 2}}, total: 3, status: 'shipped'}, u2),
    set('/orders/o2', {customer: 'u1', items: {i1: {sku: 'B', qty: 2}}, total: 3, status: 'pending'}, u2),
    set('/orders/o1/status', 'paid', {uid: 's', claims: {staff: true}}), set('/orders/o1/status', 'lost', {uid: 's', claims: {staff: true}}),
    set('/orders/o1/status', 'paid', u1), set('/orders/o1/note', 'leave at door', u1),
    set('/orders/o1/note', 'leave at door', {uid: 's', claims: {staff: true}}), del('/orders/o1/items/i1', {uid: 's', claims: {staff: true}}),
  ]],
  ['19-polls.rules.json', {
    polls: {open: {question: 'Q?', closesAt: future, options: {a: true, b: true}}, closed: {question: 'Old?', closesAt: past, options: {a: true}}},
    votes: {open: {u2: 'a'}},
  }, [
    read('/polls'), read('/votes/open'), set('/votes/open/u1', 'a', u1), set('/votes/open/u1', 'c', u1),
    set('/votes/open/u1', 'a', u2), set('/votes/open/u2', 'b', u2), set('/votes/closed/u1', 'a', u1), del('/votes/open/u2', u2),
    set('/polls/new', {question: 'New?', closesAt: future}, u1), set('/polls/new', {question: 'New?', closesAt: past}, u1),
    set('/polls/open/question', 'Changed?', u1),
  ]],
  ['20-bolt-generated.rules.json', {products: {p1: {name: 'Lamp', price: 20, tags: {t1: 'home'}}}}, [
    read('/products/p1'), read('/products'),
    set('/products/p2', {name: 'Desk', price: 100, tags: {t1: 'office'}}, u1), set('/products/p2', {name: 'Desk', price: 100}, u1),
    set('/products/p2', {name: 'Desk', price: -1, tags: {t1: 'office'}}, u1),
    set('/products/p2', {name: 'Desk', price: 100, tags: {t1: 'office'}, discount: 10}, u1),
    set('/products/p2', {name: 'Desk', price: 100, tags: {t1: 'office'}, discount: '15%'}, u1),
    set('/products/p2', {name: 'Desk', price: 100, tags: {t1: 'office'}, discount: true}, u1),
    set('/products/p2', {name: 'Desk', price: 100, tags: {t1: 'office'}, color: 'red'}, u1),
    set('/products/p2', {name: 'Desk', price: 100, tags: {t1: 'a-very-long-tag-name-indeed'}}, u1),
    set('/products/p1/price', 25, u1), set('/products/p1/price', 25, {uid: 'boss', claims: {admin: true}}),
    set('/products/p2', {name: 'Desk', price: 100, tags: {t1: 'office'}}),
  ]],
];

export default {
  groups: apps.map(([file, data, checks]) => ({name: file, rulesText: rulesText(file), data, checks})),
};
