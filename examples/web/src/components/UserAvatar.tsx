import { useEffect, useState } from 'react';
import { doc, getDoc } from 'firebase/firestore';
import { db } from '../firebase';
import { useSession } from '../session';
import { Avatar } from './ui';

// Teammates' pictures, read once per page from users/{uid}.photoURL.
const photos = new Map<string, Promise<string | null>>();

function photoOf(uid: string): Promise<string | null> {
  let p = photos.get(uid);
  if (!p) {
    p = getDoc(doc(db, 'users', uid)).then(
      (s) => (s.get('photoURL') as string | null | undefined) ?? null,
      () => null,
    );
    photos.set(uid, p);
  }
  return p;
}

export function UserAvatar({ uid, name, size }: { uid: string; name: string; size?: number }) {
  const { user, profile } = useSession();
  const own = uid === user?.uid;
  const [photo, setPhoto] = useState<string | null>(null);
  useEffect(() => {
    if (own) return;
    let live = true;
    void photoOf(uid).then((p) => live && setPhoto(p));
    return () => {
      live = false;
    };
  }, [uid, own]);
  return <Avatar uid={uid} name={name} size={size} photoURL={own ? profile?.photoURL : photo} />;
}
