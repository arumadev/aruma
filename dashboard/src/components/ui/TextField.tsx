'use client';

import type { ReactNode } from 'react';
import {
  FieldError,
  Input,
  Label,
  Text,
  TextArea,
  TextField as AriaTextField,
  type TextFieldProps as AriaTextFieldProps,
} from 'react-aria-components';
import styles from './TextField.module.css';

export interface TextFieldProps extends Omit<AriaTextFieldProps, 'children' | 'className' | 'style' | 'validationBehavior'> {
  label: string;
  description?: ReactNode;
  errorMessage?: string;
  /** Identifiers, emails and keys are typed left to right whatever the page direction. */
  ltr?: boolean;
  multiline?: boolean;
  rows?: number;
  placeholder?: string;
  /** Shown at the end of the field, such as an availability status. */
  trailing?: ReactNode;
  className?: string;
}

/** An M3 outlined text field. Errors come from the caller, so they are always localized. */
export function TextField({
  label,
  description,
  errorMessage,
  ltr,
  multiline,
  rows = 4,
  placeholder,
  trailing,
  className,
  ...props
}: TextFieldProps) {
  const input = multiline ? (
    <TextArea className={styles.input} rows={rows} placeholder={placeholder} dir={ltr ? 'ltr' : undefined} />
  ) : (
    <Input className={styles.input} placeholder={placeholder} dir={ltr ? 'ltr' : undefined} />
  );
  return (
    <AriaTextField
      {...props}
      isInvalid={props.isInvalid ?? Boolean(errorMessage)}
      validationBehavior="aria"
      className={[styles.field, className].filter(Boolean).join(' ')}
    >
      <Label className={styles.label}>{label}</Label>
      <div className={styles.control}>
        {input}
        {trailing && <span className={styles.trailing}>{trailing}</span>}
      </div>
      {description && !errorMessage && (
        <Text slot="description" className={styles.supporting}>
          {description}
        </Text>
      )}
      <FieldError className={`${styles.supporting} ${styles.error}`}>{errorMessage}</FieldError>
    </AriaTextField>
  );
}
