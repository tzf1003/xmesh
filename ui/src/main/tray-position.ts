export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}
export const TRAY_SIZE = { width: 400, height: 580 };
/** All values are DIP, including negative coordinates on displays left of the primary. */
export function trayPosition(anchor: Rect, work: Rect): Rect {
  const gap = 6,
    margin = 8;
  const width = Math.min(TRAY_SIZE.width, Math.max(1, work.width - margin * 2));
  const height = Math.min(TRAY_SIZE.height, Math.max(1, work.height - margin * 2));
  const cx = anchor.x + anchor.width / 2,
    cy = anchor.y + anchor.height / 2;
  const candidates = [
    { x: cx - width / 2, y: anchor.y + anchor.height + gap },
    { x: cx - width / 2, y: anchor.y - height - gap },
    { x: anchor.x + anchor.width + gap, y: cy - height / 2 },
    { x: anchor.x - width - gap, y: cy - height / 2 },
  ];
  const area = (p: { x: number; y: number }): number =>
    Math.max(0, Math.min(p.x + width, work.x + work.width) - Math.max(p.x, work.x)) *
    Math.max(0, Math.min(p.y + height, work.y + work.height) - Math.max(p.y, work.y));
  const preferred =
    candidates.find((p) => area(p) >= width * height) ?? candidates.reduce((a, b) => (area(a) >= area(b) ? a : b));
  return {
    x: Math.round(Math.max(work.x + margin, Math.min(preferred.x, work.x + work.width - width - margin))),
    y: Math.round(Math.max(work.y + margin, Math.min(preferred.y, work.y + work.height - height - margin))),
    width,
    height,
  };
}
