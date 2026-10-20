const BYTE_UNIT = 1024;
const SIZE_UNITS = ["B", "KB", "MB", "GB", "TB"];

/**
 * Format measured file bytes without implying filesystem allocation precision.
 * @param bytes Logical size of files in bytes.
 * @returns Localized compact size with a binary-scaled unit.
 */
export function formatWorkdirSize(bytes: number): string {
  let size = bytes;
  let unit = 0;
  while (size >= BYTE_UNIT && unit < SIZE_UNITS.length - 1) { size /= BYTE_UNIT; unit += 1; }
  return `${size.toLocaleString(undefined, { maximumFractionDigits: 1 })} ${SIZE_UNITS[unit]}`;
}
