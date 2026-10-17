'use client';

import { Switch as AriaSwitch, type SwitchProps as AriaSwitchProps } from 'react-aria-components';
import styles from './Switch.module.css';

/** An M3 switch with its label. */
export function Switch({ children, ...props }: Omit<AriaSwitchProps, 'children' | 'className' | 'style'> & { children: string }) {
  return (
    <AriaSwitch {...props} className={styles.switch}>
      <span className={styles.track} aria-hidden="true">
        <span className={styles.handle} />
      </span>
      <span>{children}</span>
    </AriaSwitch>
  );
}
