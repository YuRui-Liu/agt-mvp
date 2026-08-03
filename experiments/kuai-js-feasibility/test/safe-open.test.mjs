import assert from 'node:assert/strict';
import { mkdtemp, mkdir, rename, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import test from 'node:test';

import { readBoundedFile } from '../src/safe-open.mjs';

async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), 'kuai-safe-open-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  return root;
}

async function put(root, relative, contents) {
  const target = join(root, relative);
  await mkdir(dirname(target), { recursive: true });
  await writeFile(target, contents);
  return target;
}

test('reads a regular file through its opened descriptor', async (t) => {
  const root = await fixture(t);
  await put(root, 'session/events.jsonl', 'trusted');

  const data = await readBoundedFile(root, 'session/events.jsonl', { maxBytes: 64 });

  assert.equal(data.toString(), 'trusted');
});

test('rejects a file larger than the byte budget', async (t) => {
  const root = await fixture(t);
  await put(root, 'large.txt', '12345');

  await assert.rejects(
    readBoundedFile(root, 'large.txt', { maxBytes: 4 }),
    /file exceeds maxBytes/,
  );
});

test('rejects a final symbolic link', async (t) => {
  const root = await fixture(t);
  const outside = await put(root, 'outside.txt', 'attacker');
  await symlink(outside, join(root, 'events.jsonl'));

  await assert.rejects(
    readBoundedFile(root, 'events.jsonl', { maxBytes: 64 }),
    /symbolic link/,
  );
});

test('rejects deterministic ancestor replacement after snapshot', async (t) => {
  const root = await fixture(t);
  await put(root, 'session/events.jsonl', 'trusted');
  await put(root, 'attacker/events.jsonl', 'attacker');

  await assert.rejects(
    readBoundedFile(root, 'session/events.jsonl', {
      maxBytes: 64,
      afterSnapshot: async () => {
        await rename(join(root, 'session'), join(root, 'session.original'));
        await rename(join(root, 'attacker'), join(root, 'session'));
      },
    }),
    /path identity changed/,
  );
});

test('rejects deterministic final file replacement after snapshot', async (t) => {
  const root = await fixture(t);
  await put(root, 'session/events.jsonl', 'trusted');
  await put(root, 'session/attacker.jsonl', 'attacker');

  await assert.rejects(
    readBoundedFile(root, 'session/events.jsonl', {
      maxBytes: 64,
      afterSnapshot: async () => {
        await rename(join(root, 'session/events.jsonl'), join(root, 'session/original.jsonl'));
        await rename(join(root, 'session/attacker.jsonl'), join(root, 'session/events.jsonl'));
      },
    }),
    /file identity changed|path identity changed/,
  );
});

test('path replacement after open cannot change descriptor contents', async (t) => {
  const root = await fixture(t);
  await put(root, 'session/events.jsonl', 'trusted');
  await put(root, 'session/attacker.jsonl', 'attacker');

  const data = await readBoundedFile(root, 'session/events.jsonl', {
    maxBytes: 64,
    afterOpen: async () => {
      await rename(join(root, 'session/events.jsonl'), join(root, 'session/original.jsonl'));
      await rename(join(root, 'session/attacker.jsonl'), join(root, 'session/events.jsonl'));
    },
  });

  assert.equal(data.toString(), 'trusted');
});

test('rejects an already cancelled request', async (t) => {
  const root = await fixture(t);
  await put(root, 'events.jsonl', 'trusted');
  const controller = new AbortController();
  controller.abort();

  await assert.rejects(
    readBoundedFile(root, 'events.jsonl', { maxBytes: 64, signal: controller.signal }),
    { name: 'AbortError' },
  );
});

test('rejects cancellation raised by an injected boundary', async (t) => {
  const root = await fixture(t);
  await put(root, 'events.jsonl', 'trusted');
  const controller = new AbortController();

  await assert.rejects(
    readBoundedFile(root, 'events.jsonl', {
      maxBytes: 64,
      signal: controller.signal,
      afterSnapshot: async () => controller.abort(),
    }),
    { name: 'AbortError' },
  );
});
