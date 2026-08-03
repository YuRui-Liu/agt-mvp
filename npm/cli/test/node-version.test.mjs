import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import {
  copyFileSync,
  mkdirSync,
  mkdtempSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { readFile } from 'node:fs/promises';
import { delimiter, dirname, join } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const nodeVersionModuleUrl = new URL('../bin/node-version.js', import.meta.url).href;
const cliEntrypoint = fileURLToPath(new URL('../bin/kuai.js', import.meta.url));
const helpOutput = `Usage: kuai <command>

Commands:
  help       Show this help
  version    Show the version
`;

async function invokeCli(args) {
  const { main } = await import('../dist/cli/main.js');
  const stdout = [];
  const stderr = [];
  const status = main(args, {
    stdout: (message) => stdout.push(message),
    stderr: (message) => stderr.push(message),
  });

  return { status, stderr: stderr.join(''), stdout: stdout.join('') };
}

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

for (const args of [[], ['help'], ['--help'], ['-h']]) {
  test(`CLI help succeeds for ${args[0] ?? 'no arguments'}`, async () => {
    assert.deepEqual(await invokeCli(args), {
      status: 0,
      stderr: '',
      stdout: helpOutput,
    });
  });
}

for (const command of ['version', '--version', '-v']) {
  test(`CLI version succeeds for ${command}`, async () => {
    assert.deepEqual(await invokeCli([command]), {
      status: 0,
      stderr: '',
      stdout: '0.0.0-dev\n',
    });
  });
}

test('CLI rejects unknown commands', async () => {
  assert.deepEqual(await invokeCli(['scan']), {
    status: 2,
    stderr: 'Unknown command: scan\n',
    stdout: '',
  });
});

for (const [command, status, stdout, stderr] of [
  ['--help', 0, helpOutput, ''],
  ['--version', 0, '0.0.0-dev\n', ''],
  ['scan', 2, '', 'Unknown command: scan\n'],
]) {
  test(`Node 24 entrypoint handles ${command}`, () => {
    const result = spawnSync(process.execPath, [cliEntrypoint, command], {
      encoding: 'utf8',
      shell: false,
    });

    assert.equal(result.status, status, result.stderr);
    assert.equal(result.stdout, stdout);
    assert.equal(result.stderr, stderr);
  });
}

test('package version matches the CLI version export', async () => {
  const packageJson = JSON.parse(
    await readFile(new URL('../package.json', import.meta.url), 'utf8'),
  );
  const { VERSION } = await import('../dist/cli/main.js');

  assert.equal(VERSION, packageJson.version);
});

test('Node 22 rejects the entrypoint before loading a missing dist module', (t) => {
  const legacyNode = findLegacyNode();
  if (legacyNode === undefined) {
    t.skip('legacy Node is covered by the dedicated Node 20 CI job');
    return;
  }

  const fixture = mkdtempSync(join(tmpdir(), 'kuai-node-gate-'));
  const binDirectory = join(fixture, 'bin');
  mkdirSync(binDirectory);

  try {
    copyFileSync(cliEntrypoint, join(binDirectory, 'kuai.js'));
    copyFileSync(
      fileURLToPath(new URL('../bin/node-version.js', import.meta.url)),
      join(binDirectory, 'node-version.js'),
    );
    writeFileSync(join(fixture, 'package.json'), '{"type":"module"}\n');

    const result = spawnSync(legacyNode, [join(binDirectory, 'kuai.js'), '--help'], {
      encoding: 'utf8',
      shell: false,
    });

    assert.equal(result.status, 1, result.stderr);
    assert.equal(result.stdout, '');
    assert.equal(result.stderr, 'Node.js 24 or newer is required\n');
  } finally {
    rmSync(fixture, { recursive: true, force: true });
  }
});

function findLegacyNode() {
  const executableName = process.platform === 'win32' ? 'node.exe' : 'node';
  const currentExecutable = process.execPath;
  const candidates = [
    process.env.KUAI_TEST_LEGACY_NODE,
    ...(process.env.PATH ?? '')
      .split(delimiter)
      .filter(Boolean)
      .map((directory) => join(directory, executableName)),
  ].filter(Boolean);

  for (const candidate of new Set(candidates)) {
    if (candidate === currentExecutable || dirname(candidate) === dirname(currentExecutable)) {
      continue;
    }

    const result = spawnSync(candidate, ['--version'], { encoding: 'utf8', shell: false });
    const major = Number.parseInt(result.stdout?.match(/^v(\d+)/)?.[1] ?? '', 10);
    if (result.status === 0 && major < 24) {
      return candidate;
    }
  }

  return undefined;
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
