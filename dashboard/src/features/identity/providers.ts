import type { AuthUser } from '@/lib/api/types';

// The sign-in methods a user can have, as keys of the "auth.provider" messages.
export type ProviderLabel = 'password' | 'google' | 'apple' | 'anonymous';

const LABELS: Record<string, ProviderLabel> = { password: 'password', 'google.com': 'google', 'apple.com': 'apple' };

/** The label of a linked provider, if it is one the console knows. */
export const providerLabel = (providerId: string): ProviderLabel | undefined => LABELS[providerId];

/** The user's sign-in methods; a user with none and no email is anonymous. */
export function providerLabels(user: AuthUser): ProviderLabel[] {
  const out = user.providers.map((p) => providerLabel(p.providerId)).filter((l): l is ProviderLabel => Boolean(l));
  return out.length === 0 && !user.email ? ['anonymous'] : out;
}
