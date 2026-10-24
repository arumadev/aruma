// @firebase/component's index.ts (firebase-js-sdk 0c331cc, packages/component/index.ts) with its
// runtime exports only. run.sh copies it beside that package's src/, and vitest.config.mjs
// resolves the storage sources' @firebase/component to it. The upstream file also re-exports
// type-only names without the type modifier, which a browser rejects as missing ESM exports
// once the TypeScript is transpiled file by file; the monorepo's build never meets that.
export { Component } from './src/component';
export { ComponentContainer } from './src/component_container';
export { Provider } from './src/provider';
export { ComponentType, InstantiationMode } from './src/types';
