import type { ReactNode } from "react";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { Toaster } from "sonner";

import { ConfirmHost } from "@/components/kit/confirm";
import { ClientRootInit } from "@/components/layout/client-root-init";
import { GatewayReconnectBanner } from "@/components/layout/gateway-reconnect-banner";
import type { AppLocale } from "@/i18n";
import { useThemeStore } from "@/stores/use-theme-store";

/**
 * App-level providers.
 *
 * This used to wrap the tree in three providers: antd's `ConfigProvider` (theme) and
 * `App` (static `message` / `modal` helpers), and react-query's `QueryClientProvider`.
 * All three are gone. Colours come from Tailwind classes over `design-tokens`, toasts
 * from `@/components/kit/toast`, confirmations from `@/components/kit/confirm`, and
 * every network call is a plain async function in `services/` — nothing ever called
 * `useQuery`, so the client was a cache that only ever stayed empty. Nothing here is
 * library-specific now, so the next replacement has nowhere to leak into (#23/#24).
 *
 * The two effects are the whole of the app's theme and locale plumbing: the `dark`
 * class on `<html>` is what every `dark:` utility keys off, and `colorScheme` is
 * what makes native scrollbars and form controls follow.
 */
export function AppProviders({ children }: { children: ReactNode }) {
    const { i18n, t } = useTranslation();
    const theme = useThemeStore((state) => state.theme);
    const dark = theme === "dark";
    const locale = i18n.resolvedLanguage as AppLocale;

    useEffect(() => {
        document.documentElement.classList.toggle("dark", dark);
        document.documentElement.style.colorScheme = theme;
    }, [dark, theme]);

    useEffect(() => {
        document.documentElement.lang = locale;
        document.title = t("meta.title");
        document.querySelector('meta[name="description"]')?.setAttribute("content", t("meta.description"));
    }, [locale, t]);

    return (
        <>
            <ClientRootInit>{children}</ClientRootInit>
            <GatewayReconnectBanner />
            {/* Toasts are rendered by `@/components/kit/toast` over sonner. Sticky
                loading toasts rely on `duration: Infinity`; 3000 matches antd's old
                default for the auto-dismissing ones so the pacing of the UI did not
                change when it was replaced. */}
            <Toaster position="top-center" theme={dark ? "dark" : "light"} toastOptions={{ duration: 3000 }} />
            <ConfirmHost />
        </>
    );
}
