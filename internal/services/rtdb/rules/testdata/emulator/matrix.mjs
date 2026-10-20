// The rules × operations matrix replayed against the emulator by capture.mjs. Verdicts land in
// matrix.golden.json; rules_emulator_test.go makes the Go engine agree with them.
const groups = [];
const loads = [];

const u1 = {uid: 'u1'};
const u2 = {uid: 'u2'};
const bob = {uid: 'bob', claims: {email: 'bob@example.com', email_verified: true, n: 1, s: 'x', b: true, arr: ['x', 'y'], admin: true, nested: {k: 'v'}}};

// expr records whether expression e is true, false or fails: it reads /t under ".read": e and
// /f under ".read": "!(e)". A failure denies both. Data is seeded under both locations.
function expr(e, opts = {}) {
  const data = opts.data === undefined ? null : opts.data;
  const auth = opts.auth === undefined ? null : opts.auth;
  groups.push({
    name: 'expr: ' + e + (opts.label ? ` [${opts.label}]` : ''),
    rules: {rules: {t: {'.read': e}, f: {'.read': '!(' + e + ')'}}},
    data: data === null ? null : {t: data, f: data},
    checks: [{op: 'read', path: '/t', auth}, {op: 'read', path: '/f', auth}],
  });
}

// wexpr is expr for a .write rule, setting value at /t and /f.
function wexpr(e, value, opts = {}) {
  const data = opts.data === undefined ? null : opts.data;
  const auth = opts.auth === undefined ? null : opts.auth;
  groups.push({
    name: 'wexpr: ' + e + ' <- ' + JSON.stringify(value),
    rules: {rules: {t: {'.write': e}, f: {'.write': '!(' + e + ')'}}},
    data: data === null ? null : {t: data, f: data},
    checks: [{op: 'set', path: '/t', value, auth}, {op: 'set', path: '/f', value, auth}],
  });
}

// --- Operators, numbers and strings.
expr("1 < 'a'");
expr("'a' < 1");
expr("(1 + '') == '1'");
expr("(1.0 + '') == '1'");
expr("(0.5 + '') == '0.5'");
expr("(1e21 + '') == '1.0E21'");
expr("(123456789012 + '') == '123456789012'");
expr("(0.1 + 0.2 + '') == '0.30000000000000004'");
expr("(1e7 + '') == '10000000'");
expr("(1e-4 + '') == '1.0E-4'");
expr("(0.001 + '') == '0.001'");
expr("(12345678.5 + '') == '1.23456785E7'");
expr("(-0.0 + '') == '0'");
expr("(-5 + '') == '-5'");
expr("(1e19 + '') == '1.0E19'");
expr("(9.2e18 + '') == '9200000000000000000'");
expr("(1/3 + '') == '0.3333333333333333'");
expr("(1e308 * 10 + '') == 'Infinity'");
expr("(1/0 + '') == 'NaN'");
expr("(-1/0 + '') == 'NaN'");
expr("(5 % 0 + '') == 'NaN'");
expr("(1/0) == (1/0)");
expr("(1/0) > 2");
expr("(-7 % 3) == -1");
expr("(7 / 2) == 3.5");
expr("1 == 1.0");
expr("0x10 == 16");
expr(".5 == 0.5");
expr("1 == 1 == true");
expr("'a' + 1.5 == 'a1.5'");
expr("'B' < 'a'");
expr("'a' < 'ab'");
expr("'10' < '9'");
expr("'é' < 'z'");
expr("'￿' < '😀'");
expr("'abc'.length == 3");
expr("'é'.length == 1");
expr("'😀'.length == 2");
expr("'aXa'.replace('a', 'b') == 'bXb'");
expr("'abc'.replace('', '-') == '-a-b-c-'");
expr("'ABC'.toLowerCase() == 'abc'");
expr("'straße'.toUpperCase() == 'STRASSE'");
expr("'ﬁ'.toUpperCase() == 'FI'");
expr("'İ'.toLowerCase().length == 2");
expr("'ΣΑΣ'.toLowerCase() == 'σας'");
expr("'ǅ'.toUpperCase() == 'Ǆ'");
expr("'abc'.beginsWith('')");
expr("'abc'.contains('')");
expr("'abc'.endsWith('bc')");
expr("'abc'.endsWith('abcd')");
expr("'abc'.contains('bc') && 'abc'.beginsWith('ab')");
expr("'a\\'b' == \"a'b\"");
expr("'\\u0041' == 'A'");
expr("true || root.parent().exists()");
expr("false && root.parent().exists()");
expr("root.parent().exists() || true");
expr("(root.parent().exists() ? true : true)");
expr("(true ? true : root.parent().exists())");
expr("(true ? 1 : 'a') == 1");
expr("(false ? 1 : 'a') == 'a'");
expr("now > 1600000000000 && now < 4000000000000");
expr("data.exists() /* c */ && true", {data: 1});
// A read without query parameters: ordered by key over REST, by priority over the protocol.
expr('query.orderByKey == true');
expr('query.orderByPriority == true');
expr('query.orderByValue == false && query.orderByChild == null && query.startAt == null && query.limitToFirst == null');
expr("data.exists();", {data: 1});

