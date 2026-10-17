import { DEFAULT_LOCALE, isLocale, type Locale } from './locales';

// The interface language as an external store over localStorage, for useSyncExternalStore:
// prerendering and hydration see the default (Arabic), the client then reads the saved value,
// and a change in another tab is picked up through the storage event.

const STORAGE_KEY = 'aruma.locale';
const listeners = new Set<() => void>();
// Used when storage is unavailable (private browsing, blocked cookies).
let memory: Locale | null = null;

function read(): Locale | null {
  try {
    const value = localStorage.getItem(STORAGE_KEY);
    return isLocale(value) ? value : null;
  } catch {
    return null;
  }
}

export function getLocaleSnapshot(): Locale {
  return memory ?? read() ?? DEFAULT_LOCALE;
}

export function getServerLocaleSnapshot(): Locale {
  return DEFAULT_LOCALE;
}

export function subscribeLocale(listener: () => void): () => void {
  listeners.add(listener);
  const onStorage = (event: StorageEvent) => {
    if (event.key === STORAGE_KEY) {
      memory = null;
      listener();
    }
  };
  window.addEventListener('storage', onStorage);
  return () => {
    listeners.delete(listener);
    window.removeEventListener('storage', onStorage);
  };
}

export function saveLocale(locale: Locale): void {
  try {
    localStorage.setItem(STORAGE_KEY, locale);
    memory = null;
  } catch {
    memory = locale;
  }
  for (const listener of listeners) listener();
}
