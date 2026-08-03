import { parentPort, workerData } from 'node:worker_threads';
import { constants, DatabaseSync } from 'node:sqlite';

if (parentPort === null) {
  throw new Error('sqlite-worker must run in a Worker');
}

const operations = {
  countEvents(db) {
    return db.prepare('SELECT count(*) AS count FROM events').get();
  },
  getEventValues(db) {
    return db.prepare('SELECT id, value FROM events ORDER BY id').all();
  },
  longRead(db) {
    return db.prepare(`
      WITH RECURSIVE cnt(x) AS (
        VALUES(0) UNION ALL SELECT x + 1 FROM cnt WHERE x < 1000000000
      ) SELECT max(x) AS maximum FROM cnt
    `).get();
  },
};

const allowedAuthorizerActions = new Set([
  constants.SQLITE_SELECT,
  constants.SQLITE_READ,
  constants.SQLITE_FUNCTION,
  constants.SQLITE_TRANSACTION,
  // A recursive SELECT emits SQLITE_RECURSIVE in addition to SQLITE_SELECT.
  constants.SQLITE_RECURSIVE,
]);

function serializeError(error) {
  return {
    message: error instanceof Error ? error.message : String(error),
    name: error instanceof Error ? error.name : 'Error',
    code: error && typeof error === 'object' ? error.code : undefined,
  };
}

function execute() {
  const operation = operations[workerData.operation];
  if (operation === undefined) {
    throw new Error(`unsupported read-only operation: ${workerData.operation}`);
  }

  const db = new DatabaseSync(workerData.databasePath, {
    readOnly: true,
    allowExtension: false,
    defensive: true,
    readBigInts: true,
    timeout: 0,
  });

  let transactionOpen = false;
  try {
    db.exec('PRAGMA query_only = ON');
    db.exec('BEGIN');
    transactionOpen = true;
    db.setAuthorizer((actionCode) => (
      allowedAuthorizerActions.has(actionCode) ? constants.SQLITE_OK : constants.SQLITE_DENY
    ));
    return operation(db, workerData.args ?? {});
  } finally {
    if (transactionOpen) {
      try {
        db.exec('ROLLBACK');
      } catch {
        // The parent discards all results when cleanup fails or the Worker exits.
      }
    }
    db.close();
  }
}

parentPort.once('message', (message) => {
  if (message?.type !== 'run') {
    parentPort.postMessage({ type: 'error', error: serializeError(new Error('invalid worker command')) });
    return;
  }

  try {
    parentPort.postMessage({ type: 'result', value: execute() });
  } catch (error) {
    parentPort.postMessage({ type: 'error', error: serializeError(error) });
  }
});

parentPort.postMessage({ type: 'ready' });
