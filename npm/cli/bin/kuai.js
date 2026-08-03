#!/usr/bin/env node

import { isSupportedNodeVersion } from './node-version.js';

if (!isSupportedNodeVersion(process.versions.node)) {
  console.error('Node.js 24 or newer is required');
  process.exitCode = 1;
} else {
  const { main } = await import('../dist/cli/main.js');
  process.exitCode = main(process.argv.slice(2), {
    stdout: (message) => process.stdout.write(message),
    stderr: (message) => process.stderr.write(message),
  });
}
