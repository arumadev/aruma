// Rebuilds ../manifest.json from the corpus files as they are: each file's
// SHA-256 and its header's oracle, generator, date and versions. The recorders
// call the same function after they write; run this only after a deliberate
// hand edit of a corpus file (and say why in the commit).
import { rebuildManifest } from './lib.mjs';

rebuildManifest();
