import type { CSSProperties, ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * A bordered surface, replacing antd's `Card`.
 *
 * `cover` renders above the body and `styles.body` still reaches the body padding, because
 * the asset card uses a full-bleed cover and zero body padding to make the whole tile a
 * click target.
 */
export function Card({
    title,
    cover,
    extra,
    size = "middle",
    hoverable = false,
    className,
    bodyClassName,
    styles,
    children,
}: {
    title?: ReactNode;
    cover?: ReactNode;
    extra?: ReactNode;
    size?: "small" | "middle";
    hoverable?: boolean;
    className?: string;
    bodyClassName?: string;
    styles?: { body?: CSSProperties };
    children?: ReactNode;
}): ReactNode {
    return (
        <div className={cn("overflow-hidden rounded-lg border border-stone-200 bg-white dark:border-stone-800 dark:bg-stone-900", hoverable && "transition-shadow hover:shadow-md", className)}>
            {cover}
            {title || extra ? (
                <div className={cn("flex items-center justify-between gap-2 border-b border-stone-200 dark:border-stone-800", size === "small" ? "px-3 py-2 text-sm" : "px-4 py-3")}>
                    <div className="min-w-0 font-medium text-stone-900 dark:text-stone-100">{title}</div>
                    {extra ? <div className="shrink-0">{extra}</div> : null}
                </div>
            ) : null}
            <div className={cn(size === "small" ? "p-3" : "p-4", bodyClassName)} style={styles?.body}>
                {children}
            </div>
        </div>
    );
}
