export interface CliIO {
  stdout(message: string): void;
  stderr(message: string): void;
}

export const VERSION = '0.1.0-mvp.1';

const HELP = `Usage: kuai <command>

Commands:
  help       Show this help
  version    Show the version
`;

export function main(args: readonly string[], io: CliIO): number {
  const [command] = args;

  if (command === undefined || command === 'help' || command === '--help' || command === '-h') {
    io.stdout(HELP);
    return 0;
  }

  if (command === 'version' || command === '--version' || command === '-v') {
    io.stdout(`${VERSION}\n`);
    return 0;
  }

  io.stderr(`Unknown command: ${command}\n`);
  return 2;
}
