import assert from 'node:assert/strict';
import { lstat, mkdtemp, mkdir, rename, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { identityOf } from '../src/file-identity.mjs';
import { readBoundedFile } from '../src/safe-open.mjs';

const windowsTest = process.platform === 'win32' ? test : test.skip;

async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), 'kuai-safe-open-windows-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  return root;
}

windowsTest('rejects root, ancestor, and final reparse points', async (t) => {
  const root = await fixture(t);
  const outside = await fixture(t);
  await writeFile(join(outside, 'events.jsonl'), 'attacker');

  const rootJunction = `${root}-junction`;
  t.after(() => rm(rootJunction, { recursive: true, force: true }));
  await symlink(root, rootJunction, 'junction');
  await assert.rejects(
    readBoundedFile(rootJunction, 'missing', { maxBytes: 64 }),
    /symbolic link/,
  );

  await mkdir(join(root, 'nested'));
  await symlink(outside, join(root, 'nested', 'ancestor'), 'junction');
  await assert.rejects(
    readBoundedFile(root, 'nested/ancestor/events.jsonl', { maxBytes: 64 }),
    /symbolic link/,
  );

  await symlink(join(outside, 'events.jsonl'), join(root, 'final-link'), 'file');
  await assert.rejects(
    readBoundedFile(root, 'final-link', { maxBytes: 64 }),
    /symbolic link/,
  );
});

windowsTest('provides non-zero stable identities that distinguish replacement', async (t) => {
  const root = await fixture(t);
  const target = join(root, 'events.jsonl');
  const replacement = join(root, 'replacement.jsonl');
  await writeFile(target, 'trusted');
  await writeFile(replacement, 'attacker');

  const before = identityOf(await lstat(target, { bigint: true }));
  await rename(target, join(root, 'original.jsonl'));
  await rename(replacement, target);
  const after = identityOf(await lstat(target, { bigint: true }));

  assert.notEqual(before, after);
});
