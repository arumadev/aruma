import {
  collection,
  collectionGroup,
  doc,
  getDoc,
  onSnapshot,
  query,
  serverTimestamp,
  setDoc,
  where,
  writeBatch,
  type Unsubscribe,
} from 'firebase/firestore';
import { ref, set } from 'firebase/database';
import { db, rtdb } from '../firebase';
import { AppError, errorCode } from '../errors';
import { limits, type Member, type Role, type Team } from './model';

// Join codes: 6 characters without the ones people confuse (0/O, 1/I/L).
const alphabet = 'ABCDEFGHJKMNPQRSTUVWXYZ23456789';

export function newCode(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(6));
  return Array.from(bytes, (b) => alphabet[b % alphabet.length]).join('');
}

export function normalizeCode(code: string): string {
  return code.trim().toUpperCase().replace(/[^A-Z0-9]/g, '');
}

export interface MyTeam {
  id: string;
  role: Role;
}

// The teams a user belongs to: a collection group query on members, by the member's uid.
export function watchMyTeams(uid: string, next: (teams: MyTeam[]) => void, fail: (e: unknown) => void): Unsubscribe {
  const q = query(collectionGroup(db, 'members'), where('uid', '==', uid));
  return onSnapshot(
    q,
    (snap) => {
      const teams = snap.docs
        .filter((d) => d.ref.parent.parent !== null)
        .map((d) => ({ id: d.ref.parent.parent!.id, role: (d.get('role') as Role) ?? 'member' }));
      next(teams);
    },
    fail,
  );
}

export function watchTeam(id: string, next: (team: Team | null) => void, fail: (e: unknown) => void): Unsubscribe {
  return onSnapshot(
    doc(db, 'teams', id),
    (snap) => {
      if (!snap.exists()) return next(null);
      const d = snap.data();
      next({ id: snap.id, name: d.name, owner: d.owner, createdAt: d.createdAt ?? null });
    },
    fail,
  );
}

export function watchMembers(teamId: string, next: (members: Member[]) => void, fail: (e: unknown) => void): Unsubscribe {
  return onSnapshot(
    collection(db, 'teams', teamId, 'members'),
    (snap) =>
      next(
        snap.docs.map((d) => {
          const m = d.data();
          return { uid: d.id, role: m.role, name: m.name, joinedAt: m.joinedAt ?? null };
        }),
      ),
    fail,
  );
}

// The Realtime Database rules cannot read Firestore, so a member also writes
// members/{teamId}/{uid} = true there. Writing it again is harmless.
export function mirrorMembership(teamId: string, uid: string): Promise<void> {
  return set(ref(rtdb, `members/${teamId}/${uid}`), true);
}

// Creating a team is one batch: the team, then its owner's member document (the rules read the
// team as it will be after the batch). A taken code fails the batch with permission-denied (the
// team exists and is someone else's), so the app tries another code.
export async function createTeam(name: string, uid: string, memberName: string): Promise<string> {
  const clean = name.trim();
  if (clean.length === 0 || clean.length > limits.teamName) throw new AppError('app/name-length', { max: limits.teamName });
  for (let attempt = 0; ; attempt++) {
    const id = newCode();
    const batch = writeBatch(db);
    batch.set(doc(db, 'teams', id), { name: clean, owner: uid, createdAt: serverTimestamp() });
    batch.set(doc(db, 'teams', id, 'members', uid), {
      uid,
      role: 'owner',
      name: memberName,
      joinedAt: serverTimestamp(),
    });
    try {
      await batch.commit();
    } catch (e) {
      if (errorCode(e) === 'permission-denied' && attempt < 2) continue;
      throw e;
    }
    await mirrorMembership(id, uid);
    return id;
  }
}

// Joining: look the team up by its code, then write one's own member document.
export async function joinTeam(code: string, uid: string, memberName: string): Promise<string> {
  const id = normalizeCode(code);
  if (!id) throw new AppError('app/team-not-found');
  const team = await getDoc(doc(db, 'teams', id));
  if (!team.exists()) throw new AppError('app/team-not-found');
  await setDoc(doc(db, 'teams', id, 'members', uid), {
    uid,
    role: 'member',
    name: memberName,
    joinedAt: serverTimestamp(),
  });
  await mirrorMembership(id, uid);
  return id;
}
