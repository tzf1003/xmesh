import type { JSX } from "preact";

export type IconName = "topology" | "device" | "terminal" | "shield" | "globe" | "activity" | "runner" | "sliders" | "code" | "network" | "info" | "search" | "plus" | "chevron" | "arrow" | "copy" | "folder" | "cloud" | "close";

/** Small outline symbols shared by navigation, tools and the topology. */
export function Icon({ name, class: className = "" }: { name: IconName; class?: string }): JSX.Element {
  const paths: Record<IconName, JSX.Element> = {
    topology: <><circle cx="12" cy="12" r="2"/><circle cx="5" cy="5" r="2"/><circle cx="19" cy="5" r="2"/><circle cx="5" cy="19" r="2"/><circle cx="19" cy="19" r="2"/><path d="m6.5 6.5 4 4m3-0 4-4m-11 11 4-4m3 0 4 4"/></>,
    device: <><rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8m-4-4v4"/></>,
    terminal: <><rect x="3" y="4" width="18" height="16" rx="2"/><path d="m7 9 3 3-3 3m6 0h4"/></>,
    shield: <><path d="m12 3 8 3v5c0 5-4 8-8 10-4-2-8-5-8-10V6l8-3Z"/><path d="m8.5 12 2.5 2.5 4.5-5"/></>,
    globe: <><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c5 5 5 13 0 18-5-5-5-13 0-18Z"/></>,
    activity: <path d="M3 12h4l3-8 4 16 3-8h4"/>,
    runner: <><path d="m9 6 9 6-9 6V6Z"/><circle cx="12" cy="12" r="10"/></>,
    sliders: <><path d="M3 6h10m4 0h4M3 12h4m4 0h10M3 18h10m4 0h4"/><circle cx="15" cy="6" r="2"/><circle cx="9" cy="12" r="2"/><circle cx="15" cy="18" r="2"/></>,
    code: <><path d="m8 6-6 6 6 6m8-12 6 6-6 6m-3-15-2 18"/></>,
    network: <><rect x="8" y="3" width="8" height="6" rx="1"/><rect x="2" y="16" width="7" height="5" rx="1"/><rect x="15" y="16" width="7" height="5" rx="1"/><path d="M12 9v4M5.5 16v-3h13v3"/></>,
    info: <><circle cx="12" cy="12" r="9"/><path d="M12 11v6m0-10v.01"/></>,
    search: <><circle cx="10.5" cy="10.5" r="6.5"/><path d="m16 16 5 5"/></>,
    plus: <path d="M12 5v14M5 12h14"/>,
    chevron: <path d="m9 5 7 7-7 7"/>,
    arrow: <path d="M4 12h16m-6-6 6 6-6 6"/>,
    copy: <><rect x="8" y="8" width="12" height="13" rx="2"/><path d="M16 8V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h3"/></>,
    folder: <path d="M3 7V5a2 2 0 0 1 2-2h5l2 3h7a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7Z"/>,
    cloud: <path d="M7 18a5 5 0 0 1-1-10 6 6 0 0 1 11-1 5.5 5.5 0 0 1 0 11H7Z"/>,
    close: <path d="m6 6 12 12M6 18 18 6"/>,
  };
  return <svg class={`icon ${className}`} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">{paths[name]}</svg>;
}
