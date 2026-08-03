const MINIMUM_NODE_MAJOR = 18;

export function parseNodeMajor(version) {
  const [major] = version.split('.', 1);
  return Number.parseInt(major, 10);
}

export function isSupportedNodeVersion(version) {
  return parseNodeMajor(version) >= MINIMUM_NODE_MAJOR;
}
