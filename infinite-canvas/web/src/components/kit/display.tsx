import { Inbox, LoaderCircle } from "lucide-react";
import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * The two states every list in this app needs, replacing antd's `Empty` and `Spin`.
 *
 * `Empty` accepts antd's `image` prop and ignores it: every call site passed
 * `Empty.PRESENTED_IMAGE_SIMPLE`, so there is one illustration, not a choice. Accepting the
 * prop keeps the call sites unchanged; there is a comment there rather than a lie here.
 */
export function Empty({ description, className }: { description?: ReactNode; image?: ReactNode; className?: string }): ReactNode {
    return (
        <div className={cn("flex flex-col items-center justify-center gap-2 text-center", className)}>
            <Inbox className="size-7 text-stone-300 dark:text-stone-600" aria-hidden="true" />
            {description ? <div className="text-sm text-stone-400 dark:text-stone-500">{description}</div> : null}
        </div>
    );
}

export function Spin({ size = "middle", className }: { size?: "small" | "middle" | "large"; className?: string }): ReactNode {
    const dimension = size === "small" ? "size-4" : size === "large" ? "size-8" : "size-5";
    return (
        <span role="status" aria-live="polite" className={cn("inline-flex items-center justify-center text-stone-400", className)}>
            <LoaderCircle className={cn(dimension, "animate-spin")} aria-hidden="true" />
        </span>
    );
}

/** Horizontal or vertical stack with antd's `size` gap, replacing antd's `Space`. */
export function Space({
    size = "small",
    direction = "horizontal",
    wrap = false,
    className,
    children,
}: {
    size?: number | [number, number] | "small" | "middle" | "large";
    direction?: "horizontal" | "vertical";
    wrap?: boolean;
    className?: string;
    children?: ReactNode;
}): ReactNode {
    const preset = typeof size === "number" ? undefined : Array.isArray(size) ? undefined : size;
    const gapClass = preset === "large" ? "gap-4" : preset === "middle" ? "gap-3" : preset === "small" ? "gap-2" : undefined;
    const gapStyle = typeof size === "number" ? { gap: size } : Array.isArray(size) ? { rowGap: size[0], columnGap: size[1] } : undefined;
    return (
        <div className={cn("flex", direction === "vertical" ? "flex-col items-start" : "flex-row items-center", wrap && "flex-wrap", gapClass, className)} style={gapStyle}>
            {children}
        </div>
    );
}

/**
 * antd's `Space.Compact`: children sit flush, with the inner radii squared off and the
 * borders overlapped by 1px, so a field and its button read as one control.
 */
Space.Compact = function SpaceCompact({ className, children }: { className?: string; children?: ReactNode }): ReactNode {
    return (
        <div
            className={cn(
                "inline-flex items-center [&>*:not(:first-child)]:-ml-px [&>*:not(:first-child)]:rounded-l-none [&>*:not(:last-child)]:rounded-r-none [&>*:focus-visible]:relative [&>*:focus-visible]:z-10",
                className,
            )}
        >
            {children}
        </div>
    );
};
