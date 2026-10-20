# Application icons

`icon.svg` is the source for desktop icons and the unmasked PWA icons. The
820px tile and 486px mark share the centre of a 1024px canvas. The 102px
tile inset matches the size and alignment of neighbouring macOS app icons;
the faint shadow is excluded from the tile bounds.

Run `pnpm generate:app-icons` from the repository root after editing the SVG.
Run `pnpm generate:app-icons --check` to verify every generated file, including
the ICNS/ICO frames, Linux sizes, runtime icon and ordinary PWA sizes.
Commit the source and generated files together.

Mobile, Apple touch and maskable PWA icons use separate full-bleed artwork.
Their launchers apply masks; do not replace them with this padded desktop tile.
