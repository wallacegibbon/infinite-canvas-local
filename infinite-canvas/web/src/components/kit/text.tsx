import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * Text styles, replacing antd's `Typography`.
 *
 * Exported as a `Typography` object with `.Text` and `.Title` so the call sites — which
 * write `Typography.Text` — do not change. `type` picks a semantic colour, `strong` and
 * `ellipsis` behave as antd's did.
 */
type TextType = "secondary" | "success" | "warning" | "danger";
type Ellipsis = boolean | { rows?: number };

/** antd's `ellipsis`: `true` truncates one line, `{ rows }` clamps to that many. */
function ellipsisStyle(ellipsis: Ellipsis | undefined, className?: string) {
    if (!ellipsis) return { className, style: undefined };
    if (ellipsis === true) return { className: cn("block min-w-0 truncate", className), style: undefined };
    return {
        className: cn("block overflow-hidden", className),
        style: { display: "-webkit-box", WebkitLineClamp: ellipsis.rows ?? 1, WebkitBoxOrient: "vertical" as const },
    };
}

const TYPES: Record<TextType, string> = {
    secondary: "text-stone-500 dark:text-stone-400",
    success: "text-emerald-600 dark:text-emerald-400",
    warning: "text-amber-600 dark:text-amber-400",
    danger: "text-red-600 dark:text-red-400",
};

function Text({ type, strong, ellipsis, className, children }: { type?: TextType; strong?: boolean; ellipsis?: Ellipsis; className?: string; children?: ReactNode }): ReactNode {
    const clamp = ellipsisStyle(ellipsis, className);
    return (
        <span className={cn(type ? TYPES[type] : undefined, strong && "font-semibold", clamp.className)} style={clamp.style}>
            {children}
        </span>
    );
}

const TITLE_SIZES: Record<number, string> = {
    1: "text-3xl font-semibold",
    2: "text-2xl font-semibold",
    3: "text-xl font-semibold",
    4: "text-lg font-semibold",
    5: "text-base font-semibold",
};

function Title({ level = 1, className, children }: { level?: 1 | 2 | 3 | 4 | 5; className?: string; children?: ReactNode }): ReactNode {
    const Tag = `h${level}` as "h1";
    return <Tag className={cn("text-stone-900 dark:text-stone-100", TITLE_SIZES[level], className)}>{children}</Tag>;
}

function Paragraph({ type, ellipsis, className, children }: { type?: TextType; ellipsis?: Ellipsis; className?: string; children?: ReactNode }): ReactNode {
    const clamp = ellipsisStyle(ellipsis, cn("m-0", className));
    return (
        <p className={cn(type ? TYPES[type] : undefined, clamp.className)} style={clamp.style}>
            {children}
        </p>
    );
}

export const Typography = { Text, Title, Paragraph };
