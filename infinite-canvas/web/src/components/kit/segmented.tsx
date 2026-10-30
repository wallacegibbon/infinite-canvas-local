import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * Segmented control, replacing antd's `Segmented`.
 *
 * Items carry `data-slot="segmented-item"` and `data-active`, because three places need to
 * reach them from CSS: the canvas config panel recolours unselected labels per card skin,
 * the image settings popover rounds the control into a pill, and the canvas toolbar makes
 * the items fill the row. Those used to select antd's `.ant-segmented-*` internals.
 */
export type SegmentedOption<T extends string | number> = { value: T; label?: ReactNode; disabled?: boolean };

export function Segmented<T extends string | number>({
    value,
    onChange,
    options,
    size = "middle",
    block = false,
    className,
}: {
    value: T;
    onChange: (value: T) => void;
    /** Objects, or bare values whose label is the value itself — antd accepted both. */
    options: ReadonlyArray<T | SegmentedOption<T>>;
    size?: "small" | "middle" | "large";
    /** Stretch to the container and give every item an equal share. */
    block?: boolean;
    className?: string;
}): ReactNode {
    const height = size === "small" ? "h-6 text-[13px]" : size === "large" ? "h-9 text-base" : "h-7 text-sm";
    return (
        <div
            role="tablist"
            data-slot="segmented"
            className={cn("inline-flex items-center gap-0.5 rounded-md bg-stone-100 p-0.5 dark:bg-stone-800", block && "flex w-full", className)}
        >
            {options.map((raw) => {
                const option: SegmentedOption<T> = typeof raw === "object" && raw !== null ? raw : { value: raw };
                const active = option.value === value;
                return (
                    <button
                        key={option.value}
                        type="button"
                        role="tab"
                        aria-selected={active}
                        data-slot="segmented-item"
                        data-active={active}
                        disabled={option.disabled}
                        onClick={() => {
                            if (!active) onChange(option.value);
                        }}
                        className={cn(
                            "inline-flex items-center justify-center gap-1.5 rounded px-2.5 whitespace-nowrap transition-colors outline-none focus-visible:ring-2 focus-visible:ring-stone-400/40 disabled:cursor-not-allowed disabled:opacity-50",
                            height,
                            block && "flex-1",
                            active ? "bg-white text-stone-900 shadow-sm dark:bg-stone-700 dark:text-stone-50" : "text-stone-600 hover:text-stone-900 dark:text-stone-300 dark:hover:text-stone-100",
                        )}
                    >
                        {option.label ?? String(option.value)}
                    </button>
                );
            })}
        </div>
    );
}
