// Object names and folders. A bucket is flat: a "folder" is a prefix ending in "/", which the
// listing groups names by. Names are opaque otherwise, and go to the API as written.

export interface Crumb {
  label: string;
  prefix: string;
}

/** The folders from the root down to `prefix` ("" or a prefix ending in "/"). */
export function crumbs(prefix: string): Crumb[] {
  const out: Crumb[] = [];
  let at = 0;
  for (let end = prefix.indexOf('/'); end >= 0; end = prefix.indexOf('/', at)) {
    out.push({ label: prefix.slice(at, end), prefix: prefix.slice(0, end + 1) });
    at = end + 1;
  }
  return out;
}

/** The folder a name or a folder is in: "" at the root. */
export function parentPrefix(name: string): string {
  const trimmed = name.endsWith('/') ? name.slice(0, -1) : name;
  return trimmed.slice(0, trimmed.lastIndexOf('/') + 1);
}

/** A name or a folder as its folder shows it: without the folder, and a folder without its "/". */
export function leafName(name: string, prefix: string): string {
  const rest = name.startsWith(prefix) ? name.slice(prefix.length) : name;
  return rest.endsWith('/') ? rest.slice(0, -1) : rest;
}

const encoder = new TextEncoder();

export type NameProblem = 'empty' | 'long' | 'chars' | 'slash' | 'dots' | 'reserved';

/** What the server would refuse in an object name (ADR-022 §17), if anything. */
export function nameProblem(name: string): NameProblem | undefined {
  if (name === '') return 'empty';
  if (encoder.encode(name).length > 1024) return 'long';
  if (/[\r\n\0]/.test(name)) return 'chars';
  if (name === '.' || name === '..') return 'dots';
  if (name.startsWith('.well-known/acme-challenge/')) return 'reserved';
  return undefined;
}

/** A folder's name as typed: one segment, without "/". */
export function folderProblem(name: string): NameProblem | undefined {
  if (name.trim() === '') return 'empty';
  if (name.includes('/')) return 'slash';
  return nameProblem(name);
}

// The raster formats a browser shows in an <img>; SVG is not one: it is a document, and the
// dashboard previews no document (ADR-022 §15).
const RASTER = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp', 'image/avif', 'image/bmp']);

/** Whether the dashboard may preview a file of this type in an <img>. */
export function isRasterImage(contentType: string | undefined): boolean {
  return RASTER.has((contentType ?? '').split(';')[0]!.trim().toLowerCase());
}

const UNITS = ['byte', 'kilobyte', 'megabyte', 'gigabyte', 'terabyte'] as const;
export type ByteUnit = (typeof UNITS)[number];

/** A size in the largest binary unit that keeps it at least 1, for Intl's unit formatting. */
export function byteSize(bytes: number): { value: number; unit: ByteUnit } {
  let value = bytes;
  let i = 0;
  while (value >= 1024 && i < UNITS.length - 1) {
    value /= 1024;
    i++;
  }
  return { value, unit: UNITS[i]! };
}
