import ar from '@messages/ar.json';
import en from '@messages/en.json';
import { describe, expect, it } from 'vitest';
import { AUDIT_ACTIONS } from '@/features/audit/AuditScreen';

type Catalog = { [key: string]: string | Catalog };

function flatten(catalog: Catalog, prefix = ''): Map<string, string> {
  const out = new Map<string, string>();
  for (const [key, value] of Object.entries(catalog)) {
    const path = prefix ? `${prefix}.${key}` : key;
    if (typeof value === 'string') out.set(path, value);
    else for (const [k, v] of flatten(value, path)) out.set(k, v);
  }
  return out;
}

/** ICU argument names ({name}, {count, plural, …}) and rich-text tags (<link>) of a message. */
function placeholders(message: string): string[] {
  const args = [...message.matchAll(/\{\s*(\w+)\s*[,}]/g)].map((m) => `{${m[1]}}`);
  const tags = [...message.matchAll(/<(\w+)>/g)].map((m) => `<${m[1]}>`);
  return [...new Set([...args, ...tags])].sort();
}

const flatAr = flatten(ar);
const flatEn = flatten(en);

describe('message catalogs', () => {
  it('have the same keys', () => {
    expect([...flatAr.keys()].filter((k) => !flatEn.has(k))).toEqual([]);
    expect([...flatEn.keys()].filter((k) => !flatAr.has(k))).toEqual([]);
  });

  it.each([...flatEn.keys()])('%s uses the same arguments and tags in both languages', (key) => {
    expect(placeholders(flatAr.get(key) ?? '')).toEqual(placeholders(flatEn.get(key) ?? ''));
  });

  it('have no empty messages', () => {
    expect([...flatAr, ...flatEn].filter(([, v]) => !v.trim()).map(([k]) => k)).toEqual([]);
  });

  it('label every audit action the server records', () => {
    for (const action of AUDIT_ACTIONS) {
      expect(flatEn.has(`audit.actions.${action}`), action).toBe(true);
      expect(flatAr.has(`audit.actions.${action}`), action).toBe(true);
    }
  });
});
