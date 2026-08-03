import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { gzipSync } from 'node:zlib';

import { verifyTarball } from '../scripts/verify-tarball.mjs';

const cliRoot = fileURLToPath(new URL('..', import.meta.url));
const tarballCli = fileURLToPath(new URL('../scripts/verify-tarball.mjs', import.meta.url));
const manifest = JSON.stringify({
  name: '@kuai-ai/cli',
  version: '0.0.0-dev',
  files: ['bin', 'dist', 'skill', 'README.md', 'LICENSE'],
  dependencies: {},
  optionalDependencies: {},
  peerDependencies: {},
});

function tarHeader(name, size, type = '0', linkname = '', options = {}) {
  const header = Buffer.alloc(512);
  header.write(name, 0, 100, 'utf8');
  header.write('0000644\0', 100, 8, 'ascii');
  header.write('0000000\0', 108, 8, 'ascii');
  header.write('0000000\0', 116, 8, 'ascii');
  header.write(`${size.toString(8).padStart(11, '0')}\0`, 124, 12, 'ascii');
  header.write('00000000000\0', 136, 12, 'ascii');
  header.fill(0x20, 148, 156);
  header.write(type, 156, 1, 'ascii');
  header.write(linkname, 157, 100, 'utf8');
  header.write(options.magic ?? 'ustar\0', 257, 6, 'ascii');
  header.write(options.version ?? '00', 263, 2, 'ascii');
  const checksum = [...header].reduce((sum, byte) => sum + byte, 0);
  header.write(`${checksum.toString(8).padStart(6, '0')}\0 `, 148, 8, 'ascii');
  if (options.badChecksum) header[0] ^= 1;
  return header;
}

function createTarball(entries, options = {}) {
  const fixture = mkdtempSync(join(tmpdir(), 'kuai-tarball-policy-'));
  const path = join(fixture, 'kuai-ai-cli-0.0.0-dev.tgz');
  const chunks = [];

  for (const entry of entries) {
    const body = Buffer.isBuffer(entry.body) ? entry.body : Buffer.from(entry.body ?? '');
    const declaredSize = entry.size ?? body.length;
    chunks.push(tarHeader(
      entry.name,
      declaredSize,
      entry.type,
      entry.linkname,
      entry.headerOptions,
    ));
    chunks.push(body);
    if (!entry.omitPadding) {
      chunks.push(Buffer.alloc((512 - (body.length % 512)) % 512));
    }
  }
  chunks.push(Buffer.alloc(512 * (options.terminatorBlocks ?? 2)));
  if (options.trailing) chunks.push(options.trailing);
  writeFileSync(path, gzipSync(Buffer.concat(chunks)));
  return { fixture, path };
}

async function verifyFixture(entries, options = {}, limits) {
  const archive = createTarball(entries, options);
  try {
    await verifyTarball(archive.path, limits ? { limits } : undefined);
  } finally {
    rmSync(archive.fixture, { recursive: true, force: true });
  }
}

async function withTarball(extraEntries, assertion, options = {}) {
  const packagedManifest = options.manifest ?? manifest;
  const archive = createTarball([
    { name: 'package/package.json', body: packagedManifest },
    ...(options.includeBin === false
      ? []
      : [{ name: 'package/bin/kuai.js', body: '#!/usr/bin/env node\n' }]),
    ...extraEntries,
  ]);
  try {
    await assertion(archive.path);
  } finally {
    rmSync(archive.fixture, { recursive: true, force: true });
  }
}

function runNpmPack(packageRoot, destination, options = {}) {
  const npmExecPath = options.npmExecPath === undefined
    ? process.env.npm_execpath
    : options.npmExecPath;
  const npmCommand = npmExecPath
    ? process.execPath
    : process.platform === 'win32' ? 'npm.cmd' : 'npm';
  const npmArguments = npmExecPath ? [npmExecPath] : [];
  const result = spawnSync(
    npmCommand,
    [
      ...npmArguments,
      'pack',
      '--json',
      '--ignore-scripts',
      '--pack-destination',
      destination,
      '--cache',
      join(destination, '.npm-cache'),
    ],
    { cwd: packageRoot, encoding: 'utf8', env: options.env, shell: false },
  );
  if (result.error?.code === 'ENOENT') {
    throw new Error('unable to execute npm from PATH');
  }
  assert.equal(result.status, 0, result.stderr);
  return JSON.parse(result.stdout);
}

test('accepts a minimal tarball containing only declared publish paths', async () => {
  await withTarball([{ name: 'package/dist/cli/main.js', body: 'export {};\n' }], verifyTarball);
});

