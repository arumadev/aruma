// @vitest-environment node
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

// WCAG 2.2 contrast checks for every text/background token pair the dashboard uses, in both
// schemes. Text needs 4.5:1 (AA, normal size); component boundaries and focus rings 3:1.

const css = readFileSync(new URL('./colors.css', import.meta.url), 'utf8');

// Short hex (#fff) is expanded to the six-digit form.
const expand = (hex: string) => (hex.length === 4 ? `#${[...hex.slice(1)].map((c) => c + c).join('')}` : hex);

function parseScheme(block: string): Record<string, string> {
  return Object.fromEntries([...block.matchAll(/--([\w-]+):\s*(#[0-9a-f]{6}|#[0-9a-f]{3});/g)].map((m) => [m[1]!, expand(m[2]!)]));
}

const [lightBlock, darkBlock] = css.split('@media (prefers-color-scheme: dark)');
const SCHEMES = { light: parseScheme(lightBlock!), dark: parseScheme(darkBlock!) };

function luminance(hex: string): number {
  const channel = (i: number) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255;
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(1) + 0.7152 * channel(3) + 0.0722 * channel(5);
}

function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x) as [number, number];
  return (hi + 0.05) / (lo + 0.05);
}

const SURFACES = [
  'surface',
  'surface-container-lowest',
  'surface-container-low',
  'surface-container',
  'surface-container-high',
  'surface-container-highest',
];
const sys = (role: string) => `md-sys-color-${role}`;
const custom = (role: string) => `md-custom-color-${role}`;

const TEXT_PAIRS: [string, string][] = [
  [sys('on-primary'), sys('primary')],
  [sys('on-primary-container'), sys('primary-container')],
  [sys('on-secondary-container'), sys('secondary-container')],
  [sys('on-error'), sys('error')],
  [sys('on-error-container'), sys('error-container')],
  [custom('on-warning-container'), custom('warning-container')],
  [sys('inverse-on-surface'), sys('inverse-surface')],
  ...SURFACES.flatMap((s): [string, string][] => [
    [sys('on-surface'), sys(s)],
    [sys('on-surface-variant'), sys(s)],
    [sys('primary'), sys(s)],
    [sys('error'), sys(s)],
  ]),
];

const NON_TEXT_PAIRS: [string, string][] = [
  [sys('outline'), sys('surface')],
  [sys('outline'), sys('surface-container-lowest')],
  [sys('secondary'), sys('surface')],
  [sys('primary'), sys('surface-container-lowest')],
];

describe.each(Object.entries(SCHEMES))('%s scheme', (_, scheme) => {
  it.each(TEXT_PAIRS)('%s on %s meets 4.5:1', (fg, bg) => {
    expect(scheme[fg], fg).toBeDefined();
    expect(scheme[bg], bg).toBeDefined();
    expect(contrast(scheme[fg]!, scheme[bg]!)).toBeGreaterThanOrEqual(4.5);
  });

  it.each(NON_TEXT_PAIRS)('%s against %s meets 3:1', (fg, bg) => {
    expect(contrast(scheme[fg]!, scheme[bg]!)).toBeGreaterThanOrEqual(3);
  });
});