// --- Regular expressions.
for (const [s, re] of [
  ['xabcx', '/abc/'], ['xabcx', '/^abc$/'], ['abc', '/^abc$/'], ['ABC', '/abc/i'], ['ÉÉ', '/^é+$/i'],
  ['a\\nb', '/a.b/'], ['a\\rb', '/a.b/'], ['a1b', '/a\\db/'], ['a_b', '/a\\wb/'], ['aéb', '/a\\wb/'],
  ['٣', '/^\\d$/'], ['aaa', '/^a{2,3}$/'], ['aaaa', '/^a{2,3}$/'], ['aa', '/^a{2}$/'], ['aaaaa', '/^a{2,}$/'],
  ['a{2}', '/a{2}/'], ['{foo}', '/\\{foo}/'], ['a.b', '/^a\\.b$/'], ['axb', '/^a\\.b$/'], ['a/b', '/a\\/b/'],
  ['ab', '/a(?=b)/'], ['a?=b', '/a(?=b)/'], ['ab', '/a(?:b)/'], ['a?:b', '/a(?:b)/'], ['aa', '/(a)\\1/'],
  ['a1', '/a\\1/'], ['ab', '/a\\bb/'], ['A', '/\\x41/'], ['anb', '/a\\nb/'], ['aaa', '/^a**$/'],
  ['aaa', '/^a+?$/'], ['', '/^a+?$/'], ['', '/()/'], ['+a', '/+a/'], ['*a', '/^*a/'], ['a,', '/^a,$/'],
  ['a}', '/^a}$/'], ['$', '/^\\$$/'], ['^', '/^\\^$/'], ['a-', '/^a[a-]$/'], [']', '/^[]a]$/'],
  ['-', '/^[\\d-z]$/'], ['y', '/^[\\d-z]$/'], ['5', '/^[\\d-z]$/'], ['b', '/^[^a]$/'], ['a', '/^[^a]$/'],
  ['A', '/^[a-z]$/i'], ['abc\\n', '/c$/'], ['x', '/^(a|b|x)$/'], ['fooBAR', '/^foo(bar)?$/i'],
  ['2024-01-31', '/^(19|20)[0-9][0-9][-\\/. ](0[1-9]|1[012])[-\\/. ](0[1-9]|[12][0-9]|3[01])$/'],
  ['bob@example.com', '/^[A-Z0-9._%+-]+@[A-Z0-9.-]+\\.[A-Z]{2,4}$/i'],
]) {
  expr(`'${s}'.matches(${re})`);
}
expr("'a b'.matches(/a\\sb/)");
expr("'a1b'.matches(/a\\Sb/)");

// --- Snapshots.
expr("data.val() == null", {data: {x: 1}});
expr("data.val() != null", {data: {x: 1}});
expr("data.val() == data.val()", {data: {x: 1}});
expr("data.val() == true", {data: {x: 1}});
expr("data.val().length > 0", {data: {x: 1}});
expr("data.val().contains('x')", {data: {x: 1}});
expr("data.val() + '' == 'x'", {data: {x: 1}});
expr("data.val() < 'b'", {data: {x: 1}});
expr("data.val() === 1", {data: 1.0});
expr("data.val() === true", {data: true});
expr("data.val() === 'x'", {data: 'x'});
expr("data.val().length == 1", {data: 'x'});
expr("data.val()['length'] == 1", {data: 'x'});
expr("data.child('missing').val().length == null", {data: {x: 1}});
expr("data.isString()", {data: {x: 1}});
expr("data.isString()", {data: 'x', label: 'leaf'});
expr("data.isNumber()", {data: 2.5});
expr("data.isBoolean()", {data: false});
expr("data.child('x').val() == 1", {data: {x: 1}});
expr("data.child('/x').exists()", {data: {x: 1}});
expr("data.child('x/').exists()", {data: {x: 1}});
expr("data.child('x//').exists()", {data: {x: 1}});
expr("data.child('').exists()", {data: {x: 1}});
expr("data.child('a.b').exists()", {data: {x: 1}});
expr("data.child('a.b').val() == null", {data: {x: 1}});
expr("data.child('a.b').parent().exists()", {data: {x: 1}});
expr("data.child('y/z').val() == 2", {data: {x: 1, y: {z: 2}}});
expr("data.child('x').child('deeper').exists()", {data: {x: 1}});
expr("data.hasChild('')", {data: {x: 1}});
expr("data.hasChild('a.b')", {data: {x: 1}});
expr("data.hasChild('x')", {data: {x: 1}});
expr("data.hasChildren([])", {data: {x: 1}});
expr("data.hasChildren()", {data: {x: 1}});
expr("data.hasChildren()", {data: 5, label: 'leaf'});
expr("data.hasChildren(['x'])", {data: {x: 1}});
expr("data.hasChildren(['x', 'y/z'])", {data: {x: 1, y: {z: 2}}});
expr("data.hasChildren(['x', 'q'])", {data: {x: 1}});
expr("data.hasChildren(['0', '1'])", {data: ['a', 'b']});
expr("data.parent().child('t').exists()", {data: {x: 1}});
expr("data.parent().parent().exists()", {data: {x: 1}});
expr("root.child('t').child('x').val() == 1", {data: {x: 1}});
expr("root.exists()", {data: 1});
expr("root.isString()", {data: 1});
expr("root['exists']()", {data: 1});

