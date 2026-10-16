// Generates the application icon (build/icon.png, 1024x1024) without any image dependency.
// electron-builder derives the .ico (Windows) and .icns (macOS) from it. The PNG is committed;
// rerun `npm run icon` after changing the glyph.

import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { crc32, deflateSync } from "node:zlib";

const SIZE = 1024;
const SS = 3;
const out = join(dirname(fileURLToPath(import.meta.url)), "..", "build");
mkdirSync(out, { recursive: true });

// Geometry in a 0..1024 box. Apple's template leaves ~10% margin around the rounded square.
const MARGIN = 100;
const RADIUS = 180;
const roundedSquare = (x, y) => {
  const lo = MARGIN;
  const hi = SIZE - MARGIN;
  if (x < lo || x > hi || y < lo || y > hi) return false;
  const cx = Math.min(Math.max(x, lo + RADIUS), hi - RADIUS);
  const cy = Math.min(Math.max(y, lo + RADIUS), hi - RADIUS);
  return Math.hypot(x - cx, y - cy) <= RADIUS;
};
const disc = (cx, cy, r) => (x, y) => (x - cx) ** 2 + (y - cy) ** 2 <= r * r;
const capsule = (x1, y1, x2, y2, t) => (x, y) => {
  const dx = x2 - x1;
  const dy = y2 - y1;
  const u = Math.max(0, Math.min(1, ((x - x1) * dx + (y - y1) * dy) / (dx * dx + dy * dy)));
  return Math.hypot(x - (x1 + u * dx), y - (y1 + u * dy)) <= t / 2;
};

// Three mesh nodes joined into a triangle.
const A = [512, 300];
const B = [300, 680];
const C = [724, 680];
const links = [capsule(...A, ...B, 44), capsule(...B, ...C, 44), capsule(...C, ...A, 44)];
const nodes = [A, B, C].map(([x, y]) => disc(x, y, 92));
const holes = [A, B, C].map(([x, y]) => disc(x, y, 44));

const TOP = [38, 70, 140];
const BOTTOM = [20, 36, 82];
const WHITE = [255, 255, 255];

function sample(x, y) {
  if (!roundedSquare(x, y)) return null;
  for (const h of holes) if (h(x, y)) return (() => {
    const t = (y - MARGIN) / (SIZE - 2 * MARGIN);
    return TOP.map((v, i) => v + (BOTTOM[i] - v) * t);
  })();
  for (const g of [...links, ...nodes]) if (g(x, y)) return WHITE;
  const t = (y - MARGIN) / (SIZE - 2 * MARGIN);
  return TOP.map((v, i) => v + (BOTTOM[i] - v) * t);
}

const px = Buffer.alloc(SIZE * SIZE * 4);
for (let py = 0; py < SIZE; py++) {
  for (let pxl = 0; pxl < SIZE; pxl++) {
    let r = 0;
    let g = 0;
    let b = 0;
    let n = 0;
    for (let sy = 0; sy < SS; sy++) {
      for (let sx = 0; sx < SS; sx++) {
        const c = sample(pxl + (sx + 0.5) / SS, py + (sy + 0.5) / SS);
        if (c) {
          r += c[0];
          g += c[1];
          b += c[2];
          n++;
        }
      }
    }
    const i = (py * SIZE + pxl) * 4;
    if (n > 0) {
      px[i] = Math.round(r / n);
      px[i + 1] = Math.round(g / n);
      px[i + 2] = Math.round(b / n);
      px[i + 3] = Math.round((255 * n) / (SS * SS));
    }
  }
}

function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const body = Buffer.concat([Buffer.from(type, "ascii"), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body) >>> 0);
  return Buffer.concat([len, body, crc]);
}

const ihdr = Buffer.alloc(13);
ihdr.writeUInt32BE(SIZE, 0);
ihdr.writeUInt32BE(SIZE, 4);
ihdr[8] = 8;
ihdr[9] = 6;
const rows = Buffer.alloc((SIZE * 4 + 1) * SIZE);
for (let y = 0; y < SIZE; y++) px.copy(rows, y * (SIZE * 4 + 1) + 1, y * SIZE * 4, (y + 1) * SIZE * 4);
const file = join(out, "icon.png");
writeFileSync(
  file,
  Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(rows, { level: 9 })),
    chunk("IEND", Buffer.alloc(0)),
  ]),
);
console.log(`wrote ${file}`);
