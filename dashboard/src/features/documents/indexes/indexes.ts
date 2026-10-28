// The Indexes tab's helpers: names of indexes and fields, the composite form's checks, and the
// single-field exemptions as the console's three toggles and the Admin API's index lists.
import type { Field, Index, IndexField, QueryScope } from '../api';

/** The three kinds of single-field index the console lets a field exempt. */
export type Kind = 'ASCENDING' | 'DESCENDING' | 'ARRAYS';

export const KINDS: Kind[] = ['ASCENDING', 'DESCENDING', 'ARRAYS'];

export type KindSet = Record<Kind, boolean>;

export const ALL_KINDS: KindSet = { ASCENDING: true, DESCENDING: true, ARRAYS: true };

/** The collection group of a name …/collectionGroups/{group}/(indexes|fields)/{id}. */
export function groupOf(name: string): string {
  return /collectionGroups\/([^/]+)\//.exec(name)?.[1] ?? '';
}

/** The last segment of an index's or a field's name. */
export function idOf(name: string): string {
  return name.split('/').pop() ?? name;
}

/** An index's fields as listed: `__name__` closes every index, shown even where the server leaves it implied. */
export function listedFields(index: Index): IndexField[] {
  const fields = index.fields ?? [];
  if (fields.some((f) => f.fieldPath === '__name__')) return fields;
  const last = fields.at(-1)?.order === 'DESCENDING' ? 'DESCENDING' : 'ASCENDING';
  return [...fields, { fieldPath: '__name__', order: last }];
}

/** An index field's kind: its order, or arrays for array-contains. */
export function kindOf(f: IndexField): Kind {
  if (f.arrayConfig === 'CONTAINS') return 'ARRAYS';
  return f.order === 'DESCENDING' ? 'DESCENDING' : 'ASCENDING';
}

export const SCOPES: QueryScope[] = ['COLLECTION', 'COLLECTION_GROUP'];

export interface CompositeForm {
  collection: string;
  scope: QueryScope;
  fields: { path: string; kind: Kind }[];
}

export const newCompositeForm = (): CompositeForm => ({
  collection: '',
  scope: 'COLLECTION',
  fields: [
    { path: '', kind: 'ASCENDING' },
    { path: '', kind: 'ASCENDING' },
  ],
});

export type Problem = 'required' | 'slash' | 'name' | undefined;

/** A collection id the form takes: set, no slash, and not the reserved __x__ shape. */
export function idProblem(id: string): Problem {
  const v = id.trim();
  if (v === '') return 'required';
  if (v.includes('/')) return 'slash';
  if (/^__.*__$/.test(v)) return 'name';
  return undefined;
}

export const pathProblem = (path: string): 'required' | undefined => (path.trim() === '' ? 'required' : undefined);

export interface CompositeProblems {
  collection: Problem;
  fields: ('required' | undefined)[];
  /** The index needs two fields (a single one is the automatic index's). */
  count: boolean;
}

export function compositeProblems(form: CompositeForm): CompositeProblems {
  return { collection: idProblem(form.collection), fields: form.fields.map((f) => pathProblem(f.path)), count: form.fields.length < 2 };
}

export const hasProblems = (p: CompositeProblems) => Boolean(p.collection) || p.count || p.fields.some(Boolean);

/** The Admin API's index of the form. */
export function compositeIndex(form: CompositeForm): Index {
  return {
    queryScope: form.scope,
    fields: form.fields.map((f) => {
      const fieldPath = f.path.trim();
      return f.kind === 'ARRAYS' ? { fieldPath, arrayConfig: 'CONTAINS' as const } : { fieldPath, order: f.kind };
    }),
  };
}

/** The field a Field name …/collectionGroups/{group}/fields/{field} names. */
export const fieldPathOf = idOf;

/** The database-wide automatic settings are a field named __default__ in the group __default__. */
export const isDefaultField = (f: Field) => groupOf(f.name) === '__default__';

