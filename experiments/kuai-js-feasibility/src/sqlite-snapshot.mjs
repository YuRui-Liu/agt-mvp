import { lstat } from 'node:fs/promises';
import { Worker } from 'node:worker_threads';

import { identityOf } from './file-identity.mjs';

const WORKER_URL = new URL('./sqlite-worker.mjs', import.meta.url);
const TERMINATION_BUDGET_MS = 1_800;
const ALLOWED_OPERATIONS = new Set(['countEvents', 'getEventValues', 'longRead']);
const SIDECARS = [
  { label: 'main database', suffix: '', budgetKey: 'maxMainBytes' },
  { label: 'WAL', suffix: '-wal', budgetKey: 'maxWalBytes' },
  { label: 'SHM', suffix: '-shm', budgetKey: 'maxShmBytes' },
];

function checkedBudget(value, name) {
  if (!Number.isSafeInteger(value) || value < 0) {
    throw new TypeError(`${name} must be a non-negative safe integer`);
  }
  return BigInt(value);
}

function checkedTimeout(value) {
  if (!Number.isSafeInteger(value) || value <= 0) {
    throw new TypeError('timeoutMs must be a positive safe integer');
  }
  return value;
}

async function snapshotFile(path, label, required) {
  let stats;
  try {
    stats = await lstat(path, { bigint: true });
  } catch (error) {
    if (!required && error?.code === 'ENOENT') {
      return null;
    }
    throw error;
  }

  if (stats.isSymbolicLink() || !stats.isFile()) {
    throw new Error(`${label} must be a regular non-symlink file`);
  }
  return {
    identity: identityOf(stats),
    size: stats.size,
  };
}

async function snapshotDatabase(databasePath, budgets) {
  const files = {};
  let total = 0n;

  for (const sidecar of SIDECARS) {
    const file = await snapshotFile(
      `${databasePath}${sidecar.suffix}`,
      sidecar.label,
      sidecar.suffix === '',
    );
    files[sidecar.suffix] = file;
    if (file !== null) {
      if (file.size > budgets[sidecar.budgetKey]) {
        throw new Error(`${sidecar.label} exceeds budget`);
      }
      total += file.size;
    }
  }

  if (total > budgets.maxTotalBytes) {
    throw new Error('SQLite snapshot exceeds total budget');
  }
  return files;
}

function sameDatabaseSnapshot(before, after) {
  return SIDECARS.every(({ suffix }) => {
    const left = before[suffix];
    const right = after[suffix];
    return left === null
      ? right === null
      : right !== null && left.identity === right.identity && left.size === right.size;
  });
}

function errorFromWorker(serialized) {
  const error = new Error(serialized?.message ?? 'SQLite worker failed');
  error.name = serialized?.name ?? 'Error';
  if (serialized?.code !== undefined) {
    error.code = serialized.code;
  }
  return error;
}

export async function runReadonlyOperation(databasePath, operation, args, {
  maxMainBytes,
  maxWalBytes,
  maxShmBytes,
  maxTotalBytes,
  timeoutMs,
  signal,
  beforePostcheck = async () => {},
} = {}) {
  if (typeof databasePath !== 'string' || databasePath.length === 0) {
    throw new TypeError('databasePath must be a non-empty string');
  }
  if (!ALLOWED_OPERATIONS.has(operation)) {
    throw new Error(`unsupported read-only operation: ${operation}`);
  }
  if (signal?.aborted) {
    throw new Error('SQLite operation aborted');
  }

  const budgets = {
    maxMainBytes: checkedBudget(maxMainBytes, 'maxMainBytes'),
    maxWalBytes: checkedBudget(maxWalBytes, 'maxWalBytes'),
    maxShmBytes: checkedBudget(maxShmBytes, 'maxShmBytes'),
    maxTotalBytes: checkedBudget(maxTotalBytes, 'maxTotalBytes'),
  };
  const checkedTimeoutMs = checkedTimeout(timeoutMs);
  const before = await snapshotDatabase(databasePath, budgets);

  return await new Promise((resolve, reject) => {
    const worker = new Worker(WORKER_URL, {
      workerData: { databasePath, operation, args },
    });
    let settled = false;
    let resultReceived = false;

    const removeListeners = () => {
      clearTimeout(timer);
      signal?.removeEventListener('abort', onAbort);
      worker.removeAllListeners();
    };

    const terminateAndReject = async (error) => {
      if (settled) return;
      settled = true;
      removeListeners();
      try {
        const terminated = await Promise.race([
          worker.terminate().then(() => true, () => true),
          new Promise((resolveTermination) => {
            setTimeout(() => resolveTermination(false), TERMINATION_BUDGET_MS).unref();
          }),
        ]);
        if (!terminated) {
          worker.unref();
          reject(new Error('SQLite Worker did not terminate within two seconds'));
          return;
        }
        reject(error);
      } catch (terminationError) {
        reject(terminationError);
      }
    };

    const finishWithResult = async (value) => {
      if (settled) return;
      resultReceived = true;
      try {
        await beforePostcheck();
        const after = await snapshotDatabase(databasePath, budgets);
        if (!sameDatabaseSnapshot(before, after)) {
          throw new Error('SQLite snapshot changed during query');
        }
        settled = true;
        removeListeners();
        await worker.terminate();
        resolve(value);
      } catch (error) {
        await terminateAndReject(error);
      }
    };

    const onAbort = () => {
      void terminateAndReject(new Error('SQLite operation aborted'));
    };
    signal?.addEventListener('abort', onAbort, { once: true });

    const timer = setTimeout(() => {
      void terminateAndReject(new Error('SQLite operation timed out'));
    }, checkedTimeoutMs);

    worker.on('message', (message) => {
      if (settled) return;
      if (message?.type === 'ready') {
        worker.postMessage({ type: 'run' });
      } else if (message?.type === 'result') {
        void finishWithResult(message.value);
      } else if (message?.type === 'error') {
        void terminateAndReject(errorFromWorker(message.error));
      } else {
        void terminateAndReject(new Error('invalid SQLite worker message'));
      }
    });
    worker.on('error', (error) => {
      void terminateAndReject(error);
    });
    worker.on('exit', (code) => {
      if (!settled && !resultReceived) {
        void terminateAndReject(new Error(`SQLite worker exited before a result (code ${code})`));
      }
    });
  });
}
