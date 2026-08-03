import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import {
  isSupportedNodeVersion,
  parseNodeMajor,
} from '../bin/node-version.js';

test('rejects Node.js 23.11.0', () => {
  assert.equal(parseNodeMajor('23.11.0'), 23);
  assert.equal(isSupportedNodeVersion('23.11.0'), false);
});

test('accepts Node.js 24.0.0', () => {
  assert.equal(parseNodeMajor('24.0.0'), 24);
  assert.equal(isSupportedNodeVersion('24.0.0'), true);
});

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
