import { vi } from 'vitest';

// Stands in for next/navigation in component tests (registered in setup.ts).
export const router = { push: vi.fn(), replace: vi.fn(), back: vi.fn(), forward: vi.fn(), refresh: vi.fn(), prefetch: vi.fn() };
export const searchParams = { current: new URLSearchParams() };

export const navigationMock = {
  useRouter: () => router,
  usePathname: () => '/',
  useSearchParams: () => searchParams.current,
};
