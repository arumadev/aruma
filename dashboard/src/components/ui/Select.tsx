'use client';

import type { Key, ReactNode } from 'react';
import {
  Button,
  Label,
  ListBox,
  ListBoxItem,
  Popover,
  Select as AriaSelect,
  SelectValue,
  Text,
  type SelectProps as AriaSelectProps,
} from 'react-aria-components';
import { Icon } from './Icon';
import styles from './Select.module.css';

export interface SelectOption {
  id: string;
  label: string;
  /** Secondary text, such as an identifier, shown left to right. */
  detail?: string;
}

export interface SelectProps
  extends Omit<AriaSelectProps<SelectOption>, 'children' | 'className' | 'style' | 'items' | 'value' | 'onChange'> {
  label: string;
  /** Visually hides the label while keeping it as the accessible name. */
  hideLabel?: boolean;
  options: SelectOption[];
  value: string | null;
  onChange: (id: string) => void;
  description?: ReactNode;
  className?: string;
}

/** An M3 outlined select menu. */
export function Select({ label, hideLabel, options, value, onChange, description, className, ...props }: SelectProps) {
  return (
    <AriaSelect
      {...props}
      value={value}
      onChange={(key: Key | null) => key !== null && onChange(String(key))}
      className={[styles.select, className].filter(Boolean).join(' ')}
    >
      <Label className={hideLabel ? styles.hiddenLabel : styles.label}>{label}</Label>
      <Button className={styles.trigger}>
        <SelectValue className={styles.value} />
        <Icon name="expandMore" />
      </Button>
      {description && (
        <Text slot="description" className={styles.description}>
          {description}
        </Text>
      )}
      <Popover className={styles.popover} offset={4}>
        <ListBox items={options} className={styles.list}>
          {(option) => (
            <ListBoxItem id={option.id} textValue={option.label} className={styles.option}>
              <span>{option.label}</span>
              {option.detail && (
                <bdi dir="ltr" className={styles.detail}>
                  {option.detail}
                </bdi>
              )}
            </ListBoxItem>
          )}
        </ListBox>
      </Popover>
    </AriaSelect>
  );
}
