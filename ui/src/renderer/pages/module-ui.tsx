import type { ComponentChildren, JSX } from "preact";
import { Icon, type IconName } from "../icons";
import { useModuleT } from "./module-messages";

export function ModuleBreadcrumb({ title }: { title: string }): JSX.Element {
  const m = useModuleT();
  return <div class="module-breadcrumb">{m("modules")}<span>/</span><strong>{title}</strong></div>;
}
export function ModuleSummary({ icon, title, detail, children }: { icon: IconName; title: ComponentChildren; detail: ComponentChildren; children: ComponentChildren }): JSX.Element {
  return <section class="module-summary"><div class="module-summary-main"><span class="module-avatar"><Icon name={icon}/></span><div><h2>{title}</h2><div class="module-summary-detail">{detail}</div></div></div><div class="module-summary-numbers">{children}</div></section>;
}
export function ModuleStat({ value, label, tone = "" }: { value: number | string; label: string; tone?: "" | "ok" | "warn" }): JSX.Element {
  return <div class={`module-stat ${tone}`}><strong>{value}</strong><span>{label}</span></div>;
}
export function ModuleBox({ title, icon, children, action }: { title: string; icon?: IconName; children: ComponentChildren; action?: ComponentChildren }): JSX.Element {
  return <section class="module-box"><div class="module-box-heading"><h3>{title}</h3>{action ?? (icon && <Icon name={icon}/>)}</div>{children}</section>;
}
export function ModuleFacts({ items }: { items: readonly [string, ComponentChildren][] }): JSX.Element {
  return <dl class="module-facts">{items.map(([name, value]) => <div key={name}><dt>{name}</dt><dd>{value}</dd></div>)}</dl>;
}
