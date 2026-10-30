import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * Small labels and filter chips, replacing antd's `Tag`.
 *
 * antd exposed `Tag.CheckableTag` as a static; here it is a named export, so a call site
 * says `<CheckableTag>` and the import makes it obvious which one is used. The checked
 * state is also written as an `is-active` class, because `globals.css` styles the prompt
 * filter chips through that class rather than through antd's generated one.
 */
type TagColor = "default" | "blue" | "green" | "orange" | "gold";

const COLORS: Record<TagColor, string> = {
    default: "border-stone-200 bg-stone-100 text-stone-700 dark:border-stone-700 dark:bg-stone-800 dark:text-stone-300",
    blue: "border-blue-200 bg-blue-50 text-blue-700 dark:border-blue-500/30 dark:bg-blue-500/15 dark:text-blue-300",
    green: "border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/15 dark:text-emerald-300",
    orange: "border-orange-200 bg-orange-50 text-orange-700 dark:border-orange-500/30 dark:bg-orange-500/15 dark:text-orange-300",
    gold: "border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-500/30 dark:bg-amber-500/15 dark:text-amber-300",
};

const BASE = "inline-flex items-center gap-1 rounded border px-2 py-0.5 text-xs leading-tight whitespace-nowrap";

export function Tag({ color = "default", className, children }: { color?: TagColor; className?: string; children?: ReactNode }): ReactNode {
    return <span className={cn(BASE, COLORS[color] ?? COLORS.default, className)}>{children}</span>;
}

/** A chip that toggles. `is-active` mirrors `checked` for the stylesheet. */
export function CheckableTag({ checked, onChange, className, children }: { checked: boolean; onChange?: (checked: boolean) => void; className?: string; children?: ReactNode }): ReactNode {
    return (
        <button type="button" aria-pressed={checked} onClick={() => onChange?.(!checked)} className={cn("cursor-pointer transition-colors", className, checked && "is-active")}>
            {children}
        </button>
    );
}
