import type en from '../../../messages/en.json';

// Message keys are type-checked against the English catalog; a unit test keeps the Arabic
// catalog in step with it.
declare module 'use-intl' {
  interface AppConfig {
    Messages: typeof en;
  }
}