/** The enabled kinds among the single-field indexes of one scope. */
export function enabledKinds(field: Field, scope: QueryScope): KindSet {
  const on: KindSet = { ASCENDING: false, DESCENDING: false, ARRAYS: false };
  for (const ix of field.indexConfig?.indexes ?? []) {
    if (ix.queryScope === scope && ix.fields?.[0]) on[kindOf(ix.fields[0])] = true;
  }
  return on;
}

/**
 * The automatic settings as the table shows them: a database-wide override if there is one,
 * else what a new database has: all three kinds in collection scope, none in group scope.
 */
export function automaticSettings(fields: Field[]): Record<QueryScope, KindSet> {
  const override = fields.find(isDefaultField);
  if (!override?.indexConfig?.indexes) {
    return { COLLECTION: { ...ALL_KINDS }, COLLECTION_GROUP: { ASCENDING: false, DESCENDING: false, ARRAYS: false } };
  }
  return { COLLECTION: enabledKinds(override, 'COLLECTION'), COLLECTION_GROUP: enabledKinds(override, 'COLLECTION_GROUP') };
}

export const countOn = (s: KindSet) => KINDS.filter((k) => s[k]).length;

export interface ExemptionForm {
  collection: string;
  field: string;
  scopes: Record<QueryScope, boolean>;
  kinds: KindSet;
}

/**
 * The indexes to PUT for an exemption: only the enabled ones are listed. A scope the exemption
 * leaves alone keeps what the database's automatic settings give it (automaticSettings of the
 * fields, so a `__default__` override counts), since anything not listed is disabled.
 */
export function exemptionIndexes(form: Pick<ExemptionForm, 'scopes' | 'kinds'>, defaults: Record<QueryScope, KindSet>): Index[] {
  const make = (scope: QueryScope, kind: Kind): Index => ({
    queryScope: scope,
    fields: [kind === 'ARRAYS' ? { arrayConfig: 'CONTAINS' } : { order: kind }],
  });
  const out: Index[] = [];
  for (const scope of SCOPES) {
    const on = form.scopes[scope] ? form.kinds : defaults[scope];
    for (const k of KINDS) if (on[k]) out.push(make(scope, k));
  }
  return out;
}

/** The form an existing exemption opens the edit with. */
export function exemptionForm(field: Field): ExemptionForm {
  const collection = enabledKinds(field, 'COLLECTION');
  const group = enabledKinds(field, 'COLLECTION_GROUP');
  const groupOn = countOn(group) > 0;
  const collectionChanged = countOn(collection) !== 3;
  return {
    collection: groupOf(field.name),
    field: fieldPathOf(field.name),
    scopes: { COLLECTION: collectionChanged || !groupOn, COLLECTION_GROUP: groupOn },
    kinds: collectionChanged || !groupOn ? collection : group,
  };
}

export const newExemptionForm = (): ExemptionForm => ({ collection: '', field: '', scopes: { COLLECTION: true, COLLECTION_GROUP: false }, kinds: { ...ALL_KINDS } });

/**
 * Whether a field is a single-field exemption to list: its own indexes, and not the database
 * default. The server leaves out usesAncestorConfig when it is false (as protobuf JSON does), so
 * only an explicit true means the field follows its ancestor.
 */
export const isExemption = (f: Field) => f.indexConfig !== undefined && f.indexConfig.usesAncestorConfig !== true && !isDefaultField(f);

/** The TTL policies: fields with a ttlConfig. */
export const hasTtl = (f: Field) => Boolean(f.ttlConfig);

/**
 * Whether the group already has a TTL policy, on any of its fields: the console allows one per
 * collection group. The server itself accepts more, so this is the console's own rule.
 */
export const ttlGroupTaken = (fields: Field[], group: string) => fields.some((f) => hasTtl(f) && groupOf(f.name) === group.trim());

/** The page of a list, 0-based, and how many pages there are (at least one). */
export function pageOf<T>(items: T[], page: number, size: number): { rows: T[]; pages: number; page: number } {
  const pages = Math.max(1, Math.ceil(items.length / size));
  const p = Math.min(Math.max(page, 0), pages - 1);
  return { rows: items.slice(p * size, p * size + size), pages, page: p };
}