// --- auth: none, and an unsigned token with custom claims.
for (const a of [null, bob]) {
  const label = a ? 'token' : 'no auth';
  for (const e of [
    'auth == null', 'auth != null', "auth.uid == 'bob'", 'auth.uid == null', 'auth.token.email == null',
    "auth.token.sub == 'bob'", "auth.token.user_id == 'bob'", 'auth.token.n == 1', 'auth.token.n > 0',
    'auth.token.n + 1 == 2', 'auth.token.missing == null', 'auth.token.missing.x == null',
    'auth.token.missing.length == 3', "auth.token.arr[0] == 'x'", "auth.token.arr['1'] == 'y'",
    'auth.token.arr[5] == null', 'auth.token.arr.length == 2', "auth.token.nested.k == 'v'",
    "auth.token.nested['k'] == 'v'", 'auth.token.nested.k.length == 1', 'auth.token.nested.length == null',
    'auth.token == auth.token', 'auth.token.nested == null', 'auth.token.admin == true',
    "auth.token.email.matches(/@example\\.com$/)", 'auth.token.email_verified == true',
    "auth.token.email_verified == true && auth.token.email.matches(/.*@example.com$/)",
    'auth.token.firebase == null', 'auth.token.iat == 1', 'auth.uid.length == 3',
    "auth.token.b.contains('x')", "auth.token.n.contains('x')", 'auth.token.n.length == null',
    "auth.token.nested.contains('x')", "auth.token.nested + '' == 'x'", '-auth.token.s == 1',
    'auth.token.s - 1 == 1', 'auth.token.b == true ? true : false', "auth.uid.foo == null",
    "auth['uid'] == 'bob'", "auth.token['email'] == 'bob@example.com'", "auth.uid.beginsWith('b')",
    "auth.contains('b')", "root.child('users').child(auth.uid).exists()",
    "root.child('users/' + auth.uid).exists()",
  ]) {
    expr(e, {auth: a, label});
  }
}

// --- newData in write rules.
wexpr('newData.val() == 5', 5);
wexpr('newData.val() == null', {a: 1});
wexpr('newData.val() == data.val()', {a: 1}, {data: {a: 1}});
wexpr('newData.val().length > 0', {a: 1});
wexpr("newData.hasChildren(['0', '1'])", ['a', 'b']);
wexpr('newData.hasChildren()', ['a', 'b']);
wexpr("newData.child('1').val() == 'b'", ['a', 'b']);
wexpr("newData.child('0').isString()", ['a', 'b']);
wexpr('newData.isNumber()', 1.5);
wexpr('newData.isBoolean()', false);
wexpr('newData.exists()', null);
wexpr('!newData.exists()', null, {data: 1});
wexpr("newData.parent().child('t').val() == 1", 1);
wexpr("newData.parent().child('f').val() == 1", 1);
wexpr("root.child('t').exists()", 1, {data: 1});
wexpr('data.exists()', 1, {data: 1});
wexpr('newData.val() === data.val() + 1', 3, {data: 2});
wexpr('newData.val() === -(data.child(\'q\').val())', -4, {data: {q: 4}});
wexpr('newData.val() % 2 === 0', 6);
wexpr("newData.isNumber()", {'.value': 5, '.priority': 1});
wexpr("query.orderByKey == false && query.orderByPriority == false", 1);
wexpr("query.orderByPriority == true", 1);
wexpr("query.limitToFirst == null", 1);

