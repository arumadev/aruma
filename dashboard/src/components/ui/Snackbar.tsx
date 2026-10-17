'use client';

import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react';
import { useTranslations } from 'use-intl';
import { IconButton } from './Button';
import styles from './Snackbar.module.css';

interface SnackbarMessage {
  id: number;
  text: string;
}

const SnackbarContext = createContext<((text: string) => void) | null>(null);

const TIMEOUT_MS = 5000;

/**
 * M3 snackbars for success messages. The region is always in the DOM as a polite live
 * region, so screen readers announce each message; the timer pauses on hover and focus.
 */
export function SnackbarProvider({ children }: { children: ReactNode }) {
  const [messages, setMessages] = useState<SnackbarMessage[]>([]);
  const nextId = useRef(0);
  const show = useCallback((text: string) => {
    nextId.current += 1;
    const id = nextId.current;
    // One at a time, as M3 recommends: a new message replaces the current one.
    setMessages([{ id, text }]);
  }, []);
  const dismiss = useCallback((id: number) => setMessages((list) => list.filter((m) => m.id !== id)), []);

  return (
    <SnackbarContext value={show}>
      {children}
      <div className={styles.region} role="status" aria-live="polite">
        {messages.map((m) => (
          <Snackbar key={m.id} message={m} onDismiss={dismiss} />
        ))}
      </div>
    </SnackbarContext>
  );
}

function Snackbar({ message, onDismiss }: { message: SnackbarMessage; onDismiss: (id: number) => void }) {
  const t = useTranslations('common');
  const [paused, setPaused] = useState(false);
  useEffect(() => {
    if (paused) return;
    const timer = setTimeout(() => onDismiss(message.id), TIMEOUT_MS);
    return () => clearTimeout(timer);
  }, [paused, message.id, onDismiss]);

  return (
    <div
      className={styles.snackbar}
      onPointerEnter={() => setPaused(true)}
      onPointerLeave={() => setPaused(false)}
      onFocus={() => setPaused(true)}
      onBlur={() => setPaused(false)}
    >
      <span className={styles.text}>{message.text}</span>
      <IconButton icon="close" label={t('dismiss')} className={styles.close} onPress={() => onDismiss(message.id)} />
    </div>
  );
}

/** Returns a function that shows a snackbar message. */
export function useSnackbar(): (text: string) => void {
  const show = useContext(SnackbarContext);
  if (!show) throw new Error('useSnackbar must be used inside SnackbarProvider');
  return show;
}
