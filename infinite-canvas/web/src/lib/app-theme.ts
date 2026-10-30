import type { ThemeConfig } from "antd";
import { theme as antdTheme } from "antd";

import { appPalette } from "@/lib/design-tokens";

/**
 * Maps our palette onto antd's token model.
 *
 * This file owns *how* our colours are expressed in antd, not *what* they are —
 * the values live in `@/lib/design-tokens`. Component-level overrides are only
 * here where antd has no global token for the surface in question (dropdown,
 * menu, select popup, selected table row), which is why they are spelled out
 * one component at a time instead of being derived.
 */
export function getAntThemeConfig(dark: boolean): ThemeConfig {
    const color = appPalette[dark ? "dark" : "light"];

    return {
        algorithm: dark ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm,
        cssVar: { key: dark ? "infinite-canvas-dark" : "infinite-canvas-light" },
        token: {
            colorPrimary: color.primary,
            colorInfo: color.primary,
            colorLink: color.primary,
            colorLinkHover: color.primaryHover,
            colorLinkActive: color.primary,
            colorTextLightSolid: color.primaryText,
            colorBgElevated: color.elevatedBg,
            controlItemBgHover: color.itemHoverBg,
            controlItemBgActive: color.itemSelectedBg,
            controlItemBgActiveHover: color.itemSelectedHoverBg,
        },
        components: {
            Button: {
                primaryShadow: "none",
            },
            Dropdown: {
                colorBgElevated: color.elevatedBg,
                colorText: color.itemText,
                controlItemBgHover: color.itemHoverBg,
                controlItemBgActive: color.itemSelectedBg,
                controlItemBgActiveHover: color.itemSelectedHoverBg,
            },
            Menu: {
                popupBg: color.elevatedBg,
                itemActiveBg: color.itemSelectedBg,
                itemHoverBg: color.itemHoverBg,
                itemSelectedBg: color.itemSelectedBg,
                itemSelectedColor: color.itemText,
                darkPopupBg: appPalette.dark.elevatedBg,
                darkItemHoverBg: appPalette.dark.itemHoverBg,
                darkItemSelectedBg: appPalette.dark.itemSelectedBg,
                darkItemSelectedColor: appPalette.dark.itemText,
            },
            Select: {
                optionActiveBg: color.itemHoverBg,
                optionSelectedBg: color.itemSelectedBg,
                optionSelectedColor: color.itemText,
            },
            Table: {
                rowSelectedBg: color.tableSelectedBg,
                rowSelectedHoverBg: color.tableSelectedHoverBg,
            },
        },
    };
}
