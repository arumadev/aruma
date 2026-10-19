// Authorised domains, checked like the server does (internal/identity/core, checkConfig): a
// bare host name or IP address, no scheme, port, path or wildcard.

const LABEL = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;
const IPV4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/;

/** Lower-cases and trims a domain as the server stores it. */
export const normalizeDomain = (input: string) => input.trim().toLowerCase();

export function isValidDomain(domain: string): boolean {
  const ipv4 = IPV4.exec(domain);
  if (ipv4) return ipv4.slice(1).every((part) => Number(part) <= 255);
  if (domain.includes(':')) return /^[0-9a-f:.]+$/.test(domain) && domain.split(':').length >= 3;
  return domain.length > 0 && domain.length <= 253 && domain.split('.').every((label) => LABEL.test(label));
}
