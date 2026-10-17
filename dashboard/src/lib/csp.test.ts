// @vitest-environment node
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { describe, expect, it } from 'vitest';
import manifest from '../../public/csp.json';

// React Aria injects two fixed <style> elements at runtime (press handling, and the scroll lock
// of modals on iOS). The server's CSP allows them by hash, read from public/csp.json in the
// export. This recomputes the hashes from the installed react-aria, so an upgrade that changes
// the injected text fails here instead of in the browser.

const reactAria = dirname(createRequire(import.meta.url).resolve('react-aria/package.json'));
const source = (file: string) => readFileSync(join(reactAria, 'dist/private', file), 'utf8');

/** The text of `style.textContent = `…`.trim()` in a compiled module, with ${…} resolved. */
function injectedStyle(code: string, constants: Record<string, string> = {}): string {
  const match = /style\.textContent = `([\s\S]*?)`\.trim\(\)/.exec(code);
  if (!match) throw new Error('React Aria no longer injects a <style> element here; update public/csp.json.');
  return match[1]!.replace(/\$\{([^}]+)\}/g, (_, name: string) => {
    const key = Object.keys(constants).find((k) => name.endsWith(k));
    if (!key) throw new Error(`Unknown interpolation ${name}`);
    return constants[key]!;
  }).trim();
}

const hash = (text: string) => `'sha256-${createHash('sha256').update(text).digest('base64')}'`;

describe('CSP manifest', () => {
  it('lists the hash of every <style> element React Aria injects', () => {
    const press = source('interactions/usePress.mjs');
    const attribute = /PRESSABLE_ATTRIBUTE = '([^']+)'/.exec(press)?.[1];
    expect(attribute).toBeDefined();
    const expected = [
      hash(injectedStyle(press, { PRESSABLE_ATTRIBUTE: attribute! })),
      hash(injectedStyle(source('overlays/usePreventScroll.mjs'))),
    ];
    expect(manifest.styleSrcElem).toEqual(expected);
  });
});
