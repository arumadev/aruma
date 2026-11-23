import { useEffect, useState } from 'react';
import { useI18n } from './i18n';
import { useSession } from './session';
import { href, useRoute } from './route';
import { watchConnected } from './data/presence';
import { Loading } from './components/ui';
import { UserAvatar } from './components/UserAvatar';
import { SignIn } from './screens/SignIn';
import { Teams } from './screens/Teams';
import { Team } from './screens/Team';
import { Profile } from './screens/Profile';

// The offline banner follows the Realtime Database's own view of the connection
// (.info/connected). It waits a moment at start, while the first connection opens.
function useOffline(): boolean {
  const [connected, setConnected] = useState<boolean | null>(null);
  const [settled, setSettled] = useState(false);
  useEffect(() => watchConnected(setConnected), []);
  useEffect(() => {
    const id = setTimeout(() => setSettled(true), 4000);
    return () => clearTimeout(id);
  }, []);
  return connected === false && settled;
}

export function App() {
  const { t, otherLang, setLang, nameOf } = useI18n();
  const { user, name } = useSession();
  const route = useRoute();
  const offline = useOffline();

  let screen;
  if (user === undefined) screen = <Loading />;
  else if (user === null) screen = <SignIn />;
  else if (route.screen === 'profile') screen = <Profile />;
  else if (route.screen === 'team') screen = <Team key={route.teamId} teamId={route.teamId} tab={route.tab} />;
  else screen = <Teams />;

  return (
    <div className="app">
      <header className="topbar">
        <a className="brand" href={href({ screen: 'teams' })}>
          <span className="logo" aria-hidden="true" />
          {t('app.title')}
        </a>
        {user && (
          <nav className="nav" aria-label={t('nav.label')}>
            <a href={href({ screen: 'teams' })} aria-current={route.screen !== 'profile' ? 'page' : undefined}>
              {t('nav.teams')}
            </a>
            <a href={href({ screen: 'profile' })} aria-current={route.screen === 'profile' ? 'page' : undefined}
              className="nav-profile" data-testid="nav-profile">
              <UserAvatar uid={user.uid} name={name} size={24} />
              <span>{t('nav.profile')}</span>
            </a>
          </nav>
        )}
        <button type="button" className="lang small" lang={otherLang} onClick={() => setLang(otherLang)}
          aria-label={t('language.switch', { name: nameOf(otherLang) })} data-testid="language-switch">
          {nameOf(otherLang)}
        </button>
      </header>
      {offline && (
        <div className="banner" role="status" data-testid="offline-banner">
          {t('offline')}
        </div>
      )}
      <main id="main">{screen}</main>
    </div>
  );
}