// --- Cascade.
groups.push({
  name: 'cascade',
  rules: {rules: {a: {'.read': true, '.write': true, b: {'.read': false, '.write': false}}, c: {d: {'.read': true, '.write': true}}, x: {'.read': false, y: {'.read': true}}}},
  data: {a: {b: 1}, c: {d: 2}, x: {y: 3, z: 4}},
  checks: [
    {op: 'read', path: '/a/b'}, {op: 'read', path: '/a'}, {op: 'read', path: '/a/b/deep'},
    {op: 'read', path: '/c'}, {op: 'read', path: '/c/d'}, {op: 'read', path: '/c/d/e'},
    {op: 'read', path: '/'}, {op: 'read', path: '/x'}, {op: 'read', path: '/x/y'}, {op: 'read', path: '/x/z'},
    {op: 'read', path: '/nothing'},
    {op: 'set', path: '/a/b', value: 5}, {op: 'set', path: '/c', value: {d: 5}}, {op: 'set', path: '/c/d', value: 5},
    {op: 'set', path: '/c/d/e', value: 5}, {op: 'update', path: '/c', value: {d: 5}},
    {op: 'update', path: '/', value: {'c/d': 5, 'a/q': 1}}, {op: 'update', path: '/', value: {'c/d': 5, 'x/q': 1}},
    {op: 'update', path: '/', value: {}}, {op: 'update', path: '/x', value: {}},
    {op: 'delete', path: '/c'}, {op: 'delete', path: '/c/d'}, {op: 'delete', path: '/x/y'},
    {op: 'set', path: '/', value: {a: 1}}, {op: 'delete', path: '/'},
  ],
});
groups.push({
  name: 'write that fails above a grant',
  rules: {rules: {a: {'.write': 'root.parent().exists()', b: {'.write': true}}}},
  checks: [{op: 'set', path: '/a/b', value: 1}, {op: 'set', path: '/a', value: {b: 1}}],
});
groups.push({
  name: 'read that fails above a grant',
  rules: {rules: {a: {'.read': 'root.parent().exists()', b: {'.read': true}}}},
  data: {a: {b: 1}},
  checks: [{op: 'read', path: '/a/b'}, {op: 'read', path: '/a'}],
});

// --- Validate.
const widget = {'.write': true, widget: {'.validate': "newData.hasChildren(['color', 'size'])",
  size: {'.validate': 'newData.isNumber() && newData.val() >= 0 && newData.val() <= 99'},
  color: {'.validate': "root.child('valid_colors/' + newData.val()).exists()"}}};
groups.push({
  name: 'validate: the reference widget, none stored',
  rules: {rules: widget},
  data: {valid_colors: {red: true, blue: true}},
  checks: [
    {op: 'set', path: '/widget', value: 'foo'}, {op: 'set', path: '/widget', value: {size: 22}},
    {op: 'set', path: '/widget', value: {size: 99999, color: 'red'}}, {op: 'set', path: '/widget', value: {size: 'big', color: 'red'}},
    {op: 'set', path: '/widget', value: {size: 22, color: 'blue'}}, {op: 'set', path: '/widget', value: {size: 22, color: 'green'}},
    {op: 'set', path: '/widget/size', value: 99}, {op: 'delete', path: '/widget'},
  ],
});
groups.push({
  name: 'validate: the reference widget, one stored',
  rules: {rules: widget},
  data: {valid_colors: {red: true}, widget: {size: 1, color: 'red'}},
  checks: [
    {op: 'set', path: '/widget/size', value: 99}, {op: 'set', path: '/widget/size', value: 100},
    {op: 'delete', path: '/widget/size'}, {op: 'update', path: '/widget', value: {size: null}},
    {op: 'update', path: '/widget', value: {size: 5}}, {op: 'update', path: '/widget', value: {extra: 5}},
    {op: 'set', path: '/widget/extra', value: 5}, {op: 'delete', path: '/widget'},
    {op: 'set', path: '/widget/color', value: 'blue'}, {op: 'update', path: '/', value: {'widget/size': 3, 'valid_colors/blue': true, 'widget/color': 'blue'}},
    {op: 'onDisconnectSet', path: '/widget/size', value: 100}, {op: 'onDisconnectSet', path: '/widget/size', value: 50},
    {op: 'onDisconnectUpdate', path: '/widget', value: {size: 100}}, {op: 'onDisconnectUpdate', path: '/widget', value: {size: 7}},
    {op: 'onDisconnectSet', path: '/widget', value: null}, {op: 'onDisconnectCancel', path: '/widget'},
  ],
});
groups.push({
  name: 'validate: untouched stored children',
  rules: {rules: {'.write': true, a: {'$k': {'.validate': 'newData.val() < 10'}}}},
  data: {a: {x: 50}},
  checks: [
    {op: 'update', path: '/a', value: {y: 1}}, {op: 'set', path: '/a/y', value: 1},
    {op: 'set', path: '/a', value: {x: 50, y: 1}}, {op: 'set', path: '/a', value: {y: 1}},
    {op: 'update', path: '/a', value: {x: 50}}, {op: 'set', path: '/a/x', value: 50}, {op: 'update', path: '/', value: {'a/y': 1}},
    {op: 'update', path: '/a', value: {x: null, y: 2}},
  ],
});
groups.push({
  name: 'validate: an ancestor sees the overlay',
  rules: {rules: {'.write': true, p: {'.validate': "newData.child('n').val() == newData.child('m').val()"}}},
  data: {p: {n: 1, m: 1}},
  checks: [
    {op: 'set', path: '/p/n', value: 2}, {op: 'update', path: '/p', value: {n: 2, m: 2}},
    {op: 'update', path: '/', value: {'p/n': 2, 'p/m': 2}}, {op: 'update', path: '/', value: {'p/n': 2}},
    {op: 'delete', path: '/p/n'}, {op: 'delete', path: '/p'}, {op: 'update', path: '/p', value: {n: null, m: null}},
    {op: 'onDisconnectUpdate', path: '/p', value: {n: 3, m: 3}}, {op: 'onDisconnectSet', path: '/p/m', value: 9},
  ],
});
groups.push({
  name: 'validate: a write grant is still needed',
  rules: {rules: {a: {'.validate': true, b: {'.write': true}}}},
  checks: [
    {op: 'set', path: '/a', value: {b: 1}}, {op: 'set', path: '/a/b', value: 1},
    {op: 'update', path: '/a', value: {b: 1}}, {op: 'update', path: '/a', value: {b: 1, c: 1}},
  ],
});
groups.push({
  name: 'validate: $other false',
  rules: {rules: {'.write': true, w: {title: {'.validate': true}, '$other': {'.validate': false}}}},
  data: {w: {other: 0}},
  checks: [
    {op: 'set', path: '/w', value: {title: 'x'}}, {op: 'set', path: '/w', value: {title: 'x', other: 1}},
    {op: 'set', path: '/w/other', value: 1}, {op: 'set', path: '/w/other/deep', value: 1},
    {op: 'delete', path: '/w/other'}, {op: 'set', path: '/w', value: {title: {nested: 1}}},
    {op: 'update', path: '/w', value: {title: 'y', other: null}},
  ],
});
groups.push({
  name: 'validate: a leaf type on the parent',
  rules: {rules: {'.write': true, s: {'.validate': 'newData.isString()'}}},
  data: {s: 'x'},
  checks: [
    {op: 'set', path: '/s/child', value: 1}, {op: 'delete', path: '/s/child'}, {op: 'set', path: '/s', value: 1},
    {op: 'set', path: '/s', value: 'y'},
  ],
});
groups.push({
  name: 'validate: at the root',
  rules: {rules: {'.write': true, '.validate': "newData.hasChild('keep')"}},
  data: {keep: 1, other: 2},
  checks: [
    {op: 'set', path: '/other', value: 3}, {op: 'delete', path: '/keep'}, {op: 'delete', path: '/'},
    {op: 'set', path: '/', value: {x: 1}}, {op: 'update', path: '/', value: {keep: null, other: null}},
  ],
});
groups.push({
  name: 'validate: deep wildcards in an update',
  rules: {rules: {'.write': true, rooms: {'$room': {'$msg': {'.validate': "newData.hasChildren(['by', 'text']) && newData.child('by').val() == $room", text: {'.validate': 'newData.isString() && newData.val().length <= 5'}}}}}},
  data: {rooms: {r1: {m1: {by: 'r1', text: 'hi'}}}},
  checks: [
    {op: 'update', path: '/', value: {'rooms/r1/m2': {by: 'r1', text: 'yo'}}},
    {op: 'update', path: '/', value: {'rooms/r1/m2': {by: 'r2', text: 'yo'}}},
    {op: 'update', path: '/', value: {'rooms/r1/m2': {by: 'r1', text: 'toolong'}}},
    {op: 'update', path: '/', value: {'rooms/r1/m1/text': 'ok'}},
    {op: 'update', path: '/', value: {'rooms/r1/m1/text': null}},
    {op: 'update', path: '/rooms', value: {'r1/m1/by': 'r2'}},
    {op: 'set', path: '/rooms/r9/m1', value: {by: 'r9', text: 'a', extra: 1}},
    {op: 'update', path: '/', value: {'rooms/r1/m3': {by: 'r1', text: 'a'}, 'rooms/r2/m1': {by: 'zz', text: 'a'}}},
  ],
});

