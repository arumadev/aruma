import {
  createUserWithEmailAndPassword,
  signInAnonymously,
  signInWithEmailAndPassword,
  signOut as fbSignOut,
  updateProfile,
  type User,
} from 'firebase/auth';
import { doc, onSnapshot, serverTimestamp, setDoc, type Unsubscribe } from 'firebase/firestore';
import { getDownloadURL, ref as storageRef, uploadBytesResumable } from 'firebase/storage';
import { auth, db, storage } from '../firebase';
import { AppError } from '../errors';
import { limits, type Profile } from './model';

function checkName(name: string): string {
  const clean = name.trim();
  if (clean.length === 0 || clean.length > limits.name) throw new AppError('app/name-length', { max: limits.name });
  return clean;
}

function writeProfile(uid: string, p: { name: string; photoURL?: string | null }): Promise<void> {
  return setDoc(doc(db, 'users', uid), { ...p, updatedAt: serverTimestamp() }, { merge: true });
}

export async function signUp(email: string, password: string, name: string): Promise<void> {
  const clean = checkName(name);
  const { user } = await createUserWithEmailAndPassword(auth, email.trim(), password);
  await updateProfile(user, { displayName: clean });
  await writeProfile(user.uid, { name: clean, photoURL: null });
}

export async function signIn(email: string, password: string): Promise<void> {
  await signInWithEmailAndPassword(auth, email.trim(), password);
}

export async function signInAsGuest(guestName: (code: string) => string): Promise<void> {
  const { user } = await signInAnonymously(auth);
  const name = guestName(user.uid.slice(0, 4).toUpperCase());
  await updateProfile(user, { displayName: name });
  await writeProfile(user.uid, { name, photoURL: null });
}

export function signOut(): Promise<void> {
  return fbSignOut(auth);
}

export function watchProfile(uid: string, next: (p: Profile | null) => void, fail: (e: unknown) => void): Unsubscribe {
  return onSnapshot(
    doc(db, 'users', uid),
    (snap) => next(snap.exists() ? { name: snap.get('name'), photoURL: snap.get('photoURL') ?? null } : null),
    fail,
  );
}

export async function rename(user: User, name: string): Promise<void> {
  const clean = checkName(name);
  await updateProfile(user, { displayName: clean });
  await writeProfile(user.uid, { name: clean });
}

// The picture's link goes in users/{uid}.photoURL (and the Auth profile), where teammates read it.
export async function uploadAvatar(
  user: User,
  name: string,
  file: File,
  progress: (fraction: number) => void,
): Promise<void> {
  if (!file.type.startsWith('image/')) throw new AppError('app/not-image');
  if (file.size >= limits.avatarBytes) throw new AppError('app/file-too-big', { limit: '2 MB' });
  const r = storageRef(storage, `avatars/${user.uid}/avatar`);
  const upload = uploadBytesResumable(r, file, { contentType: file.type });
  await new Promise<void>((resolve, reject) => {
    upload.on(
      'state_changed',
      (s) => progress(s.totalBytes ? s.bytesTransferred / s.totalBytes : 0),
      reject,
      () => resolve(),
    );
  });
  const photoURL = await getDownloadURL(r);
  await updateProfile(user, { photoURL });
  await writeProfile(user.uid, { name: checkName(name), photoURL });
}
