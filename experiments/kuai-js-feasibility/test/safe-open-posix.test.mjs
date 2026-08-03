import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { mkdtemp, mkdir, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { promisify } from 'node:util';

import { readBoundedFile } from '../src/safe-open.mjs';

const execFileAsync = promisify(execFile);
const posixTest = process.platform === 'win32' ? test.skip : test;

async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), 'kuai-safe-open-posix-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  return root;
}

posixTest('rejects a FIFO before attempting to open it', async (t) => {
  const root = await fixture(t);
  const fifo = join(root, 'events.pipe');
  await execFileAsync('mkfifo', [fifo]);

  await assert.rejects(
    readBoundedFile(root, 'events.pipe', { maxBytes: 64 }),
    /final component is not a regular file/,
  );
});

posixTest('rejects final and intermediate symbolic links', async (t) => {
  const root = await fixture(t);
  const outside = await mkdtemp(join(tmpdir(), 'kuai-safe-open-outside-'));
  t.after(() => rm(outside, { recursive: true, force: true }));
  await writeFile(join(outside, 'events.jsonl'), 'attacker');
  await symlink(join(outside, 'events.jsonl'), join(root, 'final-link'));
  await mkdir(join(root, 'nested'));
  await symlink(outside, join(root, 'nested', 'middle-link'));

  await assert.rejects(
    readBoundedFile(root, 'final-link', { maxBytes: 64 }),
    /symbolic link/,
  );
  await assert.rejects(
    readBoundedFile(root, 'nested/middle-link/events.jsonl', { maxBytes: 64 }),
    /symbolic link/,
  );
});
