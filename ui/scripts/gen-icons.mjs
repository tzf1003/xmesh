// Generates the tray icons into assets/tray without any image dependency.
//
//   tray-<state>.png / tray-<state>@2x.png          coloured, used on Windows
//   tray-<state>Template.png / ...Template@2x.png    black + alpha, used on macOS (the OS tints it)
//
// The PNGs are committed; rerun `npm run icons` after changing a glyph.

import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { deflateSync, crc32 } from "node:zlib";

const out = join(dirname(fileURLToPath(import.meta.url)), "..", "assets", "tray");
mkdirSync(out, { recursive: true });

const WHITE = [255, 255, 255, 255];
const BLACK = [0, 0, 0, 255];

const disc = (cx, cy, r) => (x, y) => (x - cx) ** 2 + (y - cy) ** 2 <= r * r;
const ring = (cx, cy, r, w) => (x, y) => {
  const d = Math.hypot(x - cx, y - cy);
  return d <= r && d >= r - w;
};
const capsule = (x1, y1, x2, y2, t) => (x, y) => {
  const dx = x2 - x1;
  const dy = y2 - y1;
  const len2 = dx * dx + dy * dy;
  const u = len2 === 0 ? 0 : Math.max(0, Math.min(1, ((x - x1) * dx + (y - y1) * dy) / len2));
  return Math.hypot(x - (x1 + u * dx), y - (y1 + u * dy)) <= t / 2;
};

const glyphs = {
  offline: [capsule(10, 16, 22, 16, 3.2)],
  ok: [capsule(9, 17, 14, 22, 3.2), capsule(14, 22, 23, 11, 3.2)],
  degraded: [capsule(16, 9, 16, 18, 3.4), disc(16, 23, 2)],
  draining: [capsule(12, 10, 12, 22, 3.6), capsule(20, 10, 20, 22, 3.6)],
  restart: [capsule(9, 17, 14, 22, 3.2), capsule(14, 22, 23, 11, 3.2)],
  mismatch: [capsule(10, 10, 22, 22, 3.2), capsule(22, 10, 10, 22, 3.2)],
};
const base = {
  offline: [138, 143, 152, 255],
  ok: [46, 160, 67, 255],
  degraded: [210, 153, 34, 255],
  draining: [91, 107, 130, 255],
  restart: [46, 160, 67, 255],
  mismatch: [218, 54, 51, 255],
};
const DOT = [240, 136, 62, 255];

/** Layers painted in order; later layers win. Each is [test, rgba]. */
function layers(state, template) {
  const L = [];
  if (template) {
    L.push([ring(16, 16, 14.5, 2.4), BLACK]);
    for (const g of glyphs[state]) L.push([g, BLACK]);
    if (state === "restart") L.push([disc(24, 8, 5.5), BLACK]);
  } else {
    L.push([disc(16, 16, 14.5), base[state]]);
    for (const g of glyphs[state]) L.push([g, WHITE]);
    if (state === "restart") {
      L.push([disc(24, 8, 7.5), WHITE]);
      L.push([disc(24, 8, 5.5), DOT]);
    }
  }
  return L;
}

function render(state, template, size) {
  const L = layers(state, template);
  const scale = 32 / size;
  const SS = 4;
  const px = Buffer.alloc(size * size * 4);
  for (let py = 0; py < size; py++) {
    for (let pxl = 0; pxl < size; pxl++) {
      let r = 0;
      let g = 0;
      let b = 0;
      let a = 0;
      for (let sy = 0; sy < SS; sy++) {
        for (let sx = 0; sx < SS; sx++) {
          const x = (pxl + (sx + 0.5) / SS) * scale;
          const y = (py + (sy + 0.5) / SS) * scale;
          let c;
          for (const [test, rgba] of L) if (test(x, y)) c = rgba;
          if (c) {
            r += c[0];
            g += c[1];
            b += c[2];
            a += c[3];
          }
        }
      }
      const n = SS * SS;
      const i = (py * size + pxl) * 4;
      const alpha = a / n;
      // un-premultiply: colour channels were summed only over covered samples
      const covered = a / 255;
      px[i] = covered ? Math.round(r / covered) : 0;
      px[i + 1] = covered ? Math.round(g / covered) : 0;
      px[i + 2] = covered ? Math.round(b / covered) : 0;
      px[i + 3] = Math.round(alpha);
    }
  }
  return png(size, px);
}

function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const body = Buffer.concat([Buffer.from(type, "ascii"), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body) >>> 0);
  return Buffer.concat([len, body, crc]);
}

function png(size, rgba) {
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // RGBA
  const rows = Buffer.alloc((size * 4 + 1) * size);
  for (let y = 0; y < size; y++) {
    rows[y * (size * 4 + 1)] = 0;
    rgba.copy(rows, y * (size * 4 + 1) + 1, y * size * 4, (y + 1) * size * 4);
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(rows, { level: 9 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

for (const state of Object.keys(glyphs)) {
  writeFileSync(join(out, `tray-${state}.png`), render(state, false, 16));
  writeFileSync(join(out, `tray-${state}@2x.png`), render(state, false, 32));
  writeFileSync(join(out, `tray-${state}Template.png`), render(state, true, 16));
  writeFileSync(join(out, `tray-${state}Template@2x.png`), render(state, true, 32));
}
console.log(`wrote ${Object.keys(glyphs).length * 4} icons to ${out}`);
