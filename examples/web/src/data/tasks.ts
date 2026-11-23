import {
  addDoc,
  collection,
  deleteDoc,
  doc,
  onSnapshot,
  orderBy,
  query,
  serverTimestamp,
  updateDoc,
  type Unsubscribe,
} from 'firebase/firestore';
import { deleteObject, getDownloadURL, ref as storageRef, uploadBytesResumable } from 'firebase/storage';
import { db, storage } from '../firebase';
import { AppError, errorCode } from '../errors';
import { limits, type Attachment, type Task } from './model';

const tasksOf = (teamId: string) => collection(db, 'teams', teamId, 'tasks');

// Tasks, newest first (a single-field index); the UI puts the open ones first.
export function watchTasks(teamId: string, next: (tasks: Task[]) => void, fail: (e: unknown) => void): Unsubscribe {
  return onSnapshot(
    query(tasksOf(teamId), orderBy('createdAt', 'desc')),
    (snap) =>
      next(
        snap.docs.map((d) => {
          const t = d.data({ serverTimestamps: 'estimate' });
          return {
            id: d.id,
            title: t.title,
            done: t.done === true,
            assignee: t.assignee ?? null,
            createdBy: t.createdBy,
            createdAt: t.createdAt ?? null,
            updatedAt: t.updatedAt ?? null,
            attachment: t.attachment ?? null,
          };
        }),
      ),
    fail,
  );
}

function checkTitle(title: string): string {
  const clean = title.trim();
  if (clean.length === 0 || clean.length > limits.taskTitle) throw new AppError('app/title-length');
  return clean;
}

export async function addTask(teamId: string, uid: string, title: string): Promise<void> {
  await addDoc(tasksOf(teamId), {
    title: checkTitle(title),
    done: false,
    assignee: null,
    createdBy: uid,
    createdAt: serverTimestamp(),
    updatedAt: serverTimestamp(),
    attachment: null,
  });
}

export function setDone(teamId: string, taskId: string, done: boolean): Promise<void> {
  return updateDoc(doc(tasksOf(teamId), taskId), { done, updatedAt: serverTimestamp() });
}

export function setAssignee(teamId: string, taskId: string, assignee: string | null): Promise<void> {
  return updateDoc(doc(tasksOf(teamId), taskId), { assignee, updatedAt: serverTimestamp() });
}

async function removeFile(path: string): Promise<void> {
  try {
    await deleteObject(storageRef(storage, path));
  } catch (e) {
    if (errorCode(e) !== 'storage/object-not-found') throw e;
  }
}

export async function deleteTask(teamId: string, task: Task): Promise<void> {
  await deleteDoc(doc(tasksOf(teamId), task.id));
  // The file goes after the task: a member who may not delete the task keeps its file too.
  if (task.attachment) await removeFile(task.attachment.path).catch(() => {});
}

// Uploads a task's one attachment, reporting progress from 0 to 1, then records it on the task.
// A previous file of another name is deleted afterwards.
export async function attachFile(
  teamId: string,
  task: Task,
  file: File,
  progress: (fraction: number) => void,
): Promise<void> {
  if (file.size >= limits.attachmentBytes) throw new AppError('app/file-too-big', { limit: '10 MB' });
  const name = file.name.replace(/[/\\#?[\]*]/g, '_') || 'file';
  const path = `teams/${teamId}/tasks/${task.id}/${name}`;
  const upload = uploadBytesResumable(storageRef(storage, path), file, {
    contentType: file.type || 'application/octet-stream',
  });
  await new Promise<void>((resolve, reject) => {
    upload.on(
      'state_changed',
      (s) => progress(s.totalBytes ? s.bytesTransferred / s.totalBytes : 0),
      reject,
      () => resolve(),
    );
  });
  const attachment: Attachment = { name: file.name, path };
  await updateDoc(doc(tasksOf(teamId), task.id), { attachment, updatedAt: serverTimestamp() });
  if (task.attachment && task.attachment.path !== path) await removeFile(task.attachment.path).catch(() => {});
}

// The attachment's download link. Asking for it is a read under the Storage rules (members only);
// the link itself then works in any browser tab.
export function attachmentURL(path: string): Promise<string> {
  return getDownloadURL(storageRef(storage, path));
}
