// A bundle that rewrites the npm Storage SDK but also loads the gstatic copy, which no bundler ever
// sees: the build must fail. The comments keep webpack and Vite from refusing the URL themselves.
import { getStorage } from 'firebase/storage';

window.storage = getStorage;
window.cdn = () => import(/* webpackIgnore: true */ /* @vite-ignore */ 'https://www.gstatic.com/firebasejs/13.1.0/firebase-storage.js');
