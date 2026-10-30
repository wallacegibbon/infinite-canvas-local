import type { ButtonHTMLAttributes, ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * The plain button this app draws itself, shaped to sit next to what antd still renders.
 *
 * It exists because the kit already needed the same three looks in three places — the
 * confirm dialog, the toast-free dialog footers and the settings footers — and copying
 * Tailwind strings around is how "the same" button ends up looking different in each.
 *
 * There is no `asChild` and no icon slot: pass an icon as a child, it is laid out by the
 * gap. Sizes match antd's `middle` (h-8) and `small` (h-7) so a kit button and an antd
 * button on the same row line up.
 */
type Variant = "default" | "primary" | "danger";
type Size = "sm" | "md";

const BASE = "inline-flex shrink-0 items-center justify-center gap-1.5 rounded-md font-normal whitespace-nowrap transition-colors outline-none focus-visible:ring-2 focus-visible:ring-stone-400/40 disabled:cursor-not-allowed disabled:opacity-60";

const SIZES: Record<Size, string> = {
    sm: "h-7 px-2.5 text-[13px]",
    md: "h-8 px-3 text-sm",
};

const VARIANTS: Record<Variant, string> = {
    default:
        "border border-stone-300 bg-white text-stone-700 hover:border-stone-400 hover:bg-stone-50 dark:border-stone-700 dark:bg-stone-900 dark:text-stone-200 dark:hover:border-stone-600 dark:hover:bg-stone-800",
    primary: "bg-stone-900 font-medium text-white hover:bg-stone-800 dark:bg-stone-100 dark:text-stone-900 dark:hover:bg-white",
    danger: "bg-red-600 font-medium text-white hover:bg-red-500",
};

export function Button({ variant = "default", size = "md", className, children, type = "button", ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; size?: Size }): ReactNode {
    return (
        <button type={type} className={cn(BASE, SIZES[size], VARIANTS[variant], className)} {...props}>
            {children}
        </button>
    );
}
