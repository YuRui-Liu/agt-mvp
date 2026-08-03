import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const nodeVersionModuleUrl = new URL('../bin/node-version.js', import.meta.url).href;

function runVersionCheck(version) {
  const source = `
    import { isSupportedNodeVersion } from ${JSON.stringify(nodeVersionModuleUrl)};
    process.stdout.write(String(isSupportedNodeVersion(process.argv[1])));
  `;

  return spawnSync(
    process.execPath,
    ['--input-type=module', '-e', source, version],
    { encoding: 'utf8', shell: false },
  );
}

for (const [version, expected] of [['23.11.0', 'false'], ['24.0.0', 'true']]) {
  test(`Node.js ${version} support is ${expected}`, () => {
    const result = runVersionCheck(version);

    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stderr, '');
    assert.equal(result.stdout, expected);
  });
}

test('CLI checks Node.js before dynamically importing the business module', async () => {
  const source = await readFile(new URL('../bin/kuai.js', import.meta.url), 'utf8');
  const gateIndex = source.indexOf('isSupportedNodeVersion(process.versions.node)');
  const importExpression = "import('../dist/cli/main.js')";
  const importIndex = source.indexOf(importExpression);

  assert.notEqual(gateIndex, -1, 'CLI must check the running Node.js version');
  assert.notEqual(importIndex, -1, `CLI must use ${importExpression}`);
  assert.ok(gateIndex < importIndex, 'version gate must run before loading business code');
  assert.doesNotMatch(source, /^\s*import\s+.*dist\/cli\/main\.js/m);
});
