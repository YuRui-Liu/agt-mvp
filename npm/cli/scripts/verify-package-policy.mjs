import { constants } from 'node:fs';
import { lstat, open, readdir } from 'node:fs/promises';
import { dirname, extname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const lifecycleHooks = ['preinstall', 'install', 'postinstall', 'prepare'];
const runtimeDependencyFields = ['dependencies', 'optionalDependencies', 'peerDependencies'];
const ignoredDirectories = new Set(['.git', 'node_modules']);
// O_NOFOLLOW is not available on every supported platform. O_NONBLOCK plus
// lstat/opened-FD identity checks below preserve safe failure semantics there.
const regularFileOpenFlags = constants.O_RDONLY
  | (constants.O_NOFOLLOW ?? 0)
  | (constants.O_NONBLOCK ?? 0);
const forbiddenMagic = [
  Buffer.from('7f454c46', 'hex'),
  Buffer.from('4d5a', 'hex'),
  Buffer.from('feedface', 'hex'),
  Buffer.from('cefaedfe', 'hex'),
  Buffer.from('feedfacf', 'hex'),
  Buffer.from('cffaedfe', 'hex'),
  Buffer.from('cafebabe', 'hex'),
  Buffer.from('bebafeca', 'hex'),
  Buffer.from('cafebabf', 'hex'),
  Buffer.from('bfbafeca', 'hex'),
];

function hasOwn(object, property) {
  return object !== null
    && typeof object === 'object'
    && Object.prototype.hasOwnProperty.call(object, property);
}

function assertEmptyObject(value, label) {
  if (value === undefined) return;
  if (value === null || Array.isArray(value) || typeof value !== 'object') {
    throw new Error(`${label} must be an empty object`);
  }
  if (Object.keys(value).length !== 0) {
    throw new Error(`${label} contains runtime dependencies`);
  }
}

export function verifyPackageManifest(manifest) {
  if (manifest === null || Array.isArray(manifest) || typeof manifest !== 'object') {
    throw new Error('package.json must contain a JSON object');
  }

  for (const field of runtimeDependencyFields) {
    assertEmptyObject(manifest[field], field);
  }

  for (const hook of lifecycleHooks) {
    if (hasOwn(manifest.scripts, hook)) {
      throw new Error(`forbidden lifecycle script: ${hook}`);
    }
  }
}

export function isForbiddenNativeMagic(prefix) {
  return forbiddenMagic.some(
    (magic) => prefix.length >= magic.length && prefix.subarray(0, magic.length).equals(magic),
  );
}

async function openVerifiedRegularFile(path, initialStatus) {
  let handle;
  try {
    handle = await open(path, regularFileOpenFlags);
  } catch (error) {
    if (error instanceof Error && error.code === 'ELOOP') {
      throw new Error(`symbolic link is forbidden: ${path}`);
    }
    throw error;
  }

  const openedStatus = await handle.stat();
  if (!openedStatus.isFile()) {
    await handle.close();
    throw new Error(`unsupported special filesystem entry: ${path}`);
  }
  if (openedStatus.dev !== initialStatus.dev || openedStatus.ino !== initialStatus.ino) {
    await handle.close();
    throw new Error(`filesystem entry changed while opening: ${path}`);
  }
  return handle;
}

async function inspectRegularFile(path, initialStatus) {
  if (extname(path).toLowerCase() === '.node') {
    throw new Error(`native extension is forbidden: ${path}`);
  }

  const handle = await openVerifiedRegularFile(path, initialStatus);
  try {
    const prefix = Buffer.alloc(4);
    const { bytesRead } = await handle.read(prefix, 0, prefix.length, 0);
    if (isForbiddenNativeMagic(prefix.subarray(0, bytesRead))) {
      throw new Error(`native executable magic is forbidden: ${path}`);
    }
  } finally {
    await handle.close();
  }
}

async function inspectDirectory(root) {
  const rootStatus = await lstat(root);
  if (rootStatus.isSymbolicLink()) {
    throw new Error(`symbolic link is forbidden: ${root}`);
  }
  if (!rootStatus.isDirectory()) {
    throw new Error(`expected a package directory: ${root}`);
  }
  const entries = await readdir(root, { withFileTypes: true });
  entries.sort((left, right) => left.name.localeCompare(right.name));

  for (const entry of entries) {
    const path = join(root, entry.name);
    const status = await lstat(path);
    if (status.isSymbolicLink()) {
      throw new Error(`symbolic link is forbidden: ${path}`);
    }
    if (status.isDirectory()) {
      if (!ignoredDirectories.has(entry.name)) {
        await inspectDirectory(path);
      }
      continue;
    }
    if (status.isFile()) {
      await inspectRegularFile(path, status);
      continue;
    }
    throw new Error(`unsupported special filesystem entry: ${path}`);
  }
}

export async function verifyPackageDirectory(root) {
  const packageRoot = resolve(root);
  const rootStatus = await lstat(packageRoot);
  if (rootStatus.isSymbolicLink()) {
    throw new Error(`symbolic link is forbidden: ${packageRoot}`);
  }
  if (!rootStatus.isDirectory()) {
    throw new Error(`expected a package directory: ${packageRoot}`);
  }

  const manifestPath = join(packageRoot, 'package.json');
  let manifest;
  try {
    const manifestStatus = await lstat(manifestPath);
    if (manifestStatus.isSymbolicLink()) {
      throw new Error(`symbolic link is forbidden: ${manifestPath}`);
    }
    if (!manifestStatus.isFile()) {
      throw new Error(`unsupported special filesystem entry: ${manifestPath}`);
    }

    const handle = await openVerifiedRegularFile(manifestPath, manifestStatus);
    try {
      manifest = JSON.parse(await handle.readFile('utf8'));
    } finally {
      await handle.close();
    }
  } catch (error) {
    if (error instanceof Error && /symbolic link|special filesystem entry/.test(error.message)) {
      throw error;
    }
    throw new Error(`cannot read package.json: ${error instanceof Error ? error.message : String(error)}`);
  }
  verifyPackageManifest(manifest);
  await inspectDirectory(packageRoot);
}

const currentFile = fileURLToPath(import.meta.url);
if (process.argv[1] && resolve(process.argv[1]) === currentFile) {
  const packageRoot = resolve(process.argv[2] ?? join(dirname(currentFile), '..'));
  try {
    await verifyPackageDirectory(packageRoot);
    process.stdout.write('pure-js package policy passed\n');
  } catch (error) {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 1;
  }
}
