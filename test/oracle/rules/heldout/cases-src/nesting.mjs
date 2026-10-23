// Nested match blocks and recursive wildcards: a match nested in a block
// whose own path ends in {x=**} (the glob is final in its block) against one
// nested in a block whose glob is followed by more segments (non-final);
// rules_version 1 and 2; one glob per path; what the glob binds. Added after
// review: the first round only had nesting under a final glob. All cited by
// the findings, so all pinned to the development set.
const DOC = 'DOC';

export const topic = 'nesting';
export const service = 'firestore';
export const resource = DOC;

const fs = (version, body) => `${version ? `rules_version = '${version}';\n` : ''}service cloud.firestore {
  match /databases/{database}/documents {
${body}
  }
}
`;
const r = (id, desc, rules, path, more = {}) => ({ id, desc, rules, path, pin: true, ...more });

const nonFinalGroups = `    match /{g=**}/groups/{id} {
      match /members/{m} {
        allow get: if true;
      }
    }`;
const nonFinalPQ = `    match /p/{r=**}/q/{id} {
      match /docs/{d} {
        allow get: if true;
      }
    }`;
const finalP = `    match /p/{r=**} {
      match /docs/{d} {
        allow get: if true;
      }
    }`;
const flatGroups = `    match /{r=**}/groups/{g}/members/{m} {
      allow get: if true;
    }`;
const binding = (cond) => `    match /{document=**} {
      match /notes/{noteId} {
        allow get: if ${cond};
      }
    }`;

export const cases = [
  r('nonfinal_groups_top', 'v2: match /{g=**}/groups/{id} { match /members/{m} } on groups/g1/members/m1', fs('2', nonFinalGroups), 'groups/g1/members/m1'),
  r('nonfinal_groups_deep', 'v2: match /{g=**}/groups/{id} { match /members/{m} } on orgs/o1/groups/g1/members/m1', fs('2', nonFinalGroups), 'orgs/o1/groups/g1/members/m1'),
  r('nonfinal_groups_outer_doc', 'v2: the outer block itself, match /{g=**}/groups/{id} with allow get, on orgs/o1/groups/g1', fs('2', `    match /{g=**}/groups/{id} {
      allow get: if true;
    }`), 'orgs/o1/groups/g1'),
  r('nonfinal_pq', 'v2: match /p/{r=**}/q/{id} { match /docs/{d} } on p/a/q/x/docs/d1', fs('2', nonFinalPQ), 'p/a/q/x/docs/d1'),
  r('nonfinal_pq_zero', 'v2: match /p/{r=**}/q/{id} { match /docs/{d} } on p/q/x/docs/d1', fs('2', nonFinalPQ), 'p/q/x/docs/d1'),
  r('final_v2_zero', 'v2: match /p/{r=**} { match /docs/{d} } on p/docs/d1', fs('2', finalP), 'p/docs/d1'),
  r('final_v2_depth_1', 'v2: match /p/{r=**} { match /docs/{d} } on p/a/docs/d1', fs('2', finalP), 'p/a/docs/d1'),
  r('final_v2_depth_3', 'v2: match /p/{r=**} { match /docs/{d} } on p/a/b/c/docs/d1', fs('2', finalP), 'p/a/b/c/docs/d1'),
  r('final_v1_depth_1', "rules_version '1': match /p/{r=**} { match /docs/{d} } on p/a/docs/d1", fs('1', finalP), 'p/a/docs/d1'),
  r('final_noversion_depth_1', 'no rules_version: match /p/{r=**} { match /docs/{d} } on p/a/docs/d1', fs(null, finalP), 'p/a/docs/d1'),
  r('final_v1_depth_3', "rules_version '1': match /p/{r=**} { match /docs/{d} } on p/a/b/c/docs/d1", fs('1', finalP), 'p/a/b/c/docs/d1'),
  r('flat_groups_top', 'v2: match /{r=**}/groups/{g}/members/{m} on groups/g1/members/m1', fs('2', flatGroups), 'groups/g1/members/m1'),
  r('flat_groups_deep', 'v2: match /{r=**}/groups/{g}/members/{m} on orgs/o1/groups/g1/members/m1', fs('2', flatGroups), 'orgs/o1/groups/g1/members/m1'),
  r('nested_second_glob', 'v2: match /a/{r=**} { match /b/{s=**} }: a second glob in a nested block', fs('2', `    match /a/{r=**} {
      match /b/{s=**} {
        allow get: if true;
      }
    }`), 'a/x/b/y'),
  r('v1_nonlast_glob', "rules_version '1': match /{r=**}/x/{d}", fs('1', `    match /{r=**}/x/{d} {
      allow get: if true;
    }`), 'a/b/x/d'),
  r('binding_both', 'v2: match /{document=**} { match /notes/{noteId} } on notes/n1/notes/n2: document == /notes/n1 && noteId == n2', fs('2', binding("document == /notes/n1 && noteId == 'n2'")), 'notes/n1/notes/n2'),
  r('binding_document', 'v2: the same; document == /notes/n1', fs('2', binding('document == /notes/n1')), 'notes/n1/notes/n2'),
  r('binding_note_id', "v2: the same; noteId == 'n2'", fs('2', binding("noteId == 'n2'")), 'notes/n1/notes/n2'),
  r('binding_true', 'v2: the same; allow get: if true', fs('2', binding('true')), 'notes/n1/notes/n2'),
];

// The Storage form of the non-final case, recorded by nesting-storage.mjs.
export const storageCases = [
  {
    id: 'storage_nonfinal',
    desc: 'Storage v2: match /{r=**}/g/{gid} { match /m/{mid} } on a/g/g1/m/m1',
    rules: `rules_version = '2';
service firebase.storage {
  match /b/{bucket}/o {
    match /{r=**}/g/{gid} {
      match /m/{mid} {
        allow get: if true;
      }
    }
  }
}
`,
    path: 'a/g/g1/m/m1',
    pin: true,
  },
  {
    id: 'storage_nonfinal_top',
    desc: 'Storage v2: match /{r=**}/g/{gid} { match /m/{mid} } on g/g1/m/m1',
    rules: `rules_version = '2';
service firebase.storage {
  match /b/{bucket}/o {
    match /{r=**}/g/{gid} {
      match /m/{mid} {
        allow get: if true;
      }
    }
  }
}
`,
    path: 'g/g1/m/m1',
    pin: true,
  },
  {
    id: 'storage_final_depth',
    desc: 'Storage v2: match /p/{r=**} { match /m/{mid} } on p/a/b/m/m1',
    rules: `rules_version = '2';
service firebase.storage {
  match /b/{bucket}/o {
    match /p/{r=**} {
      match /m/{mid} {
        allow get: if true;
      }
    }
  }
}
`,
    path: 'p/a/b/m/m1',
    pin: true,
  },
  {
    id: 'storage_final_v1',
    desc: "Storage rules_version '1': match /p/{r=**} { match /m/{mid} } on p/a/m/m1",
    rules: `rules_version = '1';
service firebase.storage {
  match /b/{bucket}/o {
    match /p/{r=**} {
      match /m/{mid} {
        allow get: if true;
      }
    }
  }
}
`,
    path: 'p/a/m/m1',
    pin: true,
  },
];
