// Copies web/build into internal/companion/build so the Go program can embed it.
import { cpSync, mkdirSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const from = join(root, 'build');
const to = join(root, '..', 'internal', 'companion', 'build');

mkdirSync(to, { recursive: true });
for (const name of readdirSync(to)) rmSync(join(to, name), { recursive: true, force: true });
cpSync(from, to, { recursive: true });
writeFileSync(join(to, '.keep'), '');
console.log(`embedded ${from} -> ${to}`);
