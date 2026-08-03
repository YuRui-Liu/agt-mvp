import { readFile } from 'node:fs/promises';
import { extname, posix, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { gunzipSync } from 'node:zlib';

import { isForbiddenNativeMagic, verifyPackageManifest } from './verify-package-policy.mjs';

const blockSize = 512;
const forbiddenTopLevel = new Set([
  'src',
  'test',
  'tests',
  'tsconfig.json',
  'scripts',
  'node_modules',
]);
const automaticMetadata = [
  /^package\.json$/i,
  /^readme(?:\..+)?$/i,
  /^licen[cs]e(?:\..+)?$/i,
  /^notice(?:\..+)?$/i,
  /^changelog(?:\..+)?$/i,
  /^history(?:\..+)?$/i,
];

function isZeroBlock(block) {
  return block.every((byte) => byte === 0);
}

function readString(buffer, start, length) {
  const end = buffer.indexOf(0, start);
  const boundedEnd = end === -1 || end > start + length ? start + length : end;
  return buffer.toString('utf8', start, boundedEnd);
}

function readOctal(buffer, start, length, label) {
  const value = readString(buffer, start, length).trim().replace(/^0+/, '');
  if (value === '') return 0;
  if (!/^[0-7]+$/.test(value)) {
    throw new Error(`invalid tar ${label}`);
  }
  return Number.parseInt(value, 8);
}

function parseTar(archive) {
  const entries = [];
  const names = new Set();
  let offset = 0;

  while (offset + blockSize <= archive.length) {
    const header = archive.subarray(offset, offset + blockSize);
    if (isZeroBlock(header)) break;

    const name = readString(header, 0, 100);
    const prefix = readString(header, 345, 155);
    const path = prefix ? `${prefix}/${name}` : name;
    const size = readOctal(header, 124, 12, `size for ${path}`);
    const type = readString(header, 156, 1) || '0';
    const bodyStart = offset + blockSize;
    const bodyEnd = bodyStart + size;
    if (bodyEnd > archive.length) {
      throw new Error(`truncated tar entry: ${path}`);
    }
    if (names.has(path)) {
      throw new Error(`duplicate tarball path is forbidden: ${path}`);
    }
    names.add(path);
    entries.push({ path, type, body: archive.subarray(bodyStart, bodyEnd) });
    offset = bodyStart + Math.ceil(size / blockSize) * blockSize;
  }

  return entries;
}

function packageRelativePath(entryPath) {
  if (!entryPath.startsWith('package/')) {
    throw new Error(`tarball path must use the package prefix: ${entryPath}`);
  }
  const relative = entryPath.slice('package/'.length).replace(/\/$/, '');
  if (
    relative === ''
    || relative.startsWith('/')
    || relative.split('/').some((part) => part === '' || part === '.' || part === '..')
  ) {
    throw new Error(`invalid tarball path: ${entryPath}`);
  }
  return relative;
}

function isDeclaredPath(relative, declaredPaths) {
  return declaredPaths.some(
    (declared) => relative === declared || relative.startsWith(`${declared}/`),
  );
}

function verifyAllowedPath(relative, declaredPaths) {
  const topLevel = relative.split('/')[0];
  if (forbiddenTopLevel.has(topLevel)) {
    throw new Error(`forbidden tarball path: ${relative}`);
  }
  if (
    !automaticMetadata.some((pattern) => pattern.test(relative))
    && !isDeclaredPath(relative, declaredPaths)
  ) {
    throw new Error(`tarball path is not declared for publication: ${relative}`);
  }
}

function declaredPublishPaths(manifest) {
  if (!Array.isArray(manifest.files) || manifest.files.length === 0) {
    throw new Error('package.json files must be a non-empty array');
  }
  return manifest.files.map((value) => {
    if (typeof value !== 'string' || value === '' || posix.isAbsolute(value)) {
      throw new Error('package.json files contains an invalid path');
    }
    const normalized = posix.normalize(value).replace(/^\.\//, '').replace(/\/$/, '');
    if (normalized === '..' || normalized.startsWith('../') || normalized.includes('/../')) {
      throw new Error(`package.json files contains an unsafe path: ${value}`);
    }
    if (forbiddenTopLevel.has(normalized.split('/')[0])) {
      throw new Error(`package.json files declares a forbidden path: ${value}`);
    }
    return normalized;
  });
}

export async function verifyTarball(tarballPath) {
  let archive;
  try {
    archive = gunzipSync(await readFile(resolve(tarballPath)));
  } catch (error) {
    throw new Error(`cannot read tarball: ${error instanceof Error ? error.message : String(error)}`);
  }

  const entries = parseTar(archive);
  const manifestEntry = entries.find((entry) => entry.path === 'package/package.json');
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
    const relative = packageRelativePath(entry.path);
    verifyAllowedPath(relative, declaredPaths);

    if (entry.type === '2') {
      throw new Error(`symbolic link is forbidden in tarball: ${relative}`);
    }
    if (entry.type !== '0' && entry.type !== '5') {
      throw new Error(`unsupported special tarball entry: ${relative}`);
    }
    if (entry.type === '0') {
      if (extname(relative).toLowerCase() === '.node') {
        throw new Error(`native extension is forbidden in tarball: ${relative}`);
      }
      if (isForbiddenNativeMagic(entry.body.subarray(0, 4))) {
        throw new Error(`native executable magic is forbidden in tarball: ${relative}`);
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