test('rejects a symbolic link supplied as the tarball path', async () => {
  const archive = createTarball([
    { name: 'package/package.json', body: manifest },
    { name: 'package/bin/kuai.js', body: 'safe' },
  ]);
  const link = join(archive.fixture, 'linked.tgz');
  try {
    writeFileSync(join(archive.fixture, 'placeholder'), '');
    symlinkSync(archive.path, link);
    await assert.rejects(verifyTarball(link), /symbolic link/i);
  } finally {
    rmSync(archive.fixture, { recursive: true, force: true });
  }
});

test('rejects a FIFO supplied as the tarball path without blocking', { skip: process.platform === 'win32' }, () => {
  const fixture = mkdtempSync(join(tmpdir(), 'kuai-tarball-fifo-'));
  const fifo = join(fixture, 'package.tgz');
  try {
    const created = spawnSync('mkfifo', [fifo], { encoding: 'utf8', shell: false });
    if (created.error?.code === 'ENOENT') return;
    assert.equal(created.status, 0, created.stderr);
    const result = spawnSync(process.execPath, [tarballCli, fifo], {
      encoding: 'utf8',
      shell: false,
      timeout: 1_000,
    });
    assert.notEqual(result.status, 0);
    assert.equal(result.stdout, '');
    assert.match(result.stderr ?? '', /regular file|special/i);
  } finally {
    rmSync(fixture, { recursive: true, force: true });
  }
});

for (const [label, limits] of [
  ['compressed tarball size', { maxCompressedBytes: 1 }],
  ['uncompressed tarball size', { maxUncompressedBytes: 512 }],
  ['tar entry count', { maxEntries: 1 }],
  ['regular tar entry size', { maxEntryBytes: 64 }],
  ['packaged manifest size', { maxManifestBytes: 16 }],
]) {
  test(`enforces the ${label} limit`, async () => {
    await withTarball(
      [{ name: 'package/dist/payload.js', body: Buffer.alloc(128) }],
      async (path) => assert.rejects(
        verifyTarball(path, { limits }),
        /limit|too large|exceeds/i,
      ),
    );
  });
}

for (const [label, entries, options] of [
  ['bad header checksum', [
    { name: 'package/package.json', body: manifest, headerOptions: { badChecksum: true } },
  ]],
  ['invalid ustar magic', [
    { name: 'package/package.json', body: manifest, headerOptions: { magic: 'notar\0' } },
  ]],
  ['invalid ustar version', [
    { name: 'package/package.json', body: manifest, headerOptions: { version: '99' } },
  ]],
  ['missing end-of-archive blocks', [
    { name: 'package/package.json', body: manifest },
  ], { terminatorBlocks: 0 }],
  ['a single end-of-archive block', [
    { name: 'package/package.json', body: manifest },
  ], { terminatorBlocks: 1 }],
  ['non-zero trailing data', [
    { name: 'package/package.json', body: manifest },
  ], { trailing: Buffer.from([1]) }],
  ['truncated entry padding', [
    { name: 'package/package.json', body: manifest, omitPadding: true },
  ], { terminatorBlocks: 0 }],
  ['a directory with a non-zero size', [
    { name: 'package/package.json', body: manifest },
    { name: 'package/dist/', type: '5', body: 'x' },
  ]],
]) {
  test(`rejects ${label}`, async () => {
    await assert.rejects(
      verifyFixture(entries, options),
      /checksum|ustar|version|terminator|end-of-archive|trailing|padding|directory.*size/i,
    );
  });
}

for (const type of ['x', 'g', 'L', 'K']) {
  test(`rejects unsupported tar metadata type ${type}`, async () => {
    await withTarball(
      [{ name: 'package/dist/metadata', type }],
      async (path) => assert.rejects(verifyTarball(path), /unsupported special/i),
    );
  });
}

for (const name of [
  'package/src/index.ts',
  'package/test/cli.test.mjs',
  'package/tsconfig.json',
  'package/scripts/build.mjs',
  'package/node_modules/native/index.js',
  'package/unknown/file.txt',
]) {
  test(`rejects forbidden or unknown tarball path ${name}`, async () => {
    await withTarball(
      [{ name, body: 'unexpected' }],
      async (path) => assert.rejects(verifyTarball(path), /forbidden|unknown|not declared/i),
    );
  });
}