// --- Deletes and creates.
groups.push({
  name: 'create or delete only',
  rules: {rules: {'$k': {'.write': '!data.exists() || !newData.exists()', '.validate': 'newData.isNumber()'}}},
  data: {e: 1},
  checks: [
    {op: 'set', path: '/n', value: 1}, {op: 'set', path: '/n', value: 'x'}, {op: 'set', path: '/e', value: 2},
    {op: 'delete', path: '/e'}, {op: 'delete', path: '/missing'}, {op: 'update', path: '/', value: {e: null, n: 1}},
    {op: 'update', path: '/', value: {e: 3, n: 1}}, {op: 'set', path: '/e/x', value: 1}, {op: 'set', path: '/n', value: {}},
    {op: 'set', path: '/n', value: []}, {op: 'onDisconnectSet', path: '/e', value: null}, {op: 'onDisconnectSet', path: '/e', value: 5},
    {op: 'onDisconnectCancel', path: '/e'},
  ],
});
groups.push({
  name: 'unchanged writes still run rules',
  rules: {rules: {a: {'.write': 'newData.val() != data.val()'}}},
  data: {a: 1},
  checks: [{op: 'set', path: '/a', value: 1}, {op: 'set', path: '/a', value: 2}, {op: 'update', path: '/', value: {a: 1}}],
});
groups.push({
  name: 'onDisconnect under a closed write',
  rules: {rules: {'.read': true, a: {'.write': false}, b: {'.write': 'auth != null'}}},
  data: {a: 1, b: 1},
  checks: [
    {op: 'onDisconnectSet', path: '/a', value: 2}, {op: 'onDisconnectUpdate', path: '/a', value: {x: 1}},
    {op: 'onDisconnectCancel', path: '/a'}, {op: 'onDisconnectCancel', path: '/b'},
    {op: 'onDisconnectSet', path: '/b', value: 2}, {op: 'onDisconnectSet', path: '/b', value: 2, auth: u1},
    {op: 'onDisconnectCancel', path: '/b', auth: u1}, {op: 'onDisconnectCancel', path: '/zzz'},
  ],
});

