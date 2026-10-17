import { act, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { LocaleSwitch } from '@/components/shell/LocaleSwitch';
import { Ltr } from '@/components/ui/Code';
import { renderApp, stubApi } from '@/test/render';

afterEach(() => vi.unstubAllGlobals());

describe('language', () => {
  it('defaults to Arabic, right to left', () => {
    stubApi({});
    renderApp(<LocaleSwitch />);
    expect(document.documentElement).toHaveAttribute('lang', 'ar');
    expect(document.documentElement).toHaveAttribute('dir', 'rtl');
    expect(screen.getByRole('button', { name: 'التبديل إلى English' })).toBeInTheDocument();
  });

  it('switches in place, flips the direction and remembers the choice', async () => {
    stubApi({});
    renderApp(<LocaleSwitch />);
    await userEvent.click(screen.getByRole('button', { name: 'التبديل إلى English' }));
    expect(document.documentElement).toHaveAttribute('lang', 'en');
    expect(document.documentElement).toHaveAttribute('dir', 'ltr');
    expect(localStorage.getItem('aruma.locale')).toBe('en');
    expect(screen.getByRole('button', { name: 'Switch to العربية' })).toBeInTheDocument();
  });

  it('restores the language saved on this device', async () => {
    stubApi({});
    localStorage.setItem('aruma.locale', 'en');
    await act(async () => renderApp(<LocaleSwitch />));
    expect(document.documentElement).toHaveAttribute('dir', 'ltr');
  });
});

describe('identifiers', () => {
  it('are isolated left to right', () => {
    stubApi({});
    renderApp(<Ltr>1:123456789012:web:0123456789abcdef</Ltr>);
    const id = screen.getByText('1:123456789012:web:0123456789abcdef');
    expect(id.tagName).toBe('BDI');
    expect(id).toHaveAttribute('dir', 'ltr');
  });
});
