import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';
import { useLive } from './live';
import { onAuthStateChanged, type User } from 'firebase/auth';
import { auth } from './firebase';
import { watchProfile } from './data/profile';
import type { Profile } from './data/model';

interface Session {
  /** undefined while Auth restores the session from the browser. */
  user: User | null | undefined;
  profile: Profile | null;
  /** The name shown to teammates: the profile's, else the Auth profile's. */
  name: string;
}

const Ctx = createContext<Session>({ user: undefined, profile: null, name: '' });

export function SessionProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null | undefined>(undefined);
  useEffect(() => onAuthStateChanged(auth, setUser), []);

  // A missing or unreadable profile (a user created elsewhere) is fine: the Auth profile stands in.
  const uid = user?.uid ?? null;
  const profile = useLive<Profile | null>(uid, (next, fail) => watchProfile(uid!, next, fail)).value ?? null;

  const name = profile?.name || user?.displayName || user?.email?.split('@')[0] || 'User';
  return <Ctx.Provider value={{ user, profile, name }}>{children}</Ctx.Provider>;
}

export function useSession(): Session {
  return useContext(Ctx);
}

/** The signed-in user, for screens that are only shown when there is one. */
export function useUser(): User {
  const { user } = useSession();
  if (!user) throw new Error('useUser without a signed-in user');
  return user;
}
