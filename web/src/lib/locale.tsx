import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

export type PrayuLocale = "zh-CN" | "en-US";
export type LocaleText = (chinese: string, english: string) => string;

const localeStorageKey = "prayu.locale.v1";

export function readPrayuLocale(): PrayuLocale {
  if (typeof window === "undefined") return "zh-CN";
  try {
    return window.localStorage.getItem(localeStorageKey) === "en-US" ? "en-US" : "zh-CN";
  } catch {
    return "zh-CN";
  }
}

export function applyPrayuLocale(locale: PrayuLocale) {
  document.documentElement.lang = locale;
  document.documentElement.dataset.prayuLocale = locale;
  try {
    window.localStorage.setItem(localeStorageKey, locale);
  } catch {
    // Language selection remains usable when browser storage is unavailable.
  }
}

export function initializePrayuLocale(): PrayuLocale {
  const locale = readPrayuLocale();
  applyPrayuLocale(locale);
  return locale;
}

type LocaleContextValue = {
  locale: PrayuLocale;
  setLocale: (locale: PrayuLocale) => void;
  t: (chinese: string, english: string) => string;
};

const LocaleContext = createContext<LocaleContextValue>({
  // Production is always wrapped by LocaleProvider and defaults to Chinese.
  // English keeps legacy isolated component tests deterministic when no provider exists.
  locale: "en-US",
  setLocale: () => undefined,
  t: (_chinese, english) => english,
});

export function LocaleProvider({ children }: { children: ReactNode }) {
  const [locale, setLocale] = useState<PrayuLocale>(readPrayuLocale);
  useEffect(() => applyPrayuLocale(locale), [locale]);
  const value = useMemo<LocaleContextValue>(() => ({
    locale,
    setLocale,
    t: (chinese, english) => locale === "zh-CN" ? chinese : english,
  }), [locale]);
  return <LocaleContext.Provider value={value}>{children}</LocaleContext.Provider>;
}

export function useLocale(): LocaleContextValue {
  return useContext(LocaleContext);
}