test('rejects node_modules as a nested tarball path segment', async () => {
  const distOnlyManifest = JSON.stringify({
    name: '@kuai-ai/fixture',
    version: '0.0.0',
    files: ['dist'],
    dependencies: {},
    optionalDependencies: {},
    peerDependencies: {},
  });
  await withTarball(
    [{ name: 'package/dist/node_modules/x.js', body: 'unexpected' }],
    async (path) => assert.rejects(verifyTarball(path), /node_modules|forbidden/i),
    { manifest: distOnlyManifest, includeBin: false },
  );
});

function packageManifest(files) {
  return JSON.stringify({
    name: '@kuai-ai/fixture',
    version: '0.0.0',
    files,
    dependencies: {},
    optionalDependencies: {},
    peerDependencies: {},
  });
}

test('rejects backslashes in tarball entry paths', async () => {
  await withTarball(
    [{ name: 'package/dist/dir\\escape.js', body: 'unexpected' }],
    async (path) => assert.rejects(verifyTarball(path), /backslash|invalid.*path/i),
  );
});

test('rejects ASCII control characters in tarball entry paths', async () => {
  await withTarball(
    [{ name: 'package/dist/\x01escape.js', body: 'unexpected' }],
    async (path) => assert.rejects(verifyTarball(path), /control|invalid.*path/i),
  );
});

test('forbidden top-level tarball paths are case-insensitive', async () => {
  await withTarball(
    [{ name: 'package/SRC/index.js', body: 'unexpected' }],
    async (path) => assert.rejects(verifyTarball(path), /forbidden.*path/i),
    { manifest: packageManifest(['SRC']), includeBin: false },
  );
});

test('rejects case-insensitive tarball path collisions', async () => {
  await withTarball(
    [
      { name: 'package/dist/A.js', body: 'one' },
      { name: 'package/dist/a.js', body: 'two' },
    ],
    async (path) => assert.rejects(verifyTarball(path), /duplicate|collision/i),
  );
});

test('rejects file and directory entries with the same canonical path', async () => {
  await withTarball(
    [
      { name: 'package/dist/a', body: 'file' },
      { name: 'package/dist/a/', type: '5' },
    ],
    async (path) => assert.rejects(verifyTarball(path), /duplicate|conflict/i),
  );
});

test('rejects a regular file used as an ancestor path', async () => {
  await withTarball(
    [
      { name: 'package/dist/a', body: 'file' },
      { name: 'package/dist/a/child.js', body: 'child' },
    ],
    async (path) => assert.rejects(verifyTarball(path), /ancestor|conflict/i),
  );
});

test('accepts a directory used as an ancestor path', async () => {
  await withTarball([
    { name: 'package/dist/a/', type: '5' },
    { name: 'package/dist/a/child.js', body: 'child' },
  ], verifyTarball);
});

for (const invalidPath of ['dist\\escape', 'dist/\x01escape']) {
  test(`rejects invalid package.json files path ${JSON.stringify(invalidPath)}`, async () => {
    await withTarball(
      [],
      async (path) => assert.rejects(verifyTarball(path), /backslash|control|invalid.*path/i),
      { manifest: packageManifest([invalidPath]), includeBin: false },
    );
  });
}

test('rejects case-insensitive package.json files collisions', async () => {
  await withTarball(
    [],
    async (path) => assert.rejects(verifyTarball(path), /duplicate|collision/i),
    { manifest: packageManifest(['dist', 'DIST']), includeBin: false },
  );
});

test('rejects a tarball path outside the package prefix', async () => {
  await withTarball(
    [{ name: 'escape.txt', body: 'unexpected' }],
    async (path) => assert.rejects(verifyTarball(path), /package prefix|path/i),
  );
});

test('rejects symlinks in a tarball', async () => {
  await withTarball(
    [{ name: 'package/dist/link', type: '2', linkname: '../package.json' }],
    async (path) => assert.rejects(verifyTarball(path), /symbolic link/i),
  );
});

for (const [label, type] of [['hard link', '1'], ['character device', '3'], ['block device', '4'], ['FIFO', '6']]) {
  test(`rejects a ${label} entry in a tarball`, async () => {
    await withTarball(
      [{ name: 'package/dist/special', type }],
      async (path) => assert.rejects(verifyTarball(path), /unsupported special/i),
    );
  });
}

test('short ordinary files in a tarball are safe', async () => {
  await withTarball([{ name: 'package/dist/short.bin', body: Buffer.from([0x7f]) }], verifyTarball);
});

test('rejects native extension names in a tarball', async () => {
  await withTarball(
    [{ name: 'package/dist/addon.node', body: 'plain text' }],
    async (path) => assert.rejects(verifyTarball(path), /native extension|\.node/i),
  );
});

