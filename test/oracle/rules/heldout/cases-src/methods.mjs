// Firestore methods: read = get + list, write = create + update + delete,
// what request.method and request.resource hold for each.
const DOC = 'DOC';

export const topic = 'methods';
export const service = 'firestore';
export const resource = DOC;

const m = (id, allow, method, expr = 'true', more = {}) => ({ id, desc: `allow ${allow}: if ${expr}; request ${method}`, allow, method, expr, ...more });
const NEW = { n: 2, s: 'new', owner: 'alice' };

export const cases = [
  m('read_get', 'read', 'get'),
  m('read_list', 'read', 'list', 'true', { resource: null }),
  m('get_list', 'get', 'list', 'true', { resource: null }),
  m('list_get', 'list', 'get'),
  m('write_create', 'write', 'create', 'true', { resource: null, data: NEW }),
  m('write_update', 'write', 'update', 'true', { data: NEW }),
  m('write_delete', 'write', 'delete', 'true', { data: null }),
  m('create_update', 'create', 'update', 'true', { data: NEW }),
  m('update_create', 'update', 'create', 'true', { resource: null, data: NEW }),
  m('delete_update', 'delete', 'update', 'true', { data: NEW }),
  m('read_create', 'read', 'create', 'true', { resource: null, data: NEW }),
  m('write_get', 'write', 'get'),
  m('get_list_both', 'get, list', 'list', 'true', { resource: null }),
  m('method_get', 'read', 'get', "request.method == 'get'"),
  m('method_list', 'read', 'list', "request.method == 'list'", { resource: null }),
  m('method_create', 'write', 'create', "request.method == 'create'", { resource: null, data: NEW }),
  m('method_update', 'write', 'update', "request.method == 'update'", { data: NEW }),
  m('method_delete', 'write', 'delete', "request.method == 'delete'", { data: null }),
  m('create_resource_null', 'create', 'create', 'resource == null', { resource: null, data: NEW }),
  m('create_request_resource', 'create', 'create', "request.resource.data.owner == request.auth.uid", { resource: null, data: NEW }),
  m('update_both', 'update', 'update', 'request.resource.data.n == resource.data.n + 1', { data: NEW }),
  m('update_keys_hasonly', 'update', 'update', "request.resource.data.diff(resource.data).affectedKeys().hasOnly(['n', 's'])", { data: { ...NEW, f: 1.5, b: true, nul: null, arr: [1, 2, 3], tags: ['a', 'b'], m: { x: 1, y: 'z' } } }),
  m('delete_request_resource_null', 'delete', 'delete', 'request.resource == null', { data: null }),
  m('delete_resource', 'delete', 'delete', "resource.data.owner == 'alice'", { data: null }),
  m('list_query_limit', 'list', 'list', 'request.query.limit <= 10', { resource: null }),
  m('unknown_method_word', 'fetch', 'get', 'true', { alone: true }),
  m('method_write_verb', 'write', 'write'),
  m('method_read_verb', 'read', 'read'),
  {
    id: 'list_query_in_request',
    desc: 'allow list: if request.query.limit <= 10; with request.query {limit: 5}',
    rules: `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /q/{doc} {
      allow list: if request.query.limit <= 10;
    }
  }
}
`,
    path: 'q/doc',
    method: 'list',
    resource: null,
    query: { limit: 5 },
  },
  {
    id: 'allow_without_condition',
    desc: 'allow get; with no condition',
    rules: `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /q/{doc} {
      allow get;
    }
  }
}
`,
    path: 'q/doc',
  },
  {
    id: 'no_allow_statement',
    desc: 'a match with no allow statement',
    rules: `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /q/{doc} {
    }
  }
}
`,
    path: 'q/doc',
  },
];

// Cited by docs/research/rules/A-oracle-findings.md: kept in the development set.
const PIN = new Set(['read_list', 'get_list', 'write_create', 'write_update', 'write_delete', 'create_update', 'method_write_verb', 'delete_request_resource_null', 'allow_without_condition']);
for (const c of cases) if (PIN.has(c.id)) c.pin = true;