// --- Wildcards.
groups.push({
  name: 'wildcards',
  rules: {rules: {users: {'$uid': {'.read': 'auth != null && auth.uid == $uid', '.write': 'auth.uid === $uid',
    posts: {'$pid': {'.write': "$pid.beginsWith('p') && $uid == auth.uid"}}}},
  n: {'$num': {'.read': "$num == '1'", '.write': '$num == 1'}},
  rooms: {'$room_id': {'.write': "root.child('room_names/' + $room_id).exists()"}}}},
  data: {users: {u1: {name: 'a'}, u2: {name: 'b'}}, n: {1: 'x'}, room_names: {lobby: 'Lobby'}},
  checks: [
    {op: 'read', path: '/users/u1', auth: u1}, {op: 'read', path: '/users/u2', auth: u1}, {op: 'read', path: '/users/u1'},
    {op: 'read', path: '/users', auth: u1}, {op: 'read', path: '/users/u1/name', auth: u1},
    {op: 'set', path: '/users/u1/name', value: 'z', auth: u1}, {op: 'set', path: '/users/u2/name', value: 'z', auth: u1},
    {op: 'set', path: '/users/u1', value: null, auth: u1}, {op: 'set', path: '/users/u1', value: {name: 'x'}},
    {op: 'set', path: '/users/u2/posts/p1', value: 'x', auth: u2}, {op: 'set', path: '/users/u1/posts/q1', value: 'x', auth: u2},
    {op: 'read', path: '/n/1'}, {op: 'set', path: '/n/1', value: 'y'},
    {op: 'set', path: '/rooms/lobby', value: {topic: 'hi'}}, {op: 'set', path: '/rooms/kitchen', value: {topic: 'hi'}},
    {op: 'update', path: '/', value: {'users/u1/a': 1, 'users/u2/a': 1}, auth: u1},
    {op: 'update', path: '/users', value: {'u1/a': 1, 'u1/b': 2}, auth: u1},
  ],
});
groups.push({
  name: 'a shadowed wildcard',
  rules: {rules: {'$a': {'$a': {'.read': "$a == 'inner'"}}}},
  data: {outer: {inner: 1}},
  checks: [{op: 'read', path: '/outer/inner'}, {op: 'read', path: '/inner/outer'}],
});
groups.push({
  name: 'a named child beats the wildcard',
  rules: {rules: {list: {'$id': {'.read': false, '.write': false}, special: {'.read': true, '.write': true}}}},
  data: {list: {special: 1, other: 2}},
  checks: [{op: 'read', path: '/list/special'}, {op: 'read', path: '/list/other'}, {op: 'set', path: '/list/special', value: 3}],
});

// --- root and newData.parent().
groups.push({
  name: 'root is the stored data',
  rules: {rules: {a: {'.write': "root.child('a').val() == 1 && newData.val() == 2 && data.val() == 1"},
    b: {'.write': "newData.parent().child('a').val() == 1"}, c: {'.write': "newData.parent().child('b').val() == 7"}}},
  data: {a: 1},
  checks: [
    {op: 'set', path: '/a', value: 2}, {op: 'set', path: '/b', value: 5}, {op: 'update', path: '/', value: {b: 7, c: 1}},
    {op: 'update', path: '/', value: {c: 1}}, {op: 'update', path: '/', value: {a: 2, b: 1}},
  ],
});
groups.push({
  name: 'root.child lookups for membership',
  rules: {rules: {groups: {'$g': {'.read': "root.child('members/' + $g + '/' + auth.uid).val() === true",
    '.write': "root.child('admins').child(auth.uid).exists()"}}}},
  data: {members: {g1: {u1: true, u2: false}}, admins: {u2: true}, groups: {g1: {name: 'one'}}},
  checks: [
    {op: 'read', path: '/groups/g1', auth: u1}, {op: 'read', path: '/groups/g1', auth: u2}, {op: 'read', path: '/groups/g1'},
    {op: 'set', path: '/groups/g1/name', value: 'x', auth: u2}, {op: 'set', path: '/groups/g1/name', value: 'x', auth: u1},
    {op: 'set', path: '/groups/g1/name', value: 'x'},
  ],
});

// --- hasChildren with arrays.
groups.push({
  name: 'hasChildren with arrays',
  rules: {rules: {'.write': true, l: {'.validate': "newData.hasChildren(['0','1'])"}, m: {'.validate': 'newData.hasChildren()'}}},
  checks: [
    {op: 'set', path: '/l', value: ['a', 'b']}, {op: 'set', path: '/l', value: ['a']}, {op: 'set', path: '/l', value: {0: 'a', 1: 'b'}},
    {op: 'set', path: '/m', value: []}, {op: 'set', path: '/m', value: {}}, {op: 'set', path: '/m', value: 'leaf'},
    {op: 'set', path: '/m', value: [null, 'b']}, {op: 'set', path: '/l', value: [null, 'b']},
  ],
});

