import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
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

function tarHeader(name, size, type = '0', linkname = '') {
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
  header.write('ustar\0', 257, 6, 'ascii');
  header.write('00', 263, 2, 'ascii');
  const checksum = [...header].reduce((sum, byte) => sum + byte, 0);
  header.write(`${checksum.toString(8).padStart(6, '0')}\0 `, 148, 8, 'ascii');
  return header;
}

function createTarball(entries) {
  const fixture = mkdtempSync(join(tmpdir(), 'kuai-tarball-policy-'));
  const path = join(fixture, 'kuai-ai-cli-0.0.0-dev.tgz');
  const chunks = [];

  for (const entry of entries) {
    const body = Buffer.isBuffer(entry.body) ? entry.body : Buffer.from(entry.body ?? '');
    chunks.push(tarHeader(entry.name, body.length, entry.type, entry.linkname));
    chunks.push(body);
    chunks.push(Buffer.alloc((512 - (body.length % 512)) % 512));
  }
  chunks.push(Buffer.alloc(1024));
  writeFileSync(path, gzipSync(Buffer.concat(chunks)));
  return { fixture, path };
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

function runNpmPack(packageRoot, destination) {
  const npmExecPath = process.env.npm_execpath;
  const npmCommand = npmExecPath
    ? process.execPath
    : join(dirname(process.execPath), process.platform === 'win32' ? 'npm.cmd' : 'npm');
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
    { cwd: packageRoot, encoding: 'utf8', shell: false },
  );
  assert.equal(result.status, 0, result.stderr);
  return JSON.parse(result.stdout);
}

test('accepts a minimal tarball containing only declared publish paths', async () => {
  await withTarball([{ name: 'package/dist/cli/main.js', body: 'export {};\n' }], verifyTarball);
});

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
