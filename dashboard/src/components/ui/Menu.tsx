'use client';

import type { ReactNode } from 'react';
import { Menu as AriaMenu, MenuItem as AriaMenuItem, MenuTrigger, Popover, type MenuItemProps } from 'react-aria-components';
import { Icon, type IconName } from './Icon';
import styles from './Menu.module.css';

/** A menu opened by its trigger (usually an IconButton). */
export function Menu({ trigger, label, children }: { trigger: ReactNode; label: string; children: ReactNode }) {
  return (
    <MenuTrigger>
      {trigger}
      <Popover className={styles.popover} placement="bottom end" offset={4}>
        <AriaMenu aria-label={label} className={styles.menu}>
          {children}
        </AriaMenu>
      </Popover>
    </MenuTrigger>
  );
}

export function MenuItem({
  icon,
  children,
  danger,
  ...props
}: Omit<MenuItemProps, 'children' | 'className' | 'style'> & { icon?: IconName; children: string; danger?: boolean }) {
  return (
    <AriaMenuItem {...props} textValue={children} className={[styles.item, danger && styles.danger].filter(Boolean).join(' ')}>
      {icon && <Icon name={icon} />}
      <span>{children}</span>
    </AriaMenuItem>
  );
}
