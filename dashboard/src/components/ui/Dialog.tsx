'use client';

import type { FormEvent, ReactNode } from 'react';
import { Dialog as AriaDialog, Heading, Modal, ModalOverlay } from 'react-aria-components';
import styles from './Dialog.module.css';

export interface DialogProps {
  isOpen: boolean;
  onOpenChange: (isOpen: boolean) => void;
  title: string;
  children: ReactNode;
  /** The buttons at the end of the dialog. */
  actions: ReactNode;
  /** Wraps the content in a form so Enter submits; the submit button goes in `actions`. */
  onSubmit?: (event: FormEvent<HTMLFormElement>) => void;
  /** Alert dialogs interrupt for a confirmation and cannot be dismissed by clicking outside. */
  role?: 'dialog' | 'alertdialog';
  /** Prevents closing while a request is running. */
  isBusy?: boolean;
  wide?: boolean;
}

/** An M3 basic dialog on a modal overlay. Focus is trapped and returned on close. */
export function Dialog({ isOpen, onOpenChange, title, children, actions, onSubmit, role = 'dialog', isBusy, wide }: DialogProps) {
  const dismissable = role === 'dialog' && !isBusy;
  const body = (
    <>
      <Heading slot="title" className={styles.title}>
        {title}
      </Heading>
      <div className={styles.content}>{children}</div>
      <div className={styles.actions}>{actions}</div>
    </>
  );
  return (
    <ModalOverlay
      isOpen={isOpen}
      onOpenChange={(open) => !isBusy && onOpenChange(open)}
      isDismissable={dismissable}
      isKeyboardDismissDisabled={isBusy}
      className={styles.overlay}
    >
      <Modal className={[styles.modal, wide && styles.wide].filter(Boolean).join(' ')}>
        <AriaDialog role={role} className={styles.dialog}>
          {onSubmit ? (
            <form
              className={styles.form}
              noValidate
              onSubmit={(event) => {
                event.preventDefault();
                onSubmit(event);
              }}
            >
              {body}
            </form>
          ) : (
            body
          )}
        </AriaDialog>
      </Modal>
    </ModalOverlay>
  );
}
