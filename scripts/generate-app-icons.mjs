#!/usr/bin/env node
// Regenerate committed desktop and unmasked PWA icons from one vector source.
import { readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import sharp from "sharp";

const root = new URL("../", import.meta.url);
const source = await readFile(new URL("apps/desktop/build/icon.svg", root));
const check = process.argv.includes("--check");
const sizes = [16, 24, 32, 48, 64, 128, 192, 256, 512, 1024];
const pngs = new Map();
for (const size of sizes) {
  pngs.set(size, await sharp(source, { density: 288 })
    .resize(size, size)
    .png({ compressionLevel: 9 })
    .toBuffer());
}

const outputs = new Map([
  ["apps/desktop/build/icon.png", pngs.get(1024)],
  ["apps/desktop/resources/icon.png", pngs.get(1024)],
  ["apps/web/public/icons/icon-192.png", pngs.get(192)],
  ["apps/web/public/icons/icon-512.png", pngs.get(512)],
]);
for (const size of [16, 24, 32, 48, 64, 128, 256, 512]) {
  outputs.set(`apps/desktop/build/icons/${size}x${size}.png`, pngs.get(size));
}

// Use PNG-backed ICNS slots supported by macOS's native icon decoder.
// Smaller standard-density icons fall back to the 32px Retina representation.
const representations = [
  ["ic07", 128], ["ic08", 256], ["ic09", 512], ["ic10", 1024],
  ["ic11", 32], ["ic12", 64], ["ic13", 256], ["ic14", 512],
];
const chunks = representations.map(([type, size]) => {
  const png = pngs.get(size);
  const header = Buffer.alloc(8);
  header.write(type);
  header.writeUInt32BE(png.length + 8, 4);
  return Buffer.concat([header, png]);
});
const icnsHeader = Buffer.alloc(8);
icnsHeader.write("icns");
icnsHeader.writeUInt32BE(8 + chunks.reduce((sum, chunk) => sum + chunk.length, 0), 4);
outputs.set("apps/desktop/build/icon.icns", Buffer.concat([icnsHeader, ...chunks]));

// Modern Windows supports PNG-backed ICO entries, including the 256px frame.
const icoSizes = [16, 24, 32, 48, 64, 128, 256];
const icoHeader = Buffer.alloc(6 + icoSizes.length * 16);
icoHeader.writeUInt16LE(1, 2);
icoHeader.writeUInt16LE(icoSizes.length, 4);
let offset = icoHeader.length;
for (const [index, size] of icoSizes.entries()) {
  const entry = 6 + index * 16;
  const png = pngs.get(size);
  icoHeader[entry] = size === 256 ? 0 : size;
  icoHeader[entry + 1] = size === 256 ? 0 : size;
  icoHeader.writeUInt16LE(1, entry + 4);
  icoHeader.writeUInt16LE(32, entry + 6);
  icoHeader.writeUInt32LE(png.length, entry + 8);
  icoHeader.writeUInt32LE(offset, entry + 12);
  offset += png.length;
}
outputs.set("apps/desktop/build/icon.ico",
  Buffer.concat([icoHeader, ...icoSizes.map((size) => pngs.get(size))]));

for (const [path, data] of outputs) {
  const destination = new URL(path, root);
  if (check) {
    const committed = await readFile(destination);
    if (!data.equals(committed)) {
      throw new Error(`Stale icon: ${path}. Run pnpm generate:app-icons.`);
    }
  } else {
    await writeFile(destination, data);
  }
}
console.log(`${check ? "Verified" : "Generated"} ${outputs.size} icons from ${fileURLToPath(new URL("apps/desktop/build/icon.svg", root))}`);
