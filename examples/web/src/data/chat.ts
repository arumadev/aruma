import {
  limitToLast,
  onValue,
  orderByChild,
  push,
  query,
  ref,
  serverTimestamp,
  type Unsubscribe,
} from 'firebase/database';
import { rtdb } from '../firebase';
import { AppError } from '../errors';
import { limits, type ChatMessage } from './model';

// The last 50 messages by server time, live (the rules index messages on ts).
export function watchMessages(teamId: string, next: (m: ChatMessage[]) => void, fail: (e: unknown) => void): Unsubscribe {
  const q = query(ref(rtdb, `chat/${teamId}/messages`), orderByChild('ts'), limitToLast(50));
  return onValue(
    q,
    (snap) => {
      const list: ChatMessage[] = [];
      snap.forEach((child) => {
        const v = child.val();
        list.push({ id: child.key ?? '', uid: v.uid, name: v.name, text: v.text, ts: v.ts });
      });
      next(list);
    },
    fail,
  );
}

export async function sendMessage(teamId: string, uid: string, name: string, text: string): Promise<void> {
  const clean = text.trim();
  if (clean.length === 0 || clean.length > limits.message) throw new AppError('app/message-length');
  await push(ref(rtdb, `chat/${teamId}/messages`), { uid, name, text: clean, ts: serverTimestamp() });
}
