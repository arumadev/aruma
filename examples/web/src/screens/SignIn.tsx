import { useState, type FormEvent } from 'react';
import { useI18n } from '../i18n';
import { signIn, signInAsGuest, signUp } from '../data/profile';
import { ErrorText } from '../components/ui';

export function SignIn() {
  const { t } = useI18n();
  const [mode, setMode] = useState<'signIn' | 'signUp'>('signIn');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  async function run(action: () => Promise<void>) {
    setBusy(true);
    setError(null);
    try {
      await action();
      // Signed in: the session switches the screen.
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    void run(() => (mode === 'signIn' ? signIn(email, password) : signUp(email, password, name)));
  }

  const signingUp = mode === 'signUp';
  return (
    <section className="auth card" aria-labelledby="auth-title">
      <h1 id="auth-title">{signingUp ? t('auth.signUpTitle') : t('auth.signInTitle')}</h1>
      <p className="muted">{t('app.tagline')}</p>
      <form onSubmit={submit} className="stack" noValidate>
        {signingUp && (
          <label className="field">
            <span>{t('auth.name')}</span>
            <input name="name" autoComplete="name" required maxLength={40} value={name}
              onChange={(e) => setName(e.target.value)} />
          </label>
        )}
        <label className="field">
          <span>{t('auth.email')}</span>
          <input name="email" type="email" autoComplete="email" dir="ltr" required value={email}
            onChange={(e) => setEmail(e.target.value)} />
        </label>
        <label className="field">
          <span>{t('auth.password')}</span>
          <input name="password" type="password" dir="ltr" required
            autoComplete={signingUp ? 'new-password' : 'current-password'} value={password}
            onChange={(e) => setPassword(e.target.value)} aria-describedby={signingUp ? 'password-hint' : undefined} />
          {signingUp && <small id="password-hint" className="muted">{t('auth.passwordHint')}</small>}
        </label>
        <ErrorText error={error} />
        <button className="primary" type="submit" disabled={busy}>
          {busy ? t('auth.working') : signingUp ? t('auth.signUp') : t('auth.signIn')}
        </button>
      </form>
      <button type="button" className="link" onClick={() => { setMode(signingUp ? 'signIn' : 'signUp'); setError(null); }}>
        {signingUp ? t('auth.toSignIn') : t('auth.toSignUp')}
      </button>
      <div className="divider" role="separator"><span>{t('auth.or')}</span></div>
      <button type="button" className="secondary" disabled={busy}
        onClick={() => void run(() => signInAsGuest((code) => t('auth.guestName', { code })))}>
        {t('auth.guest')}
      </button>
    </section>
  );
}
