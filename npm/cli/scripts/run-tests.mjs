import { spawnSync } from 'node:child_process';
import { readdirSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const testDirectory = fileURLToPath(new URL('../test/', import.meta.url));

function collectTests(directory) {
  const tests = [];

  for (const entry of readdirSync(directory, { withFileTypes: true })
    .sort((left, right) => left.name.localeCompare(right.name))) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      tests.push(...collectTests(path));
    } else if (entry.isFile() && entry.name.endsWith('.test.mjs')) {
      tests.push(path);
    }
  }

  return tests;
}

const result = spawnSync(process.execPath, ['--test', ...collectTests(testDirectory)], {
  shell: false,
  stdio: 'inherit',
});

if (result.error) {
  throw result.error;
}

process.exitCode = result.status ?? 1;
