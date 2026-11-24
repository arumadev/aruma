import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import en from './en.json';
import ar from './ar.json';

export type Lang = 'en' | 'ar';
export type MessageKey = keyof typeof en;
export type Translate = (key: MessageKey, vars?: Record<string, string | number>) => string;

const dictionaries: Record<Lang, Record<MessageKey, string>> = { en, ar };
const rtl: Record<Lang, boolean> = { en: false, ar: true };
const storageKey = 'team-board.lang';

// Browser storage can be missing or throw (private windows, blocked site data): the app then
// falls back to the browser's language and forgets the choice on reload.
function savedLang(): Lang | null {
  try {
    const v = localStorage.getItem(storageKey);
    return v === 'en' || v === 'ar' ? v : null;
  } catch {
    return null;
  }
}

function initialLang(): Lang {
  return savedLang() ?? (navigator.language.toLowerCase().startsWith('ar') ? 'ar' : 'en');
}

export function format(template: string, vars?: Record<string, string | number>): string {
  if (!vars) return template;
  return template.replace(/\{(\w+)\}/g, (m, name: string) => (name in vars ? String(vars[name]) : m));
}

interface I18n {
  lang: Lang;
  setLang: (lang: Lang) => void;
  t: Translate;
  otherLang: Lang;
  nameOf: (lang: Lang) => string;
}

const Ctx = createContext<I18n | null>(null);

export function I18nProvider({ children }: { children: ReactNode }) {
  const [lang, setLangState] = useState<Lang>(initialLang);

  useEffect(() => {
    document.documentElement.lang = lang;
    document.documentElement.dir = rtl[lang] ? 'rtl' : 'ltr';
    document.title = dictionaries[lang]['app.title'];
  }, [lang]);

  const setLang = useCallback((next: Lang) => {
    setLangState(next);
    try {
      localStorage.setItem(storageKey, next);
    } catch {
      // Not remembered in this browser; the switch still works for this visit.
    }
  }, []);

  const value = useMemo<I18n>(() => {
    const dict = dictionaries[lang];
    return {
      lang,
      setLang,
      t: (key, vars) => format(dict[key] ?? en[key], vars),
      otherLang: lang === 'en' ? 'ar' : 'en',
      nameOf: (l) => dictionaries[l]['language.name'],
    };
  }, [lang, setLang]);

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useI18n(): I18n {
  const v = useContext(Ctx);
  if (!v) throw new Error('useI18n outside I18nProvider');
  return v;
}
