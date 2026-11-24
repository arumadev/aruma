import type { ReactNode } from 'react';
import { useI18n } from '../i18n';
import { errorMessage } from '../errors';

export function ErrorText({ error, onDismiss }: { error: unknown; onDismiss?: () => void }) {
  const { t } = useI18n();
  if (!error) return null;
  return (
    <p className="error" role="alert">
      <span>{errorMessage(t, error)}</span>
      {onDismiss && (
        <button type="button" className="link" onClick={onDismiss}>
          {t('common.dismiss')}
        </button>
      )}
    </p>
  );
}

export function Loading() {
  const { t } = useI18n();
  return (
    <p className="muted loading" role="status">
      <span className="spinner" aria-hidden="true" />
      {t('common.loading')}
    </p>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <p className="empty">{children}</p>;
}

export function Progress({ value, label }: { value: number; label: string }) {
  return (
    <div className="progress" role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100}
      aria-valuenow={Math.round(value * 100)}>
      <div className="progress-bar" style={{ inlineSize: `${Math.round(value * 100)}%` }} />
      <span className="progress-label">{label}</span>
    </div>
  );
}

const palette = ['#2563eb', '#7c3aed', '#db2777', '#ea580c', '#16a34a', '#0891b2', '#ca8a04', '#4f46e5'];

export function Avatar({ name, photoURL, size = 32, uid }: { name: string; photoURL?: string | null; size?: number; uid?: string }) {
  const seed = [...(uid ?? name)].reduce((a, c) => a + c.charCodeAt(0), 0);
  const initials = name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((w) => [...w][0])
    .join('')
    .toUpperCase();
  return photoURL ? (
    <img className="avatar" src={photoURL} alt="" width={size} height={size} style={{ inlineSize: size, blockSize: size }} />
  ) : (
    <span className="avatar" aria-hidden="true"
      style={{ inlineSize: size, blockSize: size, background: palette[seed % palette.length], fontSize: size * 0.4 }}>
      {initials || '?'}
    </span>
  );
}
