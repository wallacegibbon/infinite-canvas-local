import { X } from "lucide-react";
import { Dialog as RadixDialog } from "radix-ui";
import type { CSSProperties, ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * A panel that slides in from one edge, replacing antd's `Drawer`.
 *
 * It is `kit/dialog` rotated to a side: same header / scrolling body / optional footer, same
 * "closing unmounts the children" behaviour, same `data-canvas-overlay` marker on the portal so
 * the canvas overlay guards (`lib/canvas-overlay.ts`) see it as a floating layer.
 *
 * antd's `width` and `size` both expressed a pixel width — `large` was 736 — so both are
 * accepted here and resolved to a number. `placement` covers the two edges the app used, and
 * `extra` is the header-right action slot antd had, which the channel editor filled with its
 * save/cancel pair.
 */
type Placement = "left" | "right";

/** antd's `size` values, in pixels. */
const SIZES = { default: 378, large: 736 } as const;

type DrawerProps = {
    open: boolean;
    onClose: () => void;
    title?: ReactNode;
    children?: ReactNode;
    /** Header-right actions, before the close button. */
    extra?: ReactNode;
    footer?: ReactNode;
    placement?: Placement;
    width?: number;
    size?: keyof typeof SIZES;
    className?: string;
    bodyClassName?: string;
    bodyStyle?: CSSProperties;
    closable?: boolean;
    maskClosable?: boolean;
};

export function Drawer({ open, onClose, title, children, extra, footer, placement = "right", width, size = "default", className, bodyClassName, bodyStyle, closable = true, maskClosable = true }: DrawerProps): ReactNode {
    const left = placement === "left";
    return (
        <RadixDialog.Root
            open={open}
            onOpenChange={(next) => {
                if (!next) onClose();
            }}
        >
            <RadixDialog.Portal>
                <RadixDialog.Overlay data-canvas-overlay className="fixed inset-0 z-[1350] bg-black/45 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:animate-in data-[state=open]:fade-in-0" />
                <RadixDialog.Content
                    data-canvas-overlay
                    aria-describedby={undefined}
                    onPointerDownOutside={(event) => {
                        if (!maskClosable) event.preventDefault();
                    }}
                    style={{ width: width ?? SIZES[size] }}
                    className={cn(
                        "fixed inset-y-0 z-[1351] flex max-w-[92vw] flex-col overflow-hidden bg-white shadow-2xl outline-none dark:bg-stone-900",
                        left
                            ? "left-0 border-r border-stone-200 data-[state=closed]:animate-out data-[state=closed]:slide-out-to-left data-[state=open]:animate-in data-[state=open]:slide-in-from-left dark:border-stone-800"
                            : "right-0 border-l border-stone-200 data-[state=closed]:animate-out data-[state=closed]:slide-out-to-right data-[state=open]:animate-in data-[state=open]:slide-in-from-right dark:border-stone-800",
                        className,
                    )}
                >
                    {title || extra || closable ? (
                        <header className="flex shrink-0 items-center justify-between gap-4 border-b border-stone-200 px-5 py-3 dark:border-stone-800">
                            <RadixDialog.Title className="min-w-0 truncate text-base font-semibold text-stone-900 dark:text-stone-100">{title}</RadixDialog.Title>
                            <div className="flex shrink-0 items-center gap-2">
                                {extra}
                                {closable ? <CloseButton onClick={onClose} /> : null}
                            </div>
                        </header>
                    ) : (
                        <RadixDialog.Title className="sr-only">{typeof title === "string" ? title : ""}</RadixDialog.Title>
                    )}
                    <div className={cn("min-h-0 flex-1 overflow-y-auto px-5 py-4", bodyClassName)} style={bodyStyle}>
                        {children}
                    </div>
                    {footer ? <footer className="flex shrink-0 justify-end gap-2 border-t border-stone-200 px-5 py-3 dark:border-stone-800">{footer}</footer> : null}
                </RadixDialog.Content>
            </RadixDialog.Portal>
        </RadixDialog.Root>
    );
}

function CloseButton({ onClick }: { onClick: () => void }): ReactNode {
    return (
        <button
            type="button"
            onClick={onClick}
            aria-label="Close"
            className="inline-flex size-7 shrink-0 items-center justify-center rounded-md text-stone-400 transition-colors hover:bg-stone-100 hover:text-stone-700 dark:hover:bg-stone-800 dark:hover:text-stone-200"
        >
            <X className="size-4" />
        </button>
    );
}
