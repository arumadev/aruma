import { useLayoutEffect, useRef, useState, type FormEvent } from 'react';
import { useLive } from '../live';
import { useI18n } from '../i18n';
import { useSession, useUser } from '../session';
import { sendMessage, watchMessages } from '../data/chat';
import { watchPresence } from '../data/presence';
import type { ChatMessage, Presence } from '../data/model';
import { limits } from '../data/model';
import { Empty, ErrorText, Loading } from '../components/ui';
import { UserAvatar } from '../components/UserAvatar';

export function Chat({ teamId }: { teamId: string }) {
  const { t, lang } = useI18n();
  const user = useUser();
  const { name } = useSession();
  const { value: messages, error: listError } = useLive<ChatMessage[]>(teamId, (n, f) => watchMessages(teamId, n, f));
  const { value: online, error: onlineError } = useLive<Presence[]>(teamId, (n, f) => watchPresence(teamId, n, f));
  const [text, setText] = useState('');
  const [sendError, setSendError] = useState<unknown>(null);
  const list = useRef<HTMLOListElement>(null);

  // Keep the newest message in view.
  useLayoutEffect(() => {
    const el = list.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [messages]);

  async function send(e: FormEvent) {
    e.preventDefault();
    const value = text;
    setText('');
    setSendError(null);
    try {
      await sendMessage(teamId, user.uid, name, value);
    } catch (err) {
      setText(value);
      setSendError(err);
    }
  }

  const time = new Intl.DateTimeFormat(lang, { hour: '2-digit', minute: '2-digit' });

  return (
    <div className="chat-layout">
      <aside className="card online" aria-labelledby="online-title">
        <h2 id="online-title">{t('chat.online')}</h2>
        {onlineError ? <ErrorText error={onlineError} /> : online === undefined ? <Loading /> : online.length === 0 ? (
          <Empty>{t('chat.nobody')}</Empty>
        ) : (
          <ul className="people" data-testid="online">
            {online.map((p) => (
              <li key={p.uid} data-uid={p.uid}>
                <span className="dot" aria-hidden="true" />
                <UserAvatar name={p.name} uid={p.uid} size={28} />
                <span>{p.name}{p.uid === user.uid ? ` (${t('chat.you')})` : ''}</span>
              </li>
            ))}
          </ul>
        )}
      </aside>
      <section className="card chat" aria-labelledby="chat-title">
        <h2 id="chat-title" className="visually-hidden">{t('chat.messages')}</h2>
        {listError ? <ErrorText error={listError} /> : messages === undefined ? <Loading /> : messages.length === 0 ? (
          <Empty>{t('chat.empty')}</Empty>
        ) : (
          <ol className="messages" ref={list} aria-live="polite" data-testid="messages">
            {messages.map((m) => (
              <li key={m.id} className={m.uid === user.uid ? 'mine' : undefined} data-testid="message">
                <UserAvatar name={m.name} uid={m.uid} size={28} />
                <div className="bubble">
                  <div className="bubble-head">
                    <strong>{m.name}</strong>
                    <time className="muted" dateTime={new Date(m.ts).toISOString()}>{time.format(m.ts)}</time>
                  </div>
                  <p className="message-text" dir="auto">{m.text}</p>
                </div>
              </li>
            ))}
          </ol>
        )}
        <form onSubmit={send} className="row-form composer">
          <label className="field grow">
            <span className="visually-hidden">{t('chat.placeholder')}</span>
            <input name="message" dir="auto" autoComplete="off" placeholder={t('chat.placeholder')} maxLength={limits.message}
              value={text} onChange={(e) => setText(e.target.value)} />
          </label>
          <button className="primary" disabled={!text.trim()}>{t('chat.send')}</button>
        </form>
        <ErrorText error={sendError} onDismiss={() => setSendError(null)} />
      </section>
    </div>
  );
}
