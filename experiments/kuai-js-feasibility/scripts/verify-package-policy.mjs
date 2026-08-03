import { open, readFile, readdir } from 'node:fs/promises';
import { dirname, extname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const lifecycleHooks = ['preinstall', 'install', 'postinstall', 'prepare'];
const forbiddenMagic = [
  Buffer.from([0x7f, 0x45, 0x4c, 0x46]),
  Buffer.from([0x4d, 0x5a]),
  Buffer.from([0xfe, 0xed, 0xfa, 0xce]),
  Buffer.from([0xfe, 0xed, 0xfa, 0xcf]),
  Buffer.from([0xce, 0xfa, 0xed, 0xfe]),
  Buffer.from([0xcf, 0xfa, 0xed, 0xfe]),
  Buffer.from([0xca, 0xfe, 0xba, 0xbe]),
  Buffer.from([0xbe, 0xba, 0xfe, 0xca]),
  Buffer.from([0xca, 0xfe, 0xba, 0xbf]),
  Buffer.from([0xbf, 0xba, 0xfe, 0xca]),
];

function assertEmptyObject(value, label) {
  if (value === undefined) return;
  if (value === null || Array.isArray(value) || typeof value !== 'object' || Object.keys(value).length !== 0) {
    throw new Error(`${label} contains runtime dependencies`);
  }
}

async function* regularFiles(root) {
  for (const entry of await readdir(root, { withFileTypes: true })) {
    if (entry.name === '.git') continue;
    const path = join(root, entry.name);
    if (entry.isDirectory()) {
      yield* regularFiles(path);
    } else if (entry.isFile()) {
      yield path;
    } else if (entry.isSymbolicLink()) {
      throw new Error(`symbolic link is forbidden: ${path}`);
    }
  }
}

async function hasForbiddenMagic(path) {
  const handle = await open(path, 'r');
  try {
    const prefix = Buffer.alloc(4);
    const { bytesRead } = await handle.read(prefix, 0, prefix.length, 0);
    return forbiddenMagic.some((magic) => bytesRead >= magic.length && prefix.subarray(0, magic.length).equals(magic));
  } finally {
    await handle.close();
  }
}

export async function verifyPackagePolicy(root) {
  const packageRoot = resolve(root);
  const manifest = JSON.parse(await readFile(join(packageRoot, 'package.json'), 'utf8'));
  assertEmptyObject(manifest.dependencies, 'dependencies');
  assertEmptyObject(manifest.optionalDependencies, 'optionalDependencies');
  assertEmptyObject(manifest.peerDependencies, 'peerDependencies');

  for (const hook of lifecycleHooks) {
    if (manifest.scripts?.[hook] !== undefined) {
      throw new Error(`forbidden lifecycle script: ${hook}`);
    }
  }

  for await (const path of regularFiles(packageRoot)) {
    if (extname(path).toLowerCase() === '.node') {
      throw new Error(`native extension is forbidden: ${path}`);
    }
    if (await hasForbiddenMagic(path)) {
      throw new Error(`native executable is forbidden: ${path}`);
    }
  }
}

const currentFile = fileURLToPath(import.meta.url);
const invokedFile = process.argv[1] ? resolve(process.argv[1]) : '';
if (invokedFile === currentFile) {
  const packageRoot = resolve(dirname(currentFile), '..');
  try {
    await verifyPackagePolicy(packageRoot);
    process.stdout.write('pure-js package policy passed\n');
  } catch (error) {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 1;
  }
}
