import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import {
  mkdtempSync,
  mkdirSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

import { verifyPackageDirectory } from '../scripts/verify-package-policy.mjs';

const cliRoot = fileURLToPath(new URL('..', import.meta.url));
const policyCli = fileURLToPath(new URL('../scripts/verify-package-policy.mjs', import.meta.url));

async function withPackageFixture(mutator, assertion) {
  const root = mkdtempSync(join(tmpdir(), 'kuai-package-policy-'));
  const manifest = {
    name: '@kuai-ai/fixture',
    version: '0.0.0',
    scripts: {},
    dependencies: {},
    optionalDependencies: {},
    peerDependencies: {},
  };
  writeFileSync(join(root, 'package.json'), `${JSON.stringify(manifest, null, 2)}\n`);

  try {
    mutator({ manifest, root });
    return await assertion(root);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

function writeManifest(root, manifest) {
  writeFileSync(join(root, 'package.json'), `${JSON.stringify(manifest, null, 2)}\n`);
}

test('the normal @kuai-ai/cli directory passes package policy', async () => {
  await verifyPackageDirectory(cliRoot);
});

for (const hook of ['preinstall', 'install', 'postinstall', 'prepare']) {
  test(`rejects the ${hook} lifecycle property regardless of value`, async () => {
    await withPackageFixture(
      ({ manifest, root }) => {
        manifest.scripts[hook] = false;
        writeManifest(root, manifest);
      },
      async (root) => assert.rejects(
        verifyPackageDirectory(root),
        new RegExp(`forbidden lifecycle script: ${hook}`),
      ),
    );
  });
}

for (const field of ['dependencies', 'optionalDependencies', 'peerDependencies']) {
  test(`rejects non-empty ${field}`, async () => {
    await withPackageFixture(
      ({ manifest, root }) => {
        manifest[field] = { native: '1.0.0' };
        writeManifest(root, manifest);
      },
      async (root) => assert.rejects(verifyPackageDirectory(root), new RegExp(field)),
    );
  });

  for (const invalidValue of [null, [], 'none']) {
    test(`rejects ${field} with invalid value ${JSON.stringify(invalidValue)}`, async () => {
      await withPackageFixture(
        ({ manifest, root }) => {
          manifest[field] = invalidValue;
          writeManifest(root, manifest);
        },
        async (root) => assert.rejects(verifyPackageDirectory(root), new RegExp(field)),
      );
    });
  }
}

test('rejects a .node file without relying on its contents', async () => {
  await withPackageFixture(
    ({ root }) => writeFileSync(join(root, 'addon.node'), 'plain text'),
    async (root) => assert.rejects(verifyPackageDirectory(root), /\.node|native extension/i),
  );
});

const forbiddenMagic = [
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

for (const [label, hex] of forbiddenMagic) {
  test(`rejects ${label} magic`, async () => {
    await withPackageFixture(
      ({ root }) => writeFileSync(join(root, 'payload.bin'), Buffer.from(hex, 'hex')),
      async (root) => assert.rejects(verifyPackageDirectory(root), /native executable|magic/i),
    );
  });
}

test('short ordinary files are safe', async () => {
  await withPackageFixture(
    ({ root }) => writeFileSync(join(root, 'short.txt'), Buffer.from([0x7f])),
    async (root) => verifyPackageDirectory(root),
  );
});

test('rejects a symbolic link without following it', async () => {
  await withPackageFixture(
    ({ root }) => {
      writeFileSync(join(root, 'target.txt'), 'safe');
      symlinkSync('target.txt', join(root, 'link.txt'));
    },
    async (root) => assert.rejects(verifyPackageDirectory(root), /symbolic link/i),
  );
});

test('rejects package.json when it is a symbolic link before reading it', async () => {
  await withPackageFixture(
    ({ root }) => {
      rmSync(join(root, 'package.json'));
      symlinkSync('missing-target.json', join(root, 'package.json'));
    },
    async (root) => assert.rejects(verifyPackageDirectory(root), /symbolic link/i),
  );
});

test('rejects a symbolic link passed as the package root', async () => {
  const fixture = mkdtempSync(join(tmpdir(), 'kuai-package-root-link-'));
  const target = join(fixture, 'target');
  const link = join(fixture, 'link');
  mkdirSync(target);
  writeFileSync(join(target, 'package.json'), '{"name":"fixture"}\n');
  symlinkSync('target', link);
  try {
    await assert.rejects(verifyPackageDirectory(link), /symbolic link/i);
  } finally {
    rmSync(fixture, { recursive: true, force: true });
  }
});

test('rejects a special package.json before attempting to read it', { skip: process.platform === 'win32' }, () => {
  const root = mkdtempSync(join(tmpdir(), 'kuai-special-manifest-'));
  try {
    const result = spawnSync('mkfifo', [join(root, 'package.json')], {
      encoding: 'utf8',
      shell: false,
    });
    if (result.error?.code === 'ENOENT') return;
    assert.equal(result.status, 0, result.stderr);

    const verification = spawnSync(process.execPath, [policyCli, root], {
      encoding: 'utf8',
      shell: false,
      timeout: 1_000,
    });
    assert.notEqual(verification.status, 0);
    assert.equal(verification.stdout, '');
    assert.match(verification.stderr ?? '', /special|regular file|unsupported/i);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('rejects an unknown special filesystem entry', { skip: process.platform === 'win32' }, async (t) => {
  await withPackageFixture(
    ({ root }) => {
      const fifo = join(root, 'special');
      const result = spawnSync('mkfifo', [fifo], { encoding: 'utf8', shell: false });
      if (result.error?.code === 'ENOENT') {
        t.skip('mkfifo is unavailable');
        return;
      }
      assert.equal(result.status, 0, result.stderr);
    },
    async (root) => assert.rejects(verifyPackageDirectory(root), /special|unsupported/i),
  );
});

test('package policy CLI prints stable success output', () => {
  const result = spawnSync(process.execPath, [policyCli, cliRoot], {
    encoding: 'utf8',
    shell: false,
  });

  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, 'pure-js package policy passed\n');
  assert.equal(result.stderr, '');
});

test('package policy CLI reports a clear failure and exits non-zero', async () => {
  await withPackageFixture(
    ({ manifest, root }) => {
      manifest.scripts.install = 'exit 99';
      writeManifest(root, manifest);
    },
    async (root) => {
      const result = spawnSync(process.execPath, [policyCli, root], {
        encoding: 'utf8',
        shell: false,
      });
      assert.notEqual(result.status, 0);
      assert.equal(result.stdout, '');
      assert.match(result.stderr, /forbidden lifecycle script: install/);
      assert.doesNotMatch(result.stderr, /exit 99/);
    },
  );
});
