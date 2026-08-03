import { constants } from 'node:fs';
import { lstat, open } from 'node:fs/promises';
import { extname, posix, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { gunzipSync } from 'node:zlib';

import { isForbiddenNativeMagic, verifyPackageManifest } from './verify-package-policy.mjs';

const blockSize = 512;
const mebibyte = 1024 * 1024;

export const DEFAULT_TARBALL_LIMITS = Object.freeze({
  maxCompressedBytes: 32 * mebibyte,
  maxUncompressedBytes: 128 * mebibyte,
  maxEntries: 4096,
  maxEntryBytes: 32 * mebibyte,
  maxManifestBytes: 1 * mebibyte,
});

const forbiddenTopLevel = new Set([
  'src',
  'test',
  'tests',
  'tsconfig.json',
  'scripts',
]);
const automaticMetadata = [
  /^package\.json$/i,
  /^readme(?:\..+)?$/i,
  /^licen[cs]e(?:\..+)?$/i,
  /^notice(?:\..+)?$/i,
  /^changelog(?:\..+)?$/i,
  /^history(?:\..+)?$/i,
];
const invalidPathCharacter = /[\\\x00-\x1f\x7f]/;

function effectiveLimits(overrides = {}) {
  const limits = {};
  for (const [name, defaultValue] of Object.entries(DEFAULT_TARBALL_LIMITS)) {
    const value = overrides[name] ?? defaultValue;
    if (!Number.isSafeInteger(value) || value <= 0) {
      throw new Error(`invalid tarball limit: ${name}`);
    }
    limits[name] = Math.min(value, defaultValue);
  }
  return limits;
}

function isZeroBlock(block) {
  return block.length === blockSize && block.every((byte) => byte === 0);
}

function readString(buffer, start, length) {
  const end = buffer.indexOf(0, start);
  const boundedEnd = end === -1 || end > start + length ? start + length : end;
  return buffer.toString('utf8', start, boundedEnd);
}

function readPathField(buffer, start, length, label) {
  const field = buffer.subarray(start, start + length);
  const terminator = field.indexOf(0);
  const content = terminator === -1 ? field : field.subarray(0, terminator);
  if (terminator !== -1 && field.subarray(terminator + 1).some((byte) => byte !== 0)) {
    throw new Error(`invalid NUL in tar ${label}`);
  }
  if (content.some((byte) => byte === 0x5c || byte < 0x20 || byte === 0x7f)) {
    throw new Error(`invalid control character or backslash in tar ${label}`);
  }
  return content.toString('utf8');
}

function readOctal(buffer, start, length, label) {
  const value = readString(buffer, start, length).trim().replace(/^0+/, '');
  if (value === '') return 0;
  if (!/^[0-7]+$/.test(value)) {
    throw new Error(`invalid tar ${label}`);
  }
  const parsed = Number.parseInt(value, 8);
  if (!Number.isSafeInteger(parsed)) {
    throw new Error(`tar ${label} exceeds the safe integer limit`);
  }
  return parsed;
}

function verifyHeaderChecksum(header) {
  const expected = readOctal(header, 148, 8, 'header checksum');
  let actual = 0;
  for (let index = 0; index < header.length; index += 1) {
    actual += index >= 148 && index < 156 ? 0x20 : header[index];
  }
  if (actual !== expected) {
    throw new Error('invalid tar header checksum');
  }
}

function verifyUstarHeader(header) {
  if (!header.subarray(257, 263).equals(Buffer.from('ustar\0', 'ascii'))) {
    throw new Error('invalid tar ustar magic');
  }
  if (!header.subarray(263, 265).equals(Buffer.from('00', 'ascii'))) {
    throw new Error('invalid tar ustar version');
  }
}

function parseTar(archive, limits) {
  const entries = [];
  let offset = 0;

  while (offset + blockSize <= archive.length) {
    const header = archive.subarray(offset, offset + blockSize);
    if (isZeroBlock(header)) {
      const secondBlock = archive.subarray(offset + blockSize, offset + (2 * blockSize));
      if (!isZeroBlock(secondBlock)) {
        throw new Error('tarball requires two end-of-archive terminator blocks');
      }
      if (archive.subarray(offset + (2 * blockSize)).some((byte) => byte !== 0)) {
        throw new Error('non-zero trailing data after tar end-of-archive terminator');
      }
      return entries;
    }

    verifyHeaderChecksum(header);
    verifyUstarHeader(header);
    if (entries.length >= limits.maxEntries) {
      throw new Error(`tar entry count exceeds limit of ${limits.maxEntries}`);
    }

    const name = readPathField(header, 0, 100, 'name');
    const prefix = readPathField(header, 345, 155, 'prefix');
    const path = prefix ? `${prefix}/${name}` : name;
    const size = readOctal(header, 124, 12, `size for ${path}`);
    const type = readString(header, 156, 1) || '0';
    if (type === '5' && size !== 0) {
      throw new Error(`tar directory entry must have size 0: ${path}`);
    }
    if (type === '0' && size > limits.maxEntryBytes) {
      throw new Error(`tar entry size exceeds limit of ${limits.maxEntryBytes}: ${path}`);
    }
    if (path === 'package/package.json' && size > limits.maxManifestBytes) {
      throw new Error(`packaged manifest size exceeds limit of ${limits.maxManifestBytes}`);
    }

    const bodyStart = offset + blockSize;
    const bodyEnd = bodyStart + size;
    const paddedEnd = bodyStart + (Math.ceil(size / blockSize) * blockSize);
    if (!Number.isSafeInteger(bodyEnd) || !Number.isSafeInteger(paddedEnd)) {
      throw new Error(`tar entry size exceeds the safe integer limit: ${path}`);
    }
    if (bodyEnd > archive.length) {
      throw new Error(`truncated tar entry body: ${path}`);
    }
    if (paddedEnd > archive.length) {
      throw new Error(`truncated tar entry padding: ${path}`);
    }
    if (archive.subarray(bodyEnd, paddedEnd).some((byte) => byte !== 0)) {
      throw new Error(`non-zero tar entry padding: ${path}`);
    }

    entries.push({ path, type, body: archive.subarray(bodyStart, bodyEnd) });
    offset = paddedEnd;
  }

  throw new Error('tarball is missing two end-of-archive terminator blocks');
}

function assertSafePathCharacters(value, label) {
  if (invalidPathCharacter.test(value)) {
    throw new Error(`${label} contains a control character or backslash`);
  }
}

function packageRelativePath(entryPath) {
  assertSafePathCharacters(entryPath, 'tarball path');
  if (!entryPath.startsWith('package/')) {
    throw new Error(`tarball path must use the package prefix: ${entryPath}`);
  }
  const relative = entryPath.slice('package/'.length).replace(/\/+$/, '');
  if (
    relative === ''
    || relative.startsWith('/')
    || relative.split('/').some((part) => part === '' || part === '.' || part === '..')
  ) {
    throw new Error(`invalid tarball path: ${entryPath}`);
  }
  return relative;
}

function canonicalizeEntries(entries) {
  const paths = new Map();

  return entries.map((entry) => {
    const relative = packageRelativePath(entry.path);
    const key = relative.toLowerCase();
    if (paths.has(key)) {
      throw new Error(`duplicate or colliding tarball path: ${relative}`);
    }

    const parts = key.split('/');
    for (let length = 1; length < parts.length; length += 1) {
      const ancestor = parts.slice(0, length).join('/');
      const ancestorType = paths.get(ancestor);
      if (ancestorType !== undefined && ancestorType !== '5') {
        throw new Error(`regular file is an ancestor of tarball path: ${relative}`);
      }
    }
    if (entry.type !== '5') {
      for (const existing of paths.keys()) {
        if (existing.startsWith(`${key}/`)) {
          throw new Error(`regular file conflicts with descendant tarball path: ${relative}`);
        }
      }
    }
    paths.set(key, entry.type);
    return { ...entry, relative };
  });
}

function isDeclaredPath(relative, declaredPaths) {
  return declaredPaths.some(
    (declared) => relative === declared || relative.startsWith(`${declared}/`),
  );
}

function verifyAllowedPath(relative, declaredPaths) {
  const segments = relative.split('/');
  if (segments.some((segment) => segment.toLowerCase() === 'node_modules')) {
    throw new Error(`forbidden node_modules path segment: ${relative}`);
  }
  if (forbiddenTopLevel.has(segments[0].toLowerCase())) {
    throw new Error(`forbidden tarball path: ${relative}`);
  }
  if (
    !automaticMetadata.some((pattern) => pattern.test(relative))
    && !isDeclaredPath(relative, declaredPaths)
  ) {
    throw new Error(`tarball path is not declared for publication: ${relative}`);
  }
}

function normalizeManifestPath(value) {
  if (typeof value !== 'string' || value === '') {
    throw new Error('package.json files contains an invalid path');
  }
  assertSafePathCharacters(value, 'package.json files path');
  if (posix.isAbsolute(value)) {
    throw new Error(`package.json files contains an unsafe path: ${value}`);
  }
  const withoutPrefix = value.replace(/^\.\//, '').replace(/\/+$/, '');
  const parts = withoutPrefix.split('/');
  if (parts.length === 0 || parts.some((part) => part === '' || part === '.' || part === '..')) {
    throw new Error(`package.json files contains an unsafe path: ${value}`);
  }
  return parts.join('/');
}

function declaredPublishPaths(manifest) {
  if (!Array.isArray(manifest.files) || manifest.files.length === 0) {
    throw new Error('package.json files must be a non-empty array');
  }
  const keys = new Set();
  return manifest.files.map((value) => {
    const normalized = normalizeManifestPath(value);
    const key = normalized.toLowerCase();
    if (keys.has(key)) {
      throw new Error(`duplicate or colliding package.json files path: ${value}`);
    }
    keys.add(key);
    const segments = normalized.split('/');
    if (forbiddenTopLevel.has(segments[0].toLowerCase())) {
      throw new Error(`package.json files declares a forbidden path: ${value}`);
    }
    if (segments.some((segment) => segment.toLowerCase() === 'node_modules')) {
      throw new Error(`package.json files declares a forbidden node_modules path: ${value}`);
    }
    return normalized;
  });
}

async function readCompressedTarball(tarballPath, maxCompressedBytes) {
  const path = resolve(tarballPath);
  const initialStatus = await lstat(path);
  if (initialStatus.isSymbolicLink()) {
    throw new Error(`symbolic link is forbidden: ${path}`);
  }
  if (!initialStatus.isFile()) {
    throw new Error(`tarball must be a regular file: ${path}`);
  }

  // O_NOFOLLOW is unavailable on some platforms. The lstat plus opened-FD
  // identity check preserves no-follow semantics there; O_NONBLOCK prevents
  // a raced special file from blocking before fstat rejects it.
  const flags = constants.O_RDONLY
    | (constants.O_NOFOLLOW ?? 0)
    | (constants.O_NONBLOCK ?? 0);
  let handle;
  try {
    handle = await open(path, flags);
  } catch (error) {
    if (error instanceof Error && error.code === 'ELOOP') {
      throw new Error(`symbolic link is forbidden: ${path}`);
    }
    throw error;
  }

  try {
    const status = await handle.stat();
    if (!status.isFile()) {
      throw new Error(`tarball must be a regular file: ${path}`);
    }
    if (status.dev !== initialStatus.dev || status.ino !== initialStatus.ino) {
      throw new Error(`tarball changed while opening: ${path}`);
    }
    if (!Number.isSafeInteger(status.size)) {
      throw new Error('compressed tarball size exceeds the safe integer limit');
    }
    if (status.size > maxCompressedBytes) {
      throw new Error(`compressed tarball size exceeds limit of ${maxCompressedBytes}`);
    }

    const compressed = Buffer.alloc(status.size);
    let offset = 0;
    while (offset < compressed.length) {
      const { bytesRead } = await handle.read(
        compressed,
        offset,
        compressed.length - offset,
        offset,
      );
      if (bytesRead === 0) {
        throw new Error('compressed tarball was truncated while reading');
      }
      offset += bytesRead;
    }
    return compressed;
  } finally {
    await handle.close();
  }
}

export async function verifyTarball(tarballPath, options = {}) {
  const limits = effectiveLimits(options.limits);
  let compressed;
  try {
    compressed = await readCompressedTarball(tarballPath, limits.maxCompressedBytes);
  } catch (error) {
    throw new Error(`cannot read tarball: ${error instanceof Error ? error.message : String(error)}`);
  }

  let archive;
  try {
    archive = gunzipSync(compressed, { maxOutputLength: limits.maxUncompressedBytes });
  } catch (error) {
    if (error instanceof Error && error.code === 'ERR_BUFFER_TOO_LARGE') {
      throw new Error(`uncompressed tarball size exceeds limit of ${limits.maxUncompressedBytes}`);
    }
    throw new Error(`cannot decompress tarball: ${error instanceof Error ? error.message : String(error)}`);
  }

  const entries = canonicalizeEntries(parseTar(archive, limits));
  const manifestEntry = entries.find((entry) => entry.relative === 'package.json');
  if (!manifestEntry || manifestEntry.type !== '0') {
    throw new Error('tarball must contain a regular package/package.json');
  }

  let manifest;
  try {
    manifest = JSON.parse(manifestEntry.body.toString('utf8'));
  } catch (error) {
    throw new Error(`invalid packaged package.json: ${error instanceof Error ? error.message : String(error)}`);
  }
  verifyPackageManifest(manifest);
  const declaredPaths = declaredPublishPaths(manifest);

  for (const entry of entries) {
    verifyAllowedPath(entry.relative, declaredPaths);
    if (entry.type === '2') {
      throw new Error(`symbolic link is forbidden in tarball: ${entry.relative}`);
    }
    if (entry.type !== '0' && entry.type !== '5') {
      throw new Error(`unsupported special tarball entry: ${entry.relative}`);
    }
    if (entry.type === '0') {
      if (extname(entry.relative).toLowerCase() === '.node') {
        throw new Error(`native extension is forbidden in tarball: ${entry.relative}`);
      }
      if (isForbiddenNativeMagic(entry.body.subarray(0, 4))) {
        throw new Error(`native executable magic is forbidden in tarball: ${entry.relative}`);
      }
    }
  }
}

const currentFile = fileURLToPath(import.meta.url);
if (process.argv[1] && resolve(process.argv[1]) === currentFile) {
  const tarballPath = process.argv[2];
  if (!tarballPath) {
    process.stderr.write('usage: node scripts/verify-tarball.mjs <package.tgz>\n');
    process.exitCode = 1;
  } else {
    try {
      await verifyTarball(tarballPath);
      process.stdout.write('tarball policy passed\n');
    } catch (error) {
      process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
      process.exitCode = 1;
    }
  }
}
