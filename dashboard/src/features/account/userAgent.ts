// A short, readable name for a session's user agent: "Firefox on macOS". Browser and system
// names are proper nouns and stay untranslated.

const BROWSERS: [RegExp, string][] = [
  [/Edg\//, 'Edge'],
  [/OPR\//, 'Opera'],
  [/Firefox\//, 'Firefox'],
  [/Chrome\//, 'Chrome'],
  [/Safari\//, 'Safari'],
  [/curl\//, 'curl'],
];

const SYSTEMS: [RegExp, string][] = [
  [/iPhone|iPad|iPod/, 'iOS'],
  [/Android/, 'Android'],
  [/Mac OS X|Macintosh/, 'macOS'],
  [/Windows/, 'Windows'],
  [/CrOS/, 'ChromeOS'],
  [/Linux/, 'Linux'],
];

export interface UserAgentSummary {
  browser?: string;
  system?: string;
}

export function describeUserAgent(ua: string | undefined): UserAgentSummary {
  if (!ua) return {};
  return {
    browser: BROWSERS.find(([re]) => re.test(ua))?.[1],
    system: SYSTEMS.find(([re]) => re.test(ua))?.[1],
  };
}
