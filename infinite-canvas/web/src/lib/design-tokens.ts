/**
 * The application's neutral palette — the one place a surface colour changes.
 *
 * One family is in play: `stone`, the warm neutral. Card and panel surfaces,
 * borders, canvas chrome and the primary button ink all come from it. These exact
 * hex values are also the Tailwind `stone-*` scale, which the components use
 * directly (`border-stone-200`, `dark:border-stone-800`, …). Keeping the numbers
 * identical means a Tailwind class and a token-driven style never disagree.
 *
 * A second, colder family used to live here (`pureNeutral`). It existed only to
 * feed antd's `colorPrimary`, and once antd's theme was gone the kit buttons were
 * already using `stone-900`, so the token described a contrast the UI no longer
 * made. It was deleted rather than kept for the description.
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
