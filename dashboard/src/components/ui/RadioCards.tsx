'use client';

import { Label, Radio, RadioGroup } from 'react-aria-components';
import { Icon, type IconName } from './Icon';
import styles from './RadioCards.module.css';

export interface RadioCardOption {
  id: string;
  label: string;
  description?: string;
  icon?: IconName;
}

/** A radio group shown as selectable cards, such as the platform choice. */
export function RadioCards({
  label,
  options,
  value,
  onChange,
}: {
  label: string;
  options: RadioCardOption[];
  value: string | null;
  onChange: (id: string) => void;
}) {
  return (
    <RadioGroup value={value} onChange={onChange} className={styles.group}>
      <Label className={styles.label}>{label}</Label>
      <div className={styles.options}>
        {options.map((option) => (
          <Radio key={option.id} value={option.id} className={styles.card}>
            {option.icon && (
              <span className={styles.icon}>
                <Icon name={option.icon} />
              </span>
            )}
            <span className={styles.text}>
              <span className={styles.title}>{option.label}</span>
              {option.description && <span className={styles.description}>{option.description}</span>}
            </span>
            <span className={styles.check}>
              <Icon name="checkCircle" size={20} />
            </span>
          </Radio>
        ))}
      </div>
    </RadioGroup>
  );
}
