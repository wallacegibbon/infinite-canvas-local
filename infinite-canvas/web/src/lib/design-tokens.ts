/**
 * The application's neutral palette — the one place a surface colour changes.
 *
 * Two colour families are in play, on purpose:
 *
 *   `stone`       warm neutral. Card and panel surfaces, borders, canvas chrome.
 *                 These exact hex values are also the Tailwind `stone-*` scale,
 *                 which the components already use directly (`border-stone-200`,
 *                 `dark:border-stone-800`, …). Keeping the numbers identical
 *                 means a Tailwind class and a token-driven style never disagree.
 *
 *   `pureNeutral` cold neutral. Only used for the primary/accent "ink" so buttons
 *                 read as true black rather than warm dark brown. It matches
 *                 shadcn's default `neutral` scale, which `globals.css` mirrors as
 *                 oklch. If you ever want one single family, this is the seam to
 *                 collapse — but it changes how every primary button looks.
 *
 * `globals.css` (`:root` / `.dark`) carries the same decisions as oklch custom
 * properties so Tailwind utilities can reach them. It cannot import this file, so
 * when you change a value here, change the matching custom property there too.
 */
export const stone = {
    50: "#fafaf9",
    100: "#f5f5f4",
    200: "#e7e5e4",
    300: "#d6d3d1",
    400: "#a8a29e",
    500: "#78716c",
    600: "#57534e",
    700: "#44403c",
    800: "#292524",
    900: "#1c1917",
    950: "#0c0a09",
} as const;

/** Cold neutrals. Only used for the primary/accent "ink" so buttons read as true black. */
export const pureNeutral = {
    0: "#ffffff",
    50: "#fafafa",
    900: "#171717",
    950: "#000000",
} as const;

/**
 * Semantic slots the UI actually reads. Everything downstream — the antd
 * `ThemeConfig`, inline styles, future primitives — should take colours from
 * here rather than re-deriving them.
 */
export const appPalette = {
    light: {
        /** Primary button / link / focus ink. */
        primary: pureNeutral[900],
        primaryHover: pureNeutral[950],
        /** Text drawn on top of `primary`. */
        primaryText: pureNeutral[0],
        /** Raised surfaces: dropdowns, popovers, modals. */
        elevatedBg: pureNeutral[0],
        itemHoverBg: "rgba(23, 23, 23, 0.06)",
        itemSelectedBg: "rgba(23, 23, 23, 0.1)",
        itemSelectedHoverBg: "rgba(23, 23, 23, 0.14)",
        itemText: pureNeutral[900],
        tableSelectedBg: "rgba(17, 17, 17, 0.05)",
        tableSelectedHoverBg: "rgba(17, 17, 17, 0.08)",
    },
    dark: {
        /** Not `stone[50]`: the dark ink is a cold `neutral-50`, while the
         *  elevated surface below is warm `stone[900]`. That contrast is
         *  intentional and predates this file — keep it that way. */
        primary: pureNeutral[50],
        primaryHover: pureNeutral[0],
        primaryText: pureNeutral[900],
        /** Warm raised surface — `stone[900]`, not the cold `#171717`. */
        elevatedBg: stone[900],
        itemHoverBg: "rgba(250, 250, 249, 0.08)",
        itemSelectedBg: "rgba(250, 250, 249, 0.12)",
        itemSelectedHoverBg: "rgba(250, 250, 249, 0.16)",
        itemText: pureNeutral[50],
        tableSelectedBg: "rgba(255, 255, 255, 0.08)",
        tableSelectedHoverBg: "rgba(255, 255, 255, 0.12)",
    },
} as const;

export type AppColorScheme = keyof typeof appPalette;
export type AppPalette = (typeof appPalette)[AppColorScheme];
