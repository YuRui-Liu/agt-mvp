import { lstat } from 'node:fs/promises';
import { isAbsolute, join, normalize, parse, sep } from 'node:path';

export function identityOf(stats) {
  if (stats.dev === 0n || stats.ino === 0n) {
    throw new Error('stable file identity unavailable');
  }
  return `${stats.dev}:${stats.ino}`;
}

function checkedRelative(relative) {
  if (typeof relative !== 'string' || relative.length === 0 || isAbsolute(relative)) {
    throw new Error('relative path required');
  }

  const rawParts = relative.split(/[\\/]+/);
  if (rawParts.includes('..')) {
    throw new Error('path traversal is not allowed');
  }

  const normalized = normalize(relative);
  const parts = normalized.split(sep).filter((part) => part !== '' && part !== '.');
  if (parts.length === 0 || parts.includes('..') || parse(normalized).root !== '') {
    throw new Error('path traversal is not allowed');
  }
  return parts;
}

async function componentSnapshot(path, expectedKind) {
  const stats = await lstat(path, { bigint: true });
  if (stats.isSymbolicLink()) {
    throw new Error(`symbolic link is not allowed: ${path}`);
  }
  if (expectedKind === 'directory' && !stats.isDirectory()) {
    throw new Error(`path component is not a directory: ${path}`);
  }
  if (expectedKind === 'file' && !stats.isFile()) {
    throw new Error(`final component is not a regular file: ${path}`);
  }
  return { path, identity: identityOf(stats), kind: expectedKind, stats };
}

export async function snapshotPath(root, relative) {
  if (typeof root !== 'string' || !isAbsolute(root)) {
    throw new Error('root must be an absolute path');
  }

  const parts = checkedRelative(relative);
  const snapshots = [];
  snapshots.push(await componentSnapshot(root, 'directory'));

  let current = root;
  for (let index = 0; index < parts.length; index += 1) {
    current = join(current, parts[index]);
    const kind = index === parts.length - 1 ? 'file' : 'directory';
    snapshots.push(await componentSnapshot(current, kind));
  }
  return snapshots;
}

export function sameSnapshot(before, after) {
  return before.length === after.length && before.every((entry, index) => {
    const candidate = after[index];
    return entry.path === candidate.path
      && entry.identity === candidate.identity
      && entry.kind === candidate.kind;
  });
}
