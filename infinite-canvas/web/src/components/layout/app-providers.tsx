import type { ReactNode } from "react";
import { useEffect } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ConfigProvider } from "antd";
import enUS from "antd/es/locale/en_US";
import zhCN from "antd/es/locale/zh_CN";
import dayjs from "dayjs";
import "dayjs/locale/zh-cn";
import { useTranslation } from "react-i18next";
import { Toaster } from "sonner";

import { ConfirmHost } from "@/components/kit/confirm";
import { ClientRootInit } from "@/components/layout/client-root-init";
import { GatewayReconnectBanner } from "@/components/layout/gateway-reconnect-banner";
import type { AppLocale } from "@/i18n";
import { getAntThemeConfig } from "@/lib/app-theme";
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
 * antd is wrapped in `ConfigProvider` only — its `App` component used to be here to
 * provide context for the static `message` / `modal` helpers. Nothing calls those any
 * more: toasts come from `@/components/kit/toast` and confirmations from
 * `@/components/kit/confirm`. Do not reintroduce `App.useApp()`; add a kit primitive
 * instead, so the library stays replaceable (see docs/decisions.md #23/#24).
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
        dayjs.locale(locale === "zh-CN" ? "zh-cn" : "en");
    }, [locale, t]);

    return (
        <ConfigProvider locale={locale === "zh-CN" ? zhCN : enUS} theme={getAntThemeConfig(dark)}>
            <QueryClientProvider client={queryClient}>
                <ClientRootInit>{children}</ClientRootInit>
                <GatewayReconnectBanner />
            </QueryClientProvider>
            {/* Toasts are rendered by `@/components/kit/toast`, not by antd. Sticky
                loading toasts rely on `duration: Infinity`; 3000 matches antd's default
                for the auto-dismissing ones so the pacing of the UI does not change. */}
            <Toaster position="top-center" theme={dark ? "dark" : "light"} toastOptions={{ duration: 3000 }} />
            <ConfirmHost />
        </ConfigProvider>
    );
}