// --- Comments-style rule from the reference.
groups.push({
  name: 'reference: comments',
  rules: {rules: {'.read': true, '$comment': {'.write': "!data.exists() && newData.child('user_id').val() == auth.uid"}}},
  data: {c1: {user_id: 'u1'}},
  checks: [
    {op: 'set', path: '/c2', value: {user_id: 'u1'}, auth: u1}, {op: 'set', path: '/c2', value: {user_id: 'u2'}, auth: u1},
    {op: 'set', path: '/c1', value: {user_id: 'u1'}, auth: u1}, {op: 'set', path: '/c2', value: {user_id: 'u1'}},
  ],
});

// --- Query rules.
groups.push({
  name: 'query rules',
  rules: {rules: {q: {'.read': 'query.orderByChild == "s" && query.limitToFirst <= 2', '.indexOn': ['s']},
    k: {'.read': 'query.orderByKey && query.startAt == "b"'}, v: {'.read': 'query.equalTo == 3 && query.orderByValue', '.indexOn': '.value'},
    l: {'.read': 'query.limitToLast == 1 && query.orderByPriority'}, e: {'.read': "query.endAt == 'm'"}}},
  data: {q: {a: {s: 1}, b: {s: 2}}, k: {a: 1, b: 2}, v: {a: 3}, l: {a: 1}, e: {a: 1}},
  checks: [
    {op: 'read', path: '/q'}, {op: 'read', path: '/q', query: {orderBy: 's', limitToFirst: 2}},
    {op: 'read', path: '/q', query: {orderBy: 's', limitToFirst: 3}}, {op: 'read', path: '/q', query: {orderBy: 's'}},
    {op: 'read', path: '/k', query: {orderBy: 'key', startAt: 'b'}}, {op: 'read', path: '/k', query: {orderBy: 'key', startAt: 'a'}},
    {op: 'read', path: '/v', query: {orderBy: 'value', equalTo: 3}}, {op: 'read', path: '/l', query: {orderBy: 'priority', limitToLast: 1}},
    {op: 'read', path: '/e', query: {orderBy: 'key', endAt: 'm'}}, {op: 'read', path: '/e', query: {orderBy: 'key', endAt: 'n'}},
  ],
});

