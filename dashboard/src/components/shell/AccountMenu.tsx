'use client';

import { useTranslations } from 'use-intl';
import { IconButton } from '@/components/ui/Button';
import { Menu, MenuItem } from '@/components/ui/Menu';
import { routes } from '@/lib/routes';
import { useSession } from '@/lib/session/SessionProvider';

export function AccountMenu() {
  const t = useTranslations('shell');
  const { account, signOut } = useSession();
  const label = account ? t('accountMenuFor', { email: account.email }) : t('accountMenu');
  return (
    <Menu label={label} trigger={<IconButton icon="person" label={label} />}>
      <MenuItem id="account" icon="person" href={routes.account}>
        {t('account')}
      </MenuItem>
      <MenuItem id="limits" icon="speed" href={routes.limits}>
        {t('limits')}
      </MenuItem>
      <MenuItem id="signout" icon="logout" onAction={() => void signOut()}>
        {t('signOut')}
      </MenuItem>
    </Menu>
  );
}
