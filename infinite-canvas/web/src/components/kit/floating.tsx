import { DropdownMenu as RadixDropdownMenu, Popover as RadixPopover } from "radix-ui";
import { useRef, useState, type ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * Floating panels, replacing antd's `Popover` and `Dropdown`.
 *
 * `Popover` opens on hover, because antd's did and the reference bar relies on it while
 * moving along a row of thumbnails. `Dropdown` keeps antd's `menu={{ items }}` shape,
 * including `divider` entries and per-item `danger`/`disabled`, so the canvas menu's item
 * list did not have to be rewritten.
 *
 * Both portals carry `data-canvas-overlay`, so a panel opened over the canvas stops the
 * wheel and clicks from reaching the canvas underneath.
 */

/** antd wrote placements as one word: `top`, `topLeft`, `bottomRight`, … */
function splitPlacement(placement: string): { side: "top" | "right" | "bottom" | "left"; align: "start" | "center" | "end" } {
    const side = (["top", "right", "bottom", "left"] as const).find((candidate) => placement.startsWith(candidate)) ?? "bottom";
    const align = placement.endsWith("Left") ? "start" : placement.endsWith("Right") ? "end" : "center";
    return { side, align };
}

export function Popover({ content, placement = "top", mouseEnterDelay = 0.1, children, className }: { content?: ReactNode; placement?: string; mouseEnterDelay?: number; children?: ReactNode; className?: string }): ReactNode {
    const [open, setOpen] = useState(false);
    const timer = useRef<number | undefined>(undefined);
    const { side, align } = splitPlacement(placement);

    const show = () => {
        window.clearTimeout(timer.current);
        timer.current = window.setTimeout(() => setOpen(true), Math.round(mouseEnterDelay * 1000));
    };
    const hide = () => {
        window.clearTimeout(timer.current);
        setOpen(false);
    };

    return (
        <RadixPopover.Root open={open} onOpenChange={setOpen}>
            <RadixPopover.Trigger asChild onPointerEnter={show} onPointerLeave={hide}>
                {children}
            </RadixPopover.Trigger>
            <RadixPopover.Portal>
                <RadixPopover.Content
                    data-canvas-overlay
                    side={side}
                    align={align}
                    sideOffset={8}
                    onPointerEnter={() => window.clearTimeout(timer.current)}
                    onPointerLeave={hide}
                    className={cn("z-[1500] rounded-lg border border-stone-200 bg-white p-2 shadow-xl dark:border-stone-700 dark:bg-stone-900", className)}
                >
                    {content}
                </RadixPopover.Content>
            </RadixPopover.Portal>
        </RadixPopover.Root>
    );
}

export type DropdownItem = { key?: string; icon?: ReactNode; label?: ReactNode; onClick?: () => void; disabled?: boolean; danger?: boolean; type?: never } | { type: "divider"; key?: string };

export function Dropdown({ menu, className, children }: { menu?: { items?: DropdownItem[] }; trigger?: string[]; className?: string; children?: ReactNode }): ReactNode {
    return (
        <RadixDropdownMenu.Root>
            <RadixDropdownMenu.Trigger asChild>{children}</RadixDropdownMenu.Trigger>
            <RadixDropdownMenu.Portal>
                <RadixDropdownMenu.Content data-canvas-overlay align="start" sideOffset={6} className={cn("z-[1500] min-w-44 rounded-lg border border-stone-200 bg-white p-1 shadow-xl dark:border-stone-700 dark:bg-stone-900", className)}>
                    {(menu?.items ?? []).map((item, index) => {
                        if (item.type === "divider") return <RadixDropdownMenu.Separator key={item.key ?? `divider-${index}`} className="my-1 h-px bg-stone-200 dark:bg-stone-700" />;
                        return (
                            <RadixDropdownMenu.Item
                                key={item.key ?? `item-${index}`}
                                disabled={item.disabled}
                                onSelect={() => item.onClick?.()}
                                className={cn(
                                    "flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm outline-none select-none data-[disabled]:cursor-not-allowed data-[disabled]:opacity-40",
                                    item.danger ? "text-red-600 data-[highlighted]:bg-red-50 dark:text-red-400 dark:data-[highlighted]:bg-red-950/40" : "text-stone-700 data-[highlighted]:bg-stone-100 dark:text-stone-200 dark:data-[highlighted]:bg-stone-800",
                                )}
                            >
                                {item.icon ? <span className="flex shrink-0 items-center text-stone-400">{item.icon}</span> : null}
                                <span className="min-w-0 flex-1">{item.label}</span>
                            </RadixDropdownMenu.Item>
                        );
                    })}
                </RadixDropdownMenu.Content>
            </RadixDropdownMenu.Portal>
        </RadixDropdownMenu.Root>
    );
}
