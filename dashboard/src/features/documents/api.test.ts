import { afterEach, describe, expect, it, vi } from 'vitest';
import { createDocument, simulateRules, toWire, updateDocument } from './api';

function stubFetch() {
  const fetch = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }));
  vi.stubGlobal('fetch', fetch);
  return () => (fetch.mock.calls[0] as unknown as [string, RequestInit])[1].body as string;
}

afterEach(() => vi.unstubAllGlobals());

describe('request bodies', () => {
  it('sends -0 so the server reads -0, in a nested value, and leaves other doubles alone', async () => {
    const body = stubFetch();
    await updateDocument('p', 'users/u1', {
      fields: {
        z: { doubleValue: -0 },
        zero: { doubleValue: 0 },
        half: { doubleValue: 0.5 },
        nan: { doubleValue: 'NaN' },
        list: { arrayValue: { values: [{ doubleValue: -0 }] } },
        map: { mapValue: { fields: { z: { doubleValue: -0 } } } },
      },
      updateMask: ['z'],
    });
    expect(body()).toBe(
      '{"fields":{"z":{"doubleValue":"-0"},"zero":{"doubleValue":0},"half":{"doubleValue":0.5},"nan":{"doubleValue":"NaN"},' +
        '"list":{"arrayValue":{"values":[{"doubleValue":"-0"}]}},"map":{"mapValue":{"fields":{"z":{"doubleValue":"-0"}}}}},"updateMask":["z"]}',
    );
  });

  it('does the same for a new document and a simulated request', async () => {
    let body = stubFetch();
    await createDocument('p', 'users', { fields: { z: { doubleValue: -0 } } });
    expect(body()).toContain('"doubleValue":"-0"');
    vi.unstubAllGlobals();
    body = stubFetch();
    await simulateRules('p', { method: 'create', path: 'users/a', data: { z: { doubleValue: -0 } } });
    expect(body()).toContain('"doubleValue":"-0"');
  });

  it('does not change what it is given', () => {
    const fields = { z: { doubleValue: -0 } };
    toWire(fields);
    expect(Object.is(fields.z.doubleValue, -0)).toBe(true);
  });
});