const nativeMagic = [
  ['ELF', '7f454c46'],
  ['MZ/PE', '4d5a0000'],
  ['Mach-O 32-bit big-endian', 'feedface'],
  ['Mach-O 32-bit little-endian', 'cefaedfe'],
  ['Mach-O 64-bit big-endian', 'feedfacf'],
  ['Mach-O 64-bit little-endian', 'cffaedfe'],
  ['Universal/Fat 32-bit big-endian', 'cafebabe'],
  ['Universal/Fat 32-bit little-endian', 'bebafeca'],
  ['Universal/Fat 64-bit big-endian', 'cafebabf'],
  ['Universal/Fat 64-bit little-endian', 'bfbafeca'],
];

for (const [label, hex] of nativeMagic) {
  test(`rejects ${label} magic in tarball content`, async () => {
    await withTarball(
      [{ name: 'package/dist/payload.bin', body: Buffer.from(hex, 'hex') }],
      async (path) => assert.rejects(verifyTarball(path), /native executable|magic/i),
    );
  });
}

test('audits the actual scoped package produced by npm pack --json', async () => {
  const fixture = mkdtempSync(join(tmpdir(), 'kuai-real-pack-'));
  try {
    const output = runNpmPack(cliRoot, fixture);
    assert.equal(output.length, 1);
    assert.equal(output[0].filename, 'kuai-ai-cli-0.0.0-dev.tgz');
    assert.ok(output[0].files.some((entry) => entry.path === 'package.json'));
    assert.ok(output[0].files.some((entry) => entry.path === 'bin/kuai.js'));
    assert.ok(output[0].files.some((entry) => entry.path === 'dist/cli/main.js'));
    await verifyTarball(join(fixture, output[0].filename));
  } finally {
    rmSync(fixture, { recursive: true, force: true });
  }
});

test('npm pack does not execute package lifecycle scripts', () => {
  const fixture = mkdtempSync(join(tmpdir(), 'kuai-pack-lifecycle-'));
  const destination = join(fixture, 'packed');
  const marker = join(fixture, 'prepack.marker');
  mkdirSync(destination);
  writeFileSync(join(fixture, 'index.js'), 'export {};\n');
  writeFileSync(
    join(fixture, 'write-marker.cjs'),
    "require('node:fs').writeFileSync('prepack.marker', 'executed');\n",
  );
  writeFileSync(join(fixture, 'package.json'), `${JSON.stringify({
    name: 'kuai-pack-lifecycle-fixture',
    version: '0.0.0',
    files: ['index.js'],
    scripts: { prepack: 'node write-marker.cjs' },
  }, null, 2)}\n`);

  try {
    runNpmPack(fixture, destination);
    assert.equal(existsSync(marker), false, 'prepack lifecycle must not execute');
  } finally {
    rmSync(fixture, { recursive: true, force: true });
  }
});

test('npm pack fallback reports when npm is unavailable on PATH', () => {
  const fixture = mkdtempSync(join(tmpdir(), 'kuai-pack-no-npm-'));
  const destination = join(fixture, 'packed');
  mkdirSync(destination);
  try {
    assert.throws(
      () => runNpmPack(fixture, destination, {
        env: { ...process.env, PATH: '' },
        npmExecPath: null,
      }),
      /unable to execute npm from PATH/,
    );
  } finally {
    rmSync(fixture, { recursive: true, force: true });
  }
});

test('tarball policy CLI prints stable success output', async () => {
  await withTarball([], async (path) => {
    const result = spawnSync(process.execPath, [tarballCli, path], {
      encoding: 'utf8',
      shell: false,
    });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stdout, 'tarball policy passed\n');
    assert.equal(result.stderr, '');
  });
});

test('tarball policy CLI reports policy failures and exits non-zero', async () => {
  await withTarball([{ name: 'package/src/index.ts', body: 'forbidden' }], async (path) => {
    const result = spawnSync(process.execPath, [tarballCli, path], {
      encoding: 'utf8',
      shell: false,
    });
    assert.notEqual(result.status, 0);
    assert.equal(result.stdout, '');
    assert.match(result.stderr, /forbidden tarball path: src\/index\.ts/);
  });
});

test('tarball policy CLI rejects a missing tarball argument', () => {
  const result = spawnSync(process.execPath, [tarballCli], {
    encoding: 'utf8',
    shell: false,
  });
  assert.notEqual(result.status, 0);
  assert.equal(result.stdout, '');
  assert.equal(result.stderr, 'usage: node scripts/verify-tarball.mjs <package.tgz>\n');
});
