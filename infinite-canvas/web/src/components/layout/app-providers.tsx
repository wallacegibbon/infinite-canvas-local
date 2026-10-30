import type { ReactNode } from "react";
import { useEffect } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Toaster } from "sonner";

import { ConfirmHost } from "@/components/kit/confirm";
import { ClientRootInit } from "@/components/layout/client-root-init";
import { GatewayReconnectBanner } from "@/components/layout/gateway-reconnect-banner";
import type { AppLocale } from "@/i18n";
import { useThemeStore } from "@/stores/use-theme-store";

const queryClient = new QueryClient({
    defaultOptions: {
        queries: {
            staleTime: 30_000,
            retry: false,
            refetchOnWindowFocus: false,
        },
    },
});

/**
 * App-level providers.
 *
 * This used to wrap the tree in antd's `ConfigProvider` (for the theme) and `App`
 * (for the static `message` / `modal` helpers). Both are gone: colours come from
 * Tailwind classes over `design-tokens`, toasts from `@/components/kit/toast` and
 * confirmations from `@/components/kit/confirm`. Nothing here is library-specific,
 * so the next replacement has nowhere to leak into (see docs/decisions.md #23/#24).
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
            <QueryClientProvider client={queryClient}>
                <ClientRootInit>{children}</ClientRootInit>
                <GatewayReconnectBanner />
            </QueryClientProvider>
            {/* Toasts are rendered by `@/components/kit/toast` over sonner. Sticky
                loading toasts rely on `duration: Infinity`; 3000 matches antd's old
                default for the auto-dismissing ones so the pacing of the UI did not
                change when it was replaced. */}
            <Toaster position="top-center" theme={dark ? "dark" : "light"} toastOptions={{ duration: 3000 }} />
            <ConfirmHost />
        </>
    );
}
