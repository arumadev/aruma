'use client';

import { useTranslations } from 'use-intl';
import { IconButton } from './Button';
import styles from './Code.module.css';
import { useSnackbar } from './Snackbar';

/**
 * Identifiers, keys, emails and code read left to right in both languages. The bidi isolate
 * keeps `1:123:web:abc` from being reordered inside Arabic text.
 */
export function Ltr({ children, mono = true }: { children: string; mono?: boolean }) {
  return (
    <bdi dir="ltr" className={mono ? styles.code : styles.ltr}>
      {children}
    </bdi>
  );
}

/** Copies `value` to the clipboard and confirms with a snackbar. */
export function CopyButton({ value, label }: { value: string; label: string }) {
  const t = useTranslations('common');
  const notify = useSnackbar();
  return (
    <IconButton
      icon="copy"
      label={label}
      onPress={() => {
        navigator.clipboard.writeText(value).then(
          () => notify(t('copied')),
          () => notify(t('copyFailed')),
        );
      }}
    />
  );
}

/** A block of code or file content with a copy button. */
export function CodeBlock({ code, copyLabel, label }: { code: string; copyLabel: string; label: string }) {
  return (
    <figure className={styles.block}>
      <figcaption className={styles.caption}>
        <span>{label}</span>
        <CopyButton value={code} label={copyLabel} />
      </figcaption>
      <pre dir="ltr" className={styles.pre} tabIndex={0}>
        <code>{code}</code>
      </pre>
    </figure>
  );
}
