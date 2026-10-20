import { useLocale } from "./i18n";
/** Small bilingual UI phrases shared by the control pages. */
export function useCopy(): (zh: string, en: string) => string { const locale = useLocale(); return (zh, en) => locale === "en" ? en : zh; }
