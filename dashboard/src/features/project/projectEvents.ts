// Tells parts of the shell outside a project's pages (the project switcher) that a project
// changed, such as a rename, deletion or restore.

const target = new EventTarget();
const CHANGED = 'project-changed';

export function notifyProjectChanged(): void {
  target.dispatchEvent(new Event(CHANGED));
}

export function onProjectChanged(listener: () => void): () => void {
  target.addEventListener(CHANGED, listener);
  return () => target.removeEventListener(CHANGED, listener);
}
