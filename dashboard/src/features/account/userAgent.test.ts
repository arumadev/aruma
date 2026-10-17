import { describe, expect, it } from 'vitest';
import { describeUserAgent } from './userAgent';

describe('describeUserAgent', () => {
  it.each([
    [
      'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36',
      { browser: 'Chrome', system: 'macOS' },
    ],
    ['Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0', { browser: 'Firefox', system: 'Windows' }],
    [
      'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 Edg/140.0.0.0',
      { browser: 'Edge', system: 'Windows' },
    ],
    [
      'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1',
      { browser: 'Safari', system: 'iOS' },
    ],
    ['curl/8.7.1', { browser: 'curl', system: undefined }],
  ])('%s', (ua, expected) => {
    expect(describeUserAgent(ua)).toEqual(expected);
  });

  it('returns nothing for a missing user agent', () => {
    expect(describeUserAgent(undefined)).toEqual({});
  });
});
