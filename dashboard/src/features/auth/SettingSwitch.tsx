'use client';

import { useId } from 'react';
import { Switch } from '@/components/ui/Switch';
import styles from './SettingSwitch.module.css';

/** A switch for one setting, with a line explaining what it does. */
export function SettingSwitch({
  label,
  description,
  isSelected,
  onChange,
  isDisabled,
}: {
  label: string;
  description: string;
  isSelected: boolean;
  onChange: (selected: boolean) => void;
  isDisabled?: boolean;
}) {
  const id = useId();
  return (
    <div className={styles.setting}>
      <Switch isSelected={isSelected} onChange={onChange} isDisabled={isDisabled} aria-describedby={id}>
        {label}
      </Switch>
      <p id={id} className={styles.description}>
        {description}
      </p>
    </div>
  );
}
