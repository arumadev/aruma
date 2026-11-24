import { useEffect, useState } from 'react';

export type Route =
  | { screen: 'teams' }
  | { screen: 'team'; teamId: string; tab: 'tasks' | 'chat' }
  | { screen: 'profile' };

export function parse(hash: string): Route {
  const parts = hash.replace(/^#\/?/, '').split('/').filter(Boolean).map(decodeURIComponent);
  if (parts[0] === 'profile') return { screen: 'profile' };
  if (parts[0] === 'teams' && parts[1]) {
    return { screen: 'team', teamId: parts[1].toUpperCase(), tab: parts[2] === 'chat' ? 'chat' : 'tasks' };
  }
  return { screen: 'teams' };
}

export function href(route: Route): string {
  switch (route.screen) {
    case 'profile':
      return '#/profile';
    case 'team':
      return `#/teams/${encodeURIComponent(route.teamId)}/${route.tab}`;
    default:
      return '#/teams';
  }
}

export function go(route: Route): void {
  window.location.hash = href(route);
}

// The app's routes live in the URL's hash, so any static server can serve the build.
export function useRoute(): Route {
  const [route, setRoute] = useState(() => parse(window.location.hash));
  useEffect(() => {
    const on = () => setRoute(parse(window.location.hash));
    window.addEventListener('hashchange', on);
    return () => window.removeEventListener('hashchange', on);
  }, []);
  return route;
}
