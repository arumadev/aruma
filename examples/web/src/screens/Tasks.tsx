import { useMemo, useRef, useState, type FormEvent } from 'react';
import { fromPromise, useLive } from '../live';
import { useI18n } from '../i18n';
import { useUser } from '../session';
import { addTask, attachFile, attachmentURL, deleteTask, setAssignee, setDone, watchTasks } from '../data/tasks';
import type { Member, Task, Team } from '../data/model';
import { Empty, ErrorText, Loading, Progress } from '../components/ui';

function AttachmentLink({ path, name }: { path: string; name: string }) {
  const { t } = useI18n();
  const { value: url, error } = useLive(path, fromPromise(() => attachmentURL(path)));
  if (error) return <ErrorText error={error} />;
  if (!url) return <span className="muted small-text">{t('tasks.linkLoading')}</span>;
  return (
    <a className="attachment" href={url} target="_blank" rel="noopener noreferrer" data-testid="attachment-link">
      <span aria-hidden="true">📎</span> {name}
    </a>
  );
}

function TaskItem({ teamId, task, team, members }: { teamId: string; task: Task; team: Team; members: Member[] }) {
  const { t } = useI18n();
  const user = useUser();
  const [error, setError] = useState<unknown>(null);
  const [upload, setUpload] = useState<number | null>(null);
  const [pendingDone, setPendingDone] = useState<boolean | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const creator = members.find((m) => m.uid === task.createdBy)?.name ?? '…';
  const canDelete = task.createdBy === user.uid || team.owner === user.uid;

  async function act(action: () => Promise<void>) {
    setError(null);
    try {
      await action();
    } catch (e) {
      setError(e);
    }
  }

  // The box shows the click at once, until the server has the write (or refused it).
  async function toggle(done: boolean) {
    setPendingDone(done);
    await act(() => setDone(teamId, task.id, done));
    setPendingDone(null);
  }

  async function attach(file: File | undefined) {
    if (!file) return;
    setUpload(0);
    await act(() => attachFile(teamId, task, file, setUpload));
    setUpload(null);
    if (fileInput.current) fileInput.current.value = '';
  }

  return (
    <li className={`task${task.done ? ' done' : ''}`} data-testid="task">
      <label className="check">
        <input type="checkbox" checked={pendingDone ?? task.done} aria-label={t('tasks.markDone', { title: task.title })}
          onChange={(e) => void toggle(e.target.checked)} />
        <span className="task-title">{task.title}</span>
      </label>
      <div className="task-meta">
        <span className="muted small-text">{t('tasks.by', { name: creator })}</span>
        <label className="inline-field">
          <span className="muted small-text">{t('tasks.assignee')}</span>
          <select value={task.assignee ?? ''} onChange={(e) => void act(() => setAssignee(teamId, task.id, e.target.value || null))}>
            <option value="">{t('tasks.nobody')}</option>
            {members.map((m) => <option key={m.uid} value={m.uid}>{m.name}</option>)}
          </select>
        </label>
        {task.attachment && <AttachmentLink path={task.attachment.path} name={task.attachment.name} />}
        {upload !== null && <Progress value={upload} label={t('tasks.uploading', { percent: Math.round(upload * 100) })} />}
      </div>
      <div className="task-actions">
        <label className="button small file-button">
          {task.attachment ? t('tasks.replace') : t('tasks.attach')}
          <input ref={fileInput} type="file" className="visually-hidden" disabled={upload !== null}
            onChange={(e) => void attach(e.target.files?.[0])} />
        </label>
        {canDelete && (
          <button type="button" className="small danger" aria-label={t('tasks.deleteLabel', { title: task.title })}
            onClick={() => void act(() => deleteTask(teamId, task))}>
            {t('tasks.delete')}
          </button>
        )}
      </div>
      <ErrorText error={error} onDismiss={() => setError(null)} />
    </li>
  );
}

export function Tasks({ teamId, team, members }: { teamId: string; team: Team; members: Member[] }) {
  const { t } = useI18n();
  const user = useUser();
  const { value: tasks, error: listError } = useLive<Task[]>(teamId, (n, f) => watchTasks(teamId, n, f));
  const [title, setTitle] = useState('');
  const [addError, setAddError] = useState<unknown>(null);

  // Open tasks first, each group newest first (the query's order).
  const sorted = useMemo(() => (tasks ? [...tasks.filter((x) => !x.done), ...tasks.filter((x) => x.done)] : undefined), [tasks]);
  const open = tasks?.filter((x) => !x.done).length ?? 0;

  async function add(e: FormEvent) {
    e.preventDefault();
    setAddError(null);
    const value = title;
    setTitle('');
    try {
      await addTask(teamId, user.uid, value);
    } catch (err) {
      setTitle(value);
      setAddError(err);
    }
  }

  return (
    <section className="card" aria-labelledby="tasks-title">
      <div className="section-head">
        <h2 id="tasks-title">{t('team.tasks')}</h2>
        {tasks && <span className="muted" data-testid="task-summary">{t('tasks.summary', { open, done: tasks.length - open })}</span>}
      </div>
      <form onSubmit={add} className="row-form">
        <label className="field grow">
          <span className="visually-hidden">{t('tasks.newTitle')}</span>
          <input name="title" placeholder={t('tasks.newTitle')} maxLength={200} value={title} onChange={(e) => setTitle(e.target.value)} />
        </label>
        <button className="primary" disabled={!title.trim()}>{t('tasks.add')}</button>
      </form>
      <ErrorText error={addError} onDismiss={() => setAddError(null)} />
      {listError ? <ErrorText error={listError} /> : sorted === undefined ? <Loading /> : sorted.length === 0 ? (
        <Empty>{t('tasks.empty')}</Empty>
      ) : (
        <ul className="task-list">
          {sorted.map((task) => <TaskItem key={task.id} teamId={teamId} task={task} team={team} members={members} />)}
        </ul>
      )}
    </section>
  );
}
