import type { JSX } from "preact";
import type { PageContext } from "../app";
import { ModuleSettings } from "./ModuleSettings";

export function Configuration({ ctx }: { ctx: PageContext }): JSX.Element {
  return <div class="page configuration-page" data-testid="page-config">
    <ModuleSettings ctx={ctx} scope="node" onDirtyChange={ctx.setConfigDirty}/>
  </div>;
}
