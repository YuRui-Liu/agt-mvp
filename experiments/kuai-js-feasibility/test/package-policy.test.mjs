import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { verifyPackagePolicy } from '../scripts/verify-package-policy.mjs';

const manifestURL = new URL('../package.json', import.meta.url);

test('experiment has no runtime dependencies or lifecycle scripts', async () => {
  const manifest = JSON.parse(await readFile(manifestURL, 'utf8'));
  assert.deepEqual(manifest.dependencies ?? {}, {});
  assert.deepEqual(manifest.optionalDependencies ?? {}, {});
  assert.deepEqual(manifest.peerDependencies ?? {}, {});
  for (const hook of ['preinstall', 'install', 'postinstall', 'prepare']) {
    assert.equal(manifest.scripts?.[hook], undefined);
  }
  assert.equal(manifest.engines.node, '>=24');
});

async function policyRoot(manifest = {}) {
  const root = await mkdtemp(join(tmpdir(), 'kuai-package-policy-'));
  await writeFile(join(root, 'package.json'), JSON.stringify({
    name: 'fixture',
    version: '0.0.0',
    ...manifest,
  }));
  return root;
}

test('policy accepts a dependency-free JavaScript package', async () => {
  const root = await policyRoot();
  await writeFile(join(root, 'index.mjs'), 'export default true;\n');
  await assert.doesNotReject(verifyPackagePolicy(root));
});

test('policy rejects runtime dependencies and lifecycle scripts', async () => {
  const dependencyRoot = await policyRoot({ dependencies: { unsafe: '1.0.0' } });
  await assert.rejects(verifyPackagePolicy(dependencyRoot), /runtime dependencies/);

  const peerRoot = await policyRoot({ peerDependencies: { unsafe: '1.0.0' } });
  await assert.rejects(verifyPackagePolicy(peerRoot), /runtime dependencies/);

  const lifecycleRoot = await policyRoot({ scripts: { postinstall: 'node install.mjs' } });
  await assert.rejects(verifyPackagePolicy(lifecycleRoot), /lifecycle script.*postinstall/);
});

test('policy rejects native extensions and executable file magic', async () => {
  const extensionRoot = await policyRoot();
  await writeFile(join(extensionRoot, 'addon.node'), 'not-even-a-real-addon');
  await assert.rejects(verifyPackagePolicy(extensionRoot), /native extension/);

  const magicRoot = await policyRoot();
  await mkdir(join(magicRoot, 'nested'));
  await writeFile(join(magicRoot, 'nested', 'payload.dat'), Buffer.from([0x7f, 0x45, 0x4c, 0x46, 0x00]));
  await assert.rejects(verifyPackagePolicy(magicRoot), /native executable/);

  for (const [label, magic] of [
    ['little-endian Mach-O 32', [0xce, 0xfa, 0xed, 0xfe]],
    ['little-endian fat Mach-O', [0xbe, 0xba, 0xfe, 0xca]],
    ['big-endian fat64 Mach-O', [0xca, 0xfe, 0xba, 0xbf]],
    ['little-endian fat64 Mach-O', [0xbf, 0xba, 0xfe, 0xca]],
  ]) {
    const root = await policyRoot();
    await writeFile(join(root, 'payload.dat'), Buffer.from([...magic, 0x00]));
    await assert.rejects(verifyPackagePolicy(root), /native executable/, label);
  }
});

const symlinkTest = process.platform === 'win32' ? test.skip : test;

symlinkTest('policy rejects symbolic links instead of silently skipping them', async () => {
  const root = await policyRoot();
  await writeFile(join(root, 'target.mjs'), 'export default true;\n');
  await symlink(join(root, 'target.mjs'), join(root, 'linked.mjs'));

  await assert.rejects(verifyPackagePolicy(root), /symbolic link/);
});
