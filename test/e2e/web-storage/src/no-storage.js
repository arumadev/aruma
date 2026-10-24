// A bundle with the Storage plugin but no @firebase/storage module: the build must fail.
import { initializeApp } from 'firebase/app';

window.app = initializeApp({ projectId: 'x', apiKey: 'x', appId: 'x' });
