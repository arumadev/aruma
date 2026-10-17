import type { IconName } from '@/components/ui/Icon';
import type { App, Platform } from '@/lib/api/types';

export interface PlatformInfo {
  icon: IconName;
  /** Key in the "platform" message namespace. */
  label: 'web' | 'android' | 'ios';
  /** The config file the platform downloads. */
  configFile: string;
}

export const PLATFORMS: Record<Platform, PlatformInfo> = {
  WEB: { icon: 'web', label: 'web', configFile: 'firebase-config.json' },
  ANDROID: { icon: 'android', label: 'android', configFile: 'google-services.json' },
  IOS: { icon: 'phone', label: 'ios', configFile: 'GoogleService-Info.plist' },
};

export const PLATFORM_ORDER: Platform[] = ['WEB', 'ANDROID', 'IOS'];

/** The identifier that ties an app to its store listing, if the platform has one. */
export const appIdentifier = (app: App): string | undefined => app.packageName ?? app.bundleId;
