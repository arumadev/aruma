// A server entry that uses Storage, for the SSR build that must fail when the SDK is left external.
import { getStorage } from 'firebase/storage';

export const storage = getStorage;
