import { constants } from 'node:fs';
import { open } from 'node:fs/promises';

import { identityOf, sameSnapshot, snapshotPath } from './file-identity.mjs';

function validateBudget(maxBytes) {
  if (!Number.isSafeInteger(maxBytes) || maxBytes < 0) {
    throw new TypeError('maxBytes must be a non-negative safe integer');
  }
}

function assertNotAborted(signal) {
  signal?.throwIfAborted();
}

function assertSameIdentity(actualStats, expectedIdentity) {
  if (!actualStats.isFile()) {
    throw new Error('opened descriptor is not a regular file');
  }
  if (identityOf(actualStats) !== expectedIdentity) {
    throw new Error('path identity changed: file identity changed');
  }
}

async function readThroughHandle(handle, expectedSize, maxBytes, signal) {
  const detectionLength = Number(expectedSize) + 1;
  const data = Buffer.alloc(detectionLength);
  let offset = 0;

  while (offset < data.length) {
    assertNotAborted(signal);
    const { bytesRead } = await handle.read(data, offset, data.length - offset, offset);
    if (bytesRead === 0) break;
    offset += bytesRead;
  }
  assertNotAborted(signal);

  if (offset > maxBytes) {
    throw new Error('file exceeds maxBytes');
  }
  return data.subarray(0, offset);
}

export async function readBoundedFile(root, relative, {
  maxBytes,
  signal,
  afterSnapshot = async () => {},
  afterOpen = async () => {},
} = {}) {
  validateBudget(maxBytes);
  assertNotAborted(signal);

  const before = await snapshotPath(root, relative);
  const finalBefore = before.at(-1);
  if (finalBefore.stats.size > BigInt(maxBytes)) {
    throw new Error('file exceeds maxBytes');
  }

  await afterSnapshot();
  assertNotAborted(signal);

  const flags = constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0);
  let handle;
  try {
    handle = await open(finalBefore.path, flags);
    const opened = await handle.stat({ bigint: true });
    assertSameIdentity(opened, finalBefore.identity);
    if (opened.size > BigInt(maxBytes)) {
      throw new Error('file exceeds maxBytes');
    }

    const after = await snapshotPath(root, relative);
    if (!sameSnapshot(before, after)) {
      throw new Error('path identity changed');
    }

    await afterOpen();
    assertNotAborted(signal);
    const data = await readThroughHandle(handle, opened.size, maxBytes, signal);

    const finished = await handle.stat({ bigint: true });
    assertSameIdentity(finished, finalBefore.identity);
    if (finished.size !== opened.size) {
      throw new Error('file size changed while reading');
    }
    assertNotAborted(signal);
    return data;
  } finally {
    await handle?.close();
  }
}
