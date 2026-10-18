// Test double for the update source, used when XMESH_UI_TEST=1: serves releases from memory and a tiny
// "installer", so the panel flow can be exercised without the network or a real install.

import { createHash } from "node:crypto";
import * as fs from "node:fs";
import { CHECKSUM_FILE, type RawRelease, type Target, installerName } from "./releases";
import { type Progress, type UpdateSource, SourceError } from "./source";
import { parseVersion } from "../../shared/version";

export interface FakeUpdate {
  /** Versions the fake release list offers (`0.1.0-beta.3`); empty offers none. */
  offered: string[];
  /** Make the next calls fail like a dead network. */
  offline: boolean;
  /** Serve a wrong checksum, as a corrupt download would. */
  corrupt: boolean;
}

export const freshFakeUpdate = (): FakeUpdate => ({ offered: ["0.1.0-beta.3"], offline: false, corrupt: false });

const BODY = Buffer.from("not a real installer");
const sha = (b: Buffer): string => createHash("sha256").update(b).digest("hex");

export function fakeSource(fake: FakeUpdate, target: Target): UpdateSource {
  const releases = (): RawRelease[] =>
    fake.offered.map((version) => {
      const v = parseVersion(version)!;
      return {
        tag_name: `v${version}`,
        draft: false,
        prerelease: v.pre.length > 0,
        published_at: "2026-10-18T00:00:00Z",
        body: `Fake release ${version}`,
        assets: [
          { name: installerName(v, target), size: BODY.length, digest: null },
          { name: CHECKSUM_FILE, size: 100 },
        ],
      };
    });
  const down = (): void => {
    if (fake.offline) throw new SourceError("network", "offline");
  };
  return {
    async releases() {
      down();
      return releases();
    },
    async text(_tag, name) {
      down();
      const lines = releases().flatMap((r) => r.assets.filter((a) => a.name !== CHECKSUM_FILE).map((a) => `${fake.corrupt ? "0".repeat(64) : sha(BODY)}  ${a.name}`));
      void name;
      return lines.join("\n");
    },
    async download(_tag, _name, dest, onProgress: (p: Progress) => void) {
      down();
      fs.writeFileSync(dest, BODY);
      onProgress({ received: BODY.length, total: BODY.length });
      return { sha256: sha(BODY), bytes: BODY.length };
    },
  };
}
