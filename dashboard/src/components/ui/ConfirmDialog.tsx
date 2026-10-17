'use client';

import { useState, type ReactNode } from 'react';
import { useTranslations } from 'use-intl';
import { useErrorText } from '@/lib/errors';
import { Banner } from './Banner';
import { Button } from './Button';
import { Dialog } from './Dialog';

export interface ConfirmDialogProps {
  title: string;
  children: ReactNode;
  confirmLabel: string;
  /** Destructive actions get the error colour. */
  danger?: boolean;
  /** Extra condition for the confirm button, such as a typed confirmation. */
  canConfirm?: boolean;
  /** Runs the action; the dialog stays open and shows the error if it fails. */
  onConfirm: () => Promise<void>;
  onClose: () => void;
}

/** An alert dialog that asks before an action and shows its progress and failure. */
export function ConfirmDialog({ title, children, confirmLabel, danger, canConfirm = true, onConfirm, onClose }: ConfirmDialogProps) {
  const t = useTranslations('common');
  const errorText = useErrorText();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);

  async function confirm() {
    if (!canConfirm) return;
    setPending(true);
    setError(null);
    try {
      await onConfirm();
      onClose();
    } catch (err) {
      setError(err);
      setPending(false);
    }
  }

  return (
    <Dialog
      isOpen
      role="alertdialog"
      onOpenChange={(open) => !open && onClose()}
      title={title}
      isBusy={pending}
      onSubmit={confirm}
      actions={
        <>
          <Button variant="text" onPress={onClose} isDisabled={pending}>
            {t('cancel')}
          </Button>
          <Button type="submit" variant={danger ? 'danger' : 'filled'} isPending={pending} isDisabled={!canConfirm}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      {children}
      {Boolean(error) && <Banner tone="error">{errorText.message(error)}</Banner>}
    </Dialog>
  );
}
