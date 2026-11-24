import {
  onDisconnect,
  onValue,
  ref,
  remove,
  serverTimestamp,
  set,
  type Unsubscribe,
} from 'firebase/database';
import { rtdb } from '../firebase';
import type { Presence } from './model';

// Whether the Realtime Database connection is up, live (.info/connected).
export function watchConnected(next: (connected: boolean) => void): Unsubscribe {
  return onValue(ref(rtdb, '.info/connected'), (snap) => next(snap.val() === true));
}

// Marks the user online in a team while the returned function has not been called. Each time the
// connection comes up, the server is first told to remove the entry when the connection drops
// (a closed tab, a lost network), then the entry is written.
export function goOnline(teamId: string, uid: string, name: string, fail: (e: unknown) => void): () => void {
  const me = ref(rtdb, `presence/${teamId}/${uid}`);
  const off = onValue(ref(rtdb, '.info/connected'), (snap) => {
    if (snap.val() !== true) return;
    onDisconnect(me)
      .remove()
      .then(() => set(me, { name, online: true, lastSeen: serverTimestamp() }))
      .catch(fail);
  });
  return () => {
    off();
    onDisconnect(me).cancel().catch(() => {});
    remove(me).catch(() => {});
  };
}

export function watchPresence(teamId: string, next: (p: Presence[]) => void, fail: (e: unknown) => void): Unsubscribe {
  return onValue(
    ref(rtdb, `presence/${teamId}`),
    (snap) => {
      const list: Presence[] = [];
      snap.forEach((child) => {
        const v = child.val();
        if (v && v.online) list.push({ uid: child.key ?? '', name: v.name, online: true, lastSeen: v.lastSeen });
      });
      next(list);
    },
    fail,
  );
}
