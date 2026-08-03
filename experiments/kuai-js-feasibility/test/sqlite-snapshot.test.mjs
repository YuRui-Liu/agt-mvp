import assert from 'node:assert/strict';
import { appendFile, lstat, mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { DatabaseSync } from 'node:sqlite';

import { runReadonlyOperation } from '../src/sqlite-snapshot.mjs';

const LARGE_INTEGER = 9_007_199_254_740_993n;

async function withDatabase(run, { wal = false } = {}) {
  const directory = await mkdtemp(join(tmpdir(), 'kuai-sqlite-'));
  const databasePath = join(directory, 'events.db');
  const database = new DatabaseSync(databasePath);

  try {
    if (wal) {
      database.exec('PRAGMA journal_mode = WAL');
      database.exec('PRAGMA wal_autocheckpoint = 0');
    }
    database.exec('CREATE TABLE events (id INTEGER PRIMARY KEY, value INTEGER NOT NULL)');
    database.prepare('INSERT INTO events (value) VALUES (?)').run(LARGE_INTEGER);
    await run({ databasePath, database });
  } finally {
    database.close();
    await rm(directory, { recursive: true, force: true });
  }
}

function generousOptions(overrides = {}) {
  return {
    maxMainBytes: 1024 * 1024,
    maxWalBytes: 1024 * 1024,
    maxShmBytes: 1024 * 1024,
    maxTotalBytes: 3 * 1024 * 1024,
    timeoutMs: 2_000,
    ...overrides,
  };
}

test('fixed SELECT preserves integers beyond Number.MAX_SAFE_INTEGER as BigInt', async () => {
  await withDatabase(async ({ databasePath }) => {
    const result = await runReadonlyOperation(
      databasePath,
      'getEventValues',
      {},
      generousOptions(),
    );

    assert.deepEqual(result, [{ id: 1n, value: LARGE_INTEGER }]);
    assert.equal(typeof result[0].value, 'bigint');
  });
});

test('read-only operation cannot be replaced with write or arbitrary SQL operations', async () => {
  await withDatabase(async ({ databasePath }) => {
    for (const operation of [
      'insert',
      'attach',
      'writePragma',
      'multipleStatements',
      'loadExtension',
    ]) {
      await assert.rejects(
        runReadonlyOperation(databasePath, operation, {}, generousOptions()),
        /unsupported read-only operation/,
      );
    }
  });
});

test('committed rows which only exist in WAL are visible to the read-only worker', async () => {
  await withDatabase(async ({ databasePath, database }) => {
    database.prepare('INSERT INTO events (value) VALUES (?)').run(7n);
    const walStats = await lstat(`${databasePath}-wal`);
    assert.ok(walStats.size > 0);

    const result = await runReadonlyOperation(
      databasePath,
      'countEvents',
      {},
      generousOptions(),
    );

    assert.deepEqual(result, { count: 2n });
  }, { wal: true });
});

test('main, WAL, SHM and aggregate budgets are enforced before worker startup', async () => {
  await withDatabase(async ({ databasePath }) => {
    const mainSize = Number((await lstat(databasePath, { bigint: true })).size);

    await assert.rejects(
      runReadonlyOperation(
        databasePath,
        'countEvents',
        {},
        generousOptions({ maxMainBytes: mainSize - 1 }),
      ),
      /main database exceeds budget/,
    );
    await assert.rejects(
      runReadonlyOperation(
        databasePath,
        'countEvents',
        {},
        generousOptions({ maxTotalBytes: mainSize - 1 }),
      ),
      /SQLite snapshot exceeds total budget/,
    );
  });

  await withDatabase(async ({ databasePath }) => {
    const walSize = Number((await lstat(`${databasePath}-wal`, { bigint: true })).size);
    const shmSize = Number((await lstat(`${databasePath}-shm`, { bigint: true })).size);

    await assert.rejects(
      runReadonlyOperation(
        databasePath,
        'countEvents',
        {},
        generousOptions({ maxWalBytes: walSize - 1 }),
      ),
      /WAL exceeds budget/,
    );
    await assert.rejects(
      runReadonlyOperation(
        databasePath,
        'countEvents',
        {},
        generousOptions({ maxShmBytes: shmSize - 1 }),
      ),
      /SHM exceeds budget/,
    );
  }, { wal: true });
});

test('result is discarded when a sidecar changes after the initial snapshot', async () => {
  await withDatabase(async ({ databasePath }) => {
    await assert.rejects(
      runReadonlyOperation(
        databasePath,
        'countEvents',
        {},
        generousOptions({
          beforePostcheck: async () => {
            await appendFile(`${databasePath}-shm`, Buffer.alloc(1));
          },
        }),
      ),
      /SQLite snapshot changed during query/,
    );
  }, { wal: true });
});

test('timeout terminates a recursive query and a subsequent query succeeds', async () => {
  await withDatabase(async ({ databasePath }) => {
    const startedAt = Date.now();
    await assert.rejects(
      runReadonlyOperation(
        databasePath,
        'longRead',
        {},
        generousOptions({ timeoutMs: 50 }),
      ),
      /SQLite operation timed out/,
    );
    assert.ok(Date.now() - startedAt < 2_000, 'timed out worker must terminate within two seconds');

    const result = await runReadonlyOperation(
      databasePath,
      'countEvents',
      {},
      generousOptions(),
    );
    assert.deepEqual(result, { count: 1n });
  });
});

test('AbortSignal terminates a recursive query within two seconds', async () => {
  await withDatabase(async ({ databasePath }) => {
    const controller = new AbortController();
    const startedAt = Date.now();
    const pending = runReadonlyOperation(
      databasePath,
      'longRead',
      {},
      generousOptions({ signal: controller.signal, timeoutMs: 10_000 }),
    );
    setTimeout(() => controller.abort(), 50);

    await assert.rejects(pending, /SQLite operation aborted/);
    assert.ok(Date.now() - startedAt < 2_000, 'aborted worker must terminate within two seconds');
  });
});
