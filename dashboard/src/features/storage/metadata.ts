// The metadata form of a file: its five content fields and its custom metadata, and the PATCH
// that turns the saved values into the edited ones.
import { CONTENT_FIELDS, type ContentField, type ObjectPatch, type StoredObject } from './api';

export interface MetadataRow {
  /** Stable across edits, for React keys. */
  id: number;
  key: string;
  value: string;
}

export interface MetadataForm {
  fields: Record<ContentField, string>;
  rows: MetadataRow[];
}

export function formOf(o: StoredObject): MetadataForm {
  const fields = Object.fromEntries(CONTENT_FIELDS.map((f) => [f, o[f] ?? ''])) as Record<ContentField, string>;
  const rows = Object.entries(o.metadata)
    .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
    .map(([key, value], i) => ({ id: i + 1, key, value }));
  return { fields, rows };
}

/** The edit that makes the saved object's metadata the form's; empty when nothing changed. */
export function patchOf(o: StoredObject, form: MetadataForm): ObjectPatch {
  const patch: ObjectPatch = {};
  for (const f of CONTENT_FIELDS) {
    const next = form.fields[f].trim();
    if (next !== (o[f] ?? '')) patch[f] = next === '' ? null : next;
  }
  const edited = new Map(form.rows.filter((r) => r.key !== '').map((r) => [r.key, r.value]));
  const metadata: Record<string, string | null> = {};
  for (const key of Object.keys(o.metadata)) {
    if (!edited.has(key)) metadata[key] = null;
  }
  for (const [key, value] of edited) {
    if (o.metadata[key] !== value) metadata[key] = value;
  }
  if (Object.keys(metadata).length > 0) patch.metadata = metadata;
  return patch;
}

export type FieldProblem = 'chars' | 'long';
export type RowProblem = 'empty' | 'duplicate' | 'chars' | 'reserved';

const encoder = new TextEncoder();
const bytesOf = (s: string) => encoder.encode(s).length;

// CR, LF and the other control characters: a header value cannot carry them (ADR-022 §17).
const CONTROL = /[\u0000-\u001f\u007f]/;

export interface FormProblems {
  fields: Partial<Record<ContentField, FieldProblem>>;
  rows: Record<number, RowProblem>;
  /** The custom metadata is larger than the server keeps. */
  tooLarge: boolean;
}

/** What the server would refuse in the form, under its limits. */
export function problemsOf(form: MetadataForm, limits: { headerFieldBytes: number; metadataBytes: number }): FormProblems {
  const out: FormProblems = { fields: {}, rows: {}, tooLarge: false };
  for (const f of CONTENT_FIELDS) {
    const v = form.fields[f].trim();
    if (CONTROL.test(v)) out.fields[f] = 'chars';
    else if (bytesOf(v) > limits.headerFieldBytes) out.fields[f] = 'long';
  }
  const seen = new Set<string>();
  let total = 0;
  for (const r of form.rows) {
    if (r.key === '' && r.value === '') continue;
    total += bytesOf(r.key) + bytesOf(r.value);
    if (r.key === '') out.rows[r.id] = 'empty';
    else if (seen.has(r.key)) out.rows[r.id] = 'duplicate';
    else if (CONTROL.test(r.key) || CONTROL.test(r.value)) out.rows[r.id] = 'chars';
    else if (r.key === 'firebaseStorageDownloadTokens') out.rows[r.id] = 'reserved';
    seen.add(r.key);
  }
  out.tooLarge = total > limits.metadataBytes;
  return out;
}

export const hasProblems = (p: FormProblems) => p.tooLarge || Object.keys(p.fields).length > 0 || Object.keys(p.rows).length > 0;
