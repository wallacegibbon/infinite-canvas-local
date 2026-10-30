import { ChevronDown, ChevronLeft, ChevronRight } from "lucide-react";
import { useState, type ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * Tabs, Collapse and Pagination — the three pieces of chrome that carry their content in
 * antd's `items` arrays. Keeping that shape means the `items` literals at the call sites
 * did not move; only what renders them changed.
 */

/* ------------------------------------------------------------------ Tabs */

type TabItem = { key: string; label?: ReactNode; children?: ReactNode; icon?: ReactNode };

export function Tabs({ items, activeKey, defaultActiveKey, onChange, className }: { items: readonly TabItem[]; activeKey?: string; defaultActiveKey?: string; onChange?: (key: string) => void; className?: string }): ReactNode {
    const [internal, setInternal] = useState(defaultActiveKey ?? items[0]?.key);
    const current = activeKey ?? internal;
    const select = (key: string) => {
        if (activeKey === undefined) setInternal(key);
        onChange?.(key);
    };
    const active = items.find((item) => item.key === current);
    return (
        <div className={className}>
            <div role="tablist" className="flex items-center gap-1 border-b border-stone-200 dark:border-stone-800">
                {items.map((item) => {
                    const selected = item.key === current;
                    return (
                        <button
                            key={item.key}
                            type="button"
                            role="tab"
                            aria-selected={selected}
                            onClick={() => select(item.key)}
                            className={cn(
                                "-mb-px inline-flex items-center gap-1.5 border-b-2 px-3 py-2 text-sm whitespace-nowrap transition-colors",
                                selected ? "border-stone-900 font-medium text-stone-900 dark:border-stone-100 dark:text-stone-100" : "border-transparent text-stone-500 hover:text-stone-800 dark:text-stone-400 dark:hover:text-stone-200",
                            )}
                        >
                            {item.icon}
                            {item.label}
                        </button>
                    );
                })}
            </div>
            {active?.children !== undefined ? (
                <div role="tabpanel" className="pt-4">
                    {active.children}
                </div>
            ) : null}
        </div>
    );
}

/* -------------------------------------------------------------- Collapse */

type CollapseItem = { key: string; label?: ReactNode; children?: ReactNode; extra?: ReactNode };

export function Collapse({
    items,
    activeKey,
    defaultActiveKey = [],
    onChange,
    ghost = false,
    size = "middle",
    className,
}: {
    items: readonly CollapseItem[];
    activeKey?: string | string[];
    defaultActiveKey?: string | string[];
    onChange?: (keys: string[]) => void;
    ghost?: boolean;
    size?: "small" | "middle";
    className?: string;
}): ReactNode {
    const asArray = (value: string | string[] | undefined) => (value === undefined ? [] : Array.isArray(value) ? value : [value]);
    const [internal, setInternal] = useState<string[]>(asArray(defaultActiveKey));
    const openKeys = activeKey === undefined ? internal : asArray(activeKey);
    const toggle = (key: string) => {
        const next = openKeys.includes(key) ? openKeys.filter((current) => current !== key) : [...openKeys, key];
        if (activeKey === undefined) setInternal(next);
        onChange?.(next);
    };
    return (
        <div className={cn(!ghost && "overflow-hidden rounded-lg border border-stone-200 dark:border-stone-800", className)}>
            {items.map((item) => {
                const open = openKeys.includes(item.key);
                return (
                    <div key={item.key} className={cn(!ghost && "border-b border-stone-200 last:border-b-0 dark:border-stone-800")}>
                        <button
                            type="button"
                            aria-expanded={open}
                            onClick={() => toggle(item.key)}
                            className={cn(
                                "flex w-full items-center justify-between gap-2 text-left text-stone-700 transition-colors hover:bg-stone-50 dark:text-stone-200 dark:hover:bg-stone-800",
                                size === "small" ? "px-2 py-1.5 text-[13px]" : "px-3 py-2 text-sm",
                            )}
                        >
                            <span className="min-w-0">{item.label}</span>
                            <span className="flex shrink-0 items-center gap-2">
                                {item.extra}
                                <ChevronDown className={cn("size-4 text-stone-400 transition-transform", open && "rotate-180")} />
                            </span>
                        </button>
                        {open ? <div className={cn(size === "small" ? "px-2 pb-2" : "px-3 pb-3")}>{item.children}</div> : null}
                    </div>
                );
            })}
        </div>
    );
}

/* ------------------------------------------------------------ Pagination */

export function Pagination({
    current = 1,
    pageSize = 10,
    total = 0,
    onChange,
    pageSizeOptions = [10, 20, 50, 100],
    showSizeChanger = false,
    perPageLabel,
    size = "middle",
    className,
}: {
    current?: number;
    pageSize?: number;
    total?: number;
    onChange?: (page: number, pageSize: number) => void;
    pageSizeOptions?: number[];
    showSizeChanger?: boolean;
    /** Rendered after the size selector; antd took this from its own locale. */
    perPageLabel?: string;
    size?: "small" | "middle";
    className?: string;
}): ReactNode {
    const pages = Math.max(1, Math.ceil(total / pageSize));
    const go = (page: number) => {
        const next = Math.min(Math.max(1, page), pages);
        if (next !== current) onChange?.(next, pageSize);
    };
    const button =
        "inline-flex items-center justify-center rounded-md border border-transparent transition-colors disabled:cursor-not-allowed disabled:opacity-40 enabled:hover:bg-stone-100 enabled:dark:hover:bg-stone-800 text-stone-600 dark:text-stone-300";
    const dimension = size === "small" ? "size-6 text-[13px]" : "size-8 text-sm";
    return (
        <div className={cn("flex flex-wrap items-center gap-1", className)}>
            <button type="button" aria-label="Previous page" disabled={current <= 1} onClick={() => go(current - 1)} className={cn(button, dimension)}>
                <ChevronLeft className="size-4" />
            </button>
            <span className={cn("inline-flex min-w-8 items-center justify-center rounded-md border border-stone-300 font-medium text-stone-900 tabular-nums dark:border-stone-700 dark:text-stone-100", dimension)}>{current}</span>
            <span className="px-1 text-xs text-stone-400">/ {pages}</span>
            <button type="button" aria-label="Next page" disabled={current >= pages} onClick={() => go(current + 1)} className={cn(button, dimension)}>
                <ChevronRight className="size-4" />
            </button>
            {showSizeChanger ? (
                <select
                    value={pageSize}
                    onChange={(event) => onChange?.(1, Number(event.target.value))}
                    className="ml-2 h-8 rounded-md border border-stone-300 bg-white px-1.5 text-sm text-stone-700 outline-none dark:border-stone-700 dark:bg-stone-900 dark:text-stone-200"
                >
                    {pageSizeOptions.map((option) => (
                        <option key={option} value={option}>
                            {option}
                        </option>
                    ))}
                </select>
            ) : null}
            {showSizeChanger && perPageLabel ? <span className="text-xs text-stone-400">{perPageLabel}</span> : null}
        </div>
    );
}
