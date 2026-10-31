import i18n from "i18next";
import { initReactI18next } from "react-i18next";

import enUS from "@/i18n/locales/en-US";
import zhCN from "@/i18n/locales/zh-CN";

export type AppLocale = "zh-CN" | "en-US";

const LOCALE_STORAGE_KEY = "infinite-canvas:locale";

/**
 * The stored language, when there is a place to store it.
 *
 * The app only runs in a browser, but the dictionary is also read by code that runs outside one (a
 * storage migration under test), and `localStorage` does not exist there. Reading it defensively keeps
 * that import from becoming a crash.
 */
function storedLocale(): AppLocale | null {
    try {
        const value = globalThis.localStorage?.getItem(LOCALE_STORAGE_KEY);
        return value === "zh-CN" || value === "en-US" ? value : null;
    } catch {
        return null;
    }
}

i18n.use(initReactI18next).init({
    resources: {
        "zh-CN": { translation: zhCN },
        "en-US": { translation: enUS },
    },
    lng: storedLocale() || "zh-CN",
    fallbackLng: "zh-CN",
    supportedLngs: ["zh-CN", "en-US"],
    initAsync: false,
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
});

export function changeAppLocale(locale: AppLocale) {
    localStorage.setItem(LOCALE_STORAGE_KEY, locale);
    return i18n.changeLanguage(locale);
}

export default i18n;
