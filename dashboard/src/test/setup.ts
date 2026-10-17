import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach, vi } from 'vitest';

vi.mock('next/navigation', async () => (await import('./navigation')).navigationMock);

// Tests that opt into the node environment have no DOM to reset.
if (typeof document !== 'undefined') {
  afterEach(() => {
    cleanup();
    localStorage.clear();
    document.documentElement.lang = 'ar';
    document.documentElement.dir = 'rtl';
  });
}
