import { LoaderCircle } from "lucide-react";
import type { ButtonHTMLAttributes, ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * The button this app draws itself.
 *
 * It deliberately keeps antd's prop names — `type`, `size`, `icon`, `iconPlacement`,
 * `danger`, `loading`, `shape` — so replacing `<Button>` from antd with this one is an
 * import swap rather than 116 rewritten call sites. Those names were not antd's
 * invention in the first place; they are the words the call sites already think in.
 *
 * Two structural details are not cosmetic:
 *   - Children are wrapped in `<span data-slot="text">` and the icon in
 *     `<span data-slot="icon">`, because `globals.css` collapses the label of the
 *     canvas composer buttons and depends on being able to select one without the other.
 *   - Sizes match antd's metrics exactly (24 / 32 / 40 px), because these buttons sit in
 *     dense toolbars next to controls that were laid out against those heights.
 */
type ButtonType = "default" | "primary" | "text" | "link";
type ButtonSize = "small" | "middle" | "large";

type ButtonProps = Omit<ButtonHTMLAttributes<HTMLButtonElement>, "type"> & {
    type?: ButtonType;
    size?: ButtonSize;
    danger?: boolean;
    icon?: ReactNode;
    iconPlacement?: "start" | "end";
    loading?: boolean;
    shape?: "default" | "circle" | "round";
};

const BASE =
    "relative inline-flex shrink-0 cursor-pointer items-center justify-center rounded-md border font-normal whitespace-nowrap transition-colors outline-none select-none focus-visible:ring-2 focus-visible:ring-stone-400/40 disabled:cursor-not-allowed disabled:opacity-60";

const SIZES: Record<ButtonSize, string> = {
    small: "h-6 gap-1 px-[7px] text-sm",
    middle: "h-8 gap-1.5 px-4 text-sm",
    large: "h-10 gap-2 px-4 text-base",
};

const CIRCLE_SIZES: Record<ButtonSize, string> = {
    small: "w-6 px-0",
    middle: "w-8 px-0",
    large: "w-10 px-0",
};

const PLAIN: Record<ButtonType, string> = {
    default: "border-stone-300 bg-white text-stone-700 hover:border-stone-400 hover:bg-stone-50 dark:border-stone-700 dark:bg-stone-900 dark:text-stone-200 dark:hover:border-stone-600 dark:hover:bg-stone-800",
    primary: "border-stone-900 bg-stone-900 text-white hover:border-stone-800 hover:bg-stone-800 dark:border-stone-100 dark:bg-stone-100 dark:text-stone-900 dark:hover:border-white dark:hover:bg-white",
    text: "border-transparent bg-transparent text-stone-600 hover:bg-stone-100 dark:text-stone-300 dark:hover:bg-stone-800",
    link: "border-transparent bg-transparent text-stone-900 underline-offset-2 hover:underline dark:text-stone-100",
};

const DANGER: Record<ButtonType, string> = {
    default: "border-red-300 bg-white text-red-600 hover:border-red-400 hover:bg-red-50 dark:border-red-900 dark:bg-stone-900 dark:text-red-400 dark:hover:border-red-800 dark:hover:bg-red-950/40",
    primary: "border-red-600 bg-red-600 text-white hover:border-red-500 hover:bg-red-500 dark:border-red-600 dark:bg-red-600 dark:text-white dark:hover:border-red-500 dark:hover:bg-red-500",
    text: "border-transparent bg-transparent text-red-600 hover:bg-red-50 dark:text-red-400 dark:hover:bg-red-950/40",
    link: "border-transparent bg-transparent text-red-600 hover:underline dark:text-red-400",
};

export function Button({ type = "default", size = "middle", danger = false, icon, iconPlacement = "start", loading = false, shape = "default", className, children, disabled, ...props }: ButtonProps): ReactNode {
    const square = shape === "circle" || shape === "round";
    return (
        <button type="button" disabled={disabled || loading} className={cn(BASE, SIZES[size], square && CIRCLE_SIZES[size], shape === "circle" && "rounded-full", danger ? DANGER[type] : PLAIN[type], className)} {...props}>
            {loading ? <LoaderCircle data-slot="icon" className="size-3.5 shrink-0 animate-spin" /> : iconPlacement === "start" ? startIcon(icon) : null}
            {children === undefined || children === null || children === false ? null : <span data-slot="text">{children}</span>}
            {!loading && iconPlacement === "end" ? startIcon(icon) : null}
        </button>
    );
}

function startIcon(icon: ReactNode): ReactNode {
    if (icon === undefined || icon === null || icon === false) return null;
    return (
        <span data-slot="icon" className="inline-flex shrink-0 items-center">
            {icon}
        </span>
    );
}
