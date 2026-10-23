// .validate at the nodes a write leaves null, replayed against the emulator by capture.mjs.
// Verdicts land in nulls.golden.json; TestEmulatorNulls makes the Go engine agree with them.
//
// Firebase skips .validate wherever the new data is null and runs it everywhere else a write
// reaches: nulling a validated child in a multi-path update is allowed, removing a child its
// parent requires is denied. Lark 0.3.0 had both the other way round (its CHANGELOG, "Fixed");
// these cases check that Aruma does not.
const groups = [];

const set = (path, value) => ({op: 'set', path, value});
const update = (path, value) => ({op: 'update', path, value});
const del = (path) => ({op: 'delete', path});
const odSet = (path, value) => ({op: 'onDisconnectSet', path, value});
const odUpdate = (path, value) => ({op: 'onDisconnectUpdate', path, value});

// A record with two required fields and an optional one, each with its own .validate.
groups.push({
  name: 'required fields',
  rules: {rules: {items: {$id: {
    '.write': true,
    '.validate': "newData.hasChildren(['a', 'b'])",
    a: {'.validate': 'newData.isString()'},
    b: {'.validate': 'newData.isNumber()'},
    c: {'.validate': 'newData.isBoolean()'},
    $other: {'.validate': false},
  }}}},
  data: {items: {x: {a: 's', b: 1, c: true}, y: {a: 't', b: 2}}},
  checks: [
    // Nulling a validated optional child: allowed, alone or with other children.
    update('/items/x', {c: null}),
    update('/', {'items/x/c': null}),
    update('/', {'items/x/c': null, 'items/y/c': true}),
    update('/items/x', {a: 'new', c: null}),
    del('/items/x/c'),
    set('/items/x/c', null),
    set('/items/x/c', {}),
    set('/items/x', {a: 's', b: 1, c: null}),
    // Removing a required child leaves the parent invalid: denied, however it is written.
    update('/items/x', {a: null}),
    update('/', {'items/x/a': null}),
    update('/', {'items/x/a': null, 'items/y/c': true}),
    del('/items/x/a'),
    set('/items/x/a', null),
    set('/items/x', {a: null, b: 1}),
    // Removing the whole record, directly or child by child: allowed.
    del('/items/x'),
    update('/items', {x: null}),
    update('/', {'items/x/a': null, 'items/x/b': null, 'items/x/c': null}),
    update('/', {'items/x': null, 'items/z': {a: 'u', b: 3}}),
    update('/', {'items/x': null, 'items/z': {a: 'u'}}),
    // A null below a child that $other refuses.
    update('/items/x', {d: null}),
    update('/items/x', {d: 1}),
    // Update keys as clients may spell them: with a leading, trailing or double slash.
    update('/', {'/items/x/c/': null}),
    update('/', {'/items/x/a/': null}),
    update('/', {'items//x/a': null}),
    // onDisconnect: checked when registered, against the stored data.
    odSet('/items/x/c', null),
    odSet('/items/x/a', null),
    odUpdate('/items/x', {c: null}),
    odUpdate('/items/x', {a: null}),
  ],
});

// .validate false: no value may be written, but deleting is always allowed.
groups.push({
  name: 'validate false',
  rules: {rules: {locked: {'.write': true, $k: {'.validate': false}}}},
  data: {locked: {k: 1, j: 2}},
  checks: [
    del('/locked/k'),
    update('/locked', {k: null}),
    update('/', {'locked/k': null, 'locked/j': null}),
    set('/locked/k', 2),
    update('/', {'locked/k': null, 'locked/n': 1}),
    set('/locked', {k: null}),
    set('/locked', null),
  ],
});

// A .validate on the parent runs only while the parent survives the write.
groups.push({
  name: 'validate false on the parent',
  rules: {rules: {v: {'.write': true, '.validate': false}}},
  data: {v: {a: 1, b: 2}},
  checks: [
    del('/v'),
    del('/v/a'),
    update('/v', {a: null}),
    update('/v', {a: null, b: null}),
    update('/', {'v/a': null, 'v/b': null}),
    set('/v/a', 3),
  ],
});

// A .validate that tests for null never sees it: a delete skips it, a value fails it.
groups.push({
  name: 'validate only null',
  rules: {rules: {w: {$k: {'.write': true, '.validate': '!newData.exists()'}}}},
  data: {w: {a: 1}},
  checks: [
    del('/w/a'),
    update('/w', {a: null}),
    update('/w', {a: null, b: null}),
    set('/w/a', 1),
    update('/w', {a: null, b: 1}),
  ],
});

// Nested: deleting a grandchild that the child requires, while the parent requires the child.
groups.push({
  name: 'nested required',
  rules: {rules: {p: {
    '.write': true,
    '.validate': "newData.hasChild('q')",
    q: {'.validate': "newData.hasChild('r')", r: {'.validate': 'newData.isNumber()'}, s: {'.validate': 'newData.isNumber()'}},
  }}},
  data: {p: {q: {r: 1, s: 2}}},
  checks: [
    del('/p/q/s'),
    del('/p/q/r'),
    update('/p/q', {r: null}),
    update('/', {'p/q/r': null, 'p/q/s': null}),
    del('/p/q'),
    del('/p'),
    update('/', {'p/q': null, 'p/t': 1}),
  ],
});

export default {groups, loads: []};