// --- Documents: what loads and what does not.
const doc = (name, d) => loads.push({name, doc: typeof d === 'string' ? d : JSON.stringify(d)});
const rule = (kind, e) => doc(`${kind}: ${e}`, {rules: {'$x': {[kind]: e}}});
for (const e of [
  'auth.token.admin', 'auth.token.admin || false', 'auth.token.admin == true', '!auth.token.admin',
  'data.val()', 'auth != ', "$y == 'a'", "$x == 'a'", 'auth.foo ? true : false', 'data.val() ? true : false',
  "root.child('a').val() > 1", 'data.val() + 1 > 2', "'a' + 1 == 'a1'", 'data.isObject()', 'auth.uid.length() > 1',
  'data.numChildren() > 1', 'data.val().foo == 1', "data.val()['length'] == 1", "true && 'a'",
  'auth.token.email.matches(/a{2,3}/)', 'auth.token.email.matches(/a{/)', 'auth.token.email.matches(/a**/)',
  'auth.token.email.matches(/a/g)', 'auth.token.email.matches(/a/m)', 'auth.token.email.matches(/[a-]/)',
  'auth.token.email.matches(/a$|b/)', 'auth.token.email.matches(/^a|^b/)', 'auth.token.email.matches(/()/)',
  'auth.token.email.matches(/^(a|)$/)', 'auth.token.email.matches(/é+/)', '1 == 1 == true', "-'a' == 1",
  '--1 == 1', '!!true', '+1 == 1', '1 === 1 && 2 !== 3', '"a" == \'a\'', '1.5e3 == 1500', '0x10 == 16',
  '.5 == 0.5', 'null == null', '[1,2] == null', 'root.hasChildren([])', "root.hasChildren(['a'], ['b'])",
  'root.hasChild()', 'root.child().exists()', "root.child('a', 'b').exists()", 'root.child(1).exists()',
  'root.parent(1).exists()', 'root.val(1) == null', 'now > 0', 'now() > 0',
  "auth.token.firebase.identities['google.com'][0] == 'x'", "auth['uid'] == 'x'", "auth[$x] == 'x'",
  "'abc'[0] == 'a'", "'abc'.length == 3", "'abc'.length() == 3", "'abc'.toUpperCase == 'ABC'",
  "auth.uid.replace('a') == 'x'", 'auth.uid.contains(1)', "auth.uid.matches('a')", 'auth.uid.matches(auth.re)',
  "data.child('a') == data.child('b')", 'data == null', 'auth == null', 'auth.token == auth.token', 'query == null',
  'true ? 1 : 2', '(true ? 1 : 2) == 1', "(true ? 1 : 'a') == 1", '(true ? data : root).exists()', "true ? true : 'a'",
  'auth.x > 1', 'auth.x > true', 'null > 1', 'null < null', "'a' < 'b'", "data.val() < 'b'", "1 < 'a'",
  'true < false', '1 + true == 2', '1 + null == 2', "'a' + null == 'anull'", "1 - 'a' == 2", 'auth.x - 1 == 2',
  '-data.val() == 2', '-auth.x == 2', '!data.val()', '!auth.x', '!null', '1', 'null', 'auth.x', 'data.exists();',
  'data.exists() // c', 'data.exists() /* c */ && true', 'foo(1)', 'this.x', 'typeof auth == \'object\'',
  'auth instanceof Object', 'auth.uid in data', 'a = 1', 'data.exists() , true', 'true & false', 'true | false',
  '1 << 2 == 4', '~1 == -2', 'void 0 == null', '`a` == \'a\'', 'a\nb', 'data.exists()\n&& true',
  'auth.provider == \'x\'', "auth['provider'] == 'x'", 'auth.token.provider == \'x\'', 'data.getPriority() == null',
  "query.orderByChild == 'a'", 'query.foo == 1', 'query.orderByKey', 'newData.exists()',
  "auth.token.email.matches(/a\\sb/)", "auth.token.email.matches(/a(?=b)/)", "auth.token.email.matches(/a||b/)",
  "auth.token.email.matches(/[z-a]/)", "auth.token.email.matches(/a{2000}/)", "auth.token.email.matches(/\\/)",
]) {
  rule('.read', e);
}
for (const e of ['newData.exists()', "query.orderByChild == 'a'", 'newData.val() == null']) {
  rule('.write', e);
  rule('.validate', e);
}
doc('bool rule', {rules: {'.read': true}});
doc('string rule', {rules: {'.read': 'true'}});
doc('number rule', {rules: {'.read': 1}});
doc('null rule', {rules: {'.read': null}});
doc('array rule', {rules: {'.read': [true]}});
doc('unknown dot key', {rules: {'.foo': true}});
doc('two wildcards', {rules: {'$a': {}, '$b': {}}});
doc('nested same wildcard', {rules: {'$a': {'$a': {}}}});
doc('child is not an object', {rules: {a: true}});
doc('empty child', {rules: {a: {}}});
doc('indexOn string', {rules: {'.indexOn': 'a'}});
doc('indexOn array', {rules: {'.indexOn': ['a', 'b']}});
doc('indexOn number in array', {rules: {'.indexOn': [1]}});
doc('indexOn number', {rules: {'.indexOn': 1}});
doc('indexOn path', {rules: {'.indexOn': 'a/b'}});
doc('indexOn .value', {rules: {'.indexOn': '.value'}});
doc('indexOn empty string', {rules: {'.indexOn': ''}});
doc('indexOn empty array', {rules: {'.indexOn': []}});
doc('validate at root', {rules: {'.validate': 'newData.exists()'}});
doc('extra top-level key', '{"rules":{},"x":1}');
doc('no rules key', '{"x":{}}');
doc('empty object', '{}');
doc('array document', '[]');
doc('rules is not an object', '{"rules":true}');
doc('trailing comma', '{"rules":{".read":true,}}');
doc('trailing comment', '{"rules":{".read":true}} // c');
doc('leading comment', '// head\n{"rules":{".read":true}}');
doc('block comment', '{"rules":{/* c */".read":true}}');
doc('duplicate rule', '{"rules":{".read":true,".read":false}}');
doc('duplicate child', '{"rules":{"a":{},"a":{}}}');
doc('key with a dot', '{"rules":{"a.b":{".read":true}}}');
doc('key with a slash', '{"rules":{"a/b":{".read":true}}}');
doc('key with a hash', '{"rules":{"a#b":{".read":true}}}');
doc('key with a dollar', '{"rules":{"a$b":{".read":true}}}');
doc('empty key', '{"rules":{"":{".read":true}}}');
doc('lone dollar', '{"rules":{"$":{".read":true}}}');
doc('wildcard with a dash', '{"rules":{"$a-b":{".read":"$a-b == \'x\'"}}}');
doc('second rule broken', '{"rules":{".read":"auth != null",  ".write":"auth.uid ==  "}}');
doc('single-quoted key', '{\'rules\':{".read":true}}');
doc('capital True', '{"rules":{".read":True}}');
doc('unicode escape in a rule', '{"rules":{".read":"\\u0074rue"}}');
doc('escaped quote in a rule', '{"rules":{".read":"data.child(\'a\').val() == \\"x\\""}}');
doc('multi-line rules file', '{\n  "rules": {\n    ".read": "auth != null",\n    "a": {\n      ".write": "newData.val() =="\n    }\n  }\n}');
doc('two bad rules', '{"rules":{".read":"foo","a":{".write":"bar"}}}');

export default {groups, loads};
