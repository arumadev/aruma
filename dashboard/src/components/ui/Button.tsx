'use client';

import type { ReactNode } from 'react';
import {
  Button as AriaButton,
  Link as AriaLink,
  Tooltip,
  TooltipTrigger,
  type ButtonProps as AriaButtonProps,
  type LinkProps as AriaLinkProps,
} from 'react-aria-components';
import styles from './Button.module.css';
import { Icon, type IconName } from './Icon';

export type ButtonVariant = 'filled' | 'tonal' | 'outlined' | 'text' | 'danger';

interface CommonProps {
  variant?: ButtonVariant;
  icon?: IconName;
  children: ReactNode;
  className?: string;
}

const classes = (variant: ButtonVariant, className?: string) => [styles.button, styles[variant], className].filter(Boolean).join(' ');

export type ButtonProps = CommonProps & Omit<AriaButtonProps, 'children' | 'className' | 'style'>;

/** An M3 common button. `isPending` keeps the label and shows progress while a request runs. */
export function Button({ variant = 'filled', icon, children, className, isPending, ...props }: ButtonProps) {
  return (
    <AriaButton {...props} isPending={isPending} className={classes(variant, className)}>
      {isPending ? <span className={styles.spinner} aria-hidden="true" /> : icon && <Icon name={icon} size={18} />}
      <span>{children}</span>
    </AriaButton>
  );
}

export type LinkButtonProps = CommonProps & Omit<AriaLinkProps, 'children' | 'className' | 'style'>;

/** A link that looks like a button, for navigation. */
export function LinkButton({ variant = 'filled', icon, children, className, ...props }: LinkButtonProps) {
  return (
    <AriaLink {...props} className={classes(variant, className)}>
      {icon && <Icon name={icon} size={18} />}
      <span>{children}</span>
    </AriaLink>
  );
}

export interface IconButtonProps extends Omit<AriaButtonProps, 'children' | 'className' | 'style' | 'aria-label'> {
  icon: IconName;
  /** The accessible name, also shown as a tooltip. */
  label: string;
  variant?: 'standard' | 'tonal';
  className?: string;
}

/** An M3 icon button. Its label is the accessible name and a tooltip. */
export function IconButton({ icon, label, variant = 'standard', className, ...props }: IconButtonProps) {
  return (
    <TooltipTrigger delay={600}>
      <AriaButton {...props} aria-label={label} className={[styles.iconButton, styles[variant], className].filter(Boolean).join(' ')}>
        <Icon name={icon} />
      </AriaButton>
      <Tooltip className={styles.tooltip} offset={6}>
        {label}
      </Tooltip>
    </TooltipTrigger>
  );
}
