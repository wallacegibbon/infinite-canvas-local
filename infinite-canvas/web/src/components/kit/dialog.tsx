import { X } from "lucide-react";
import { Dialog as RadixDialog } from "radix-ui";
import type { CSSProperties, ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * The app's dialog, replacing antd's `Modal`.
 *
 * The props mirror what the call sites actually need — `open`/`onClose`/`title`/`footer`/`size`
 * — rather than antd's full surface, so a call site cannot quietly depend on a feature this does
 * not have. It is always centred, because all but a handful asked for that and the rest are
 * content panels where centring is not a regression.
 *
 * **`size` is not a hint.** `panel` is one fixed width *and* one fixed height, shared by every
 * floating panel in the app, because a window that resizes to its contents breaks the one thing
 * a window is for: the user's sense of where it is and how big it is. Switch between the canvas
 * list, the asset library and settings and the frame does not move; switch tabs inside settings
 * and the frame does not move either. Short content leaves space at the bottom, which is the
 * price and is accepted. Only two things are exempt: a confirmation, where a 1000px box around
 * one sentence would be its own kind of wrong, and an image preview, whose size *is* the image.
 *
 * Behaviour worth knowing:
 *   - The body scrolls; the dialog is capped at 90dvh. `bodyStyle`/`bodyClassName` still
 *     reach the body for the handful of panels that tune padding or max height.
 *   - Closing unmounts the children. That is antd's `destroyOnHidden`, which 13 of the call
 *     sites asked for explicitly; the ones that did not were keeping transient form state
 *     alive across opens, which is not behaviour worth preserving.
 *   - It fades and scales in, which is what antd's Modal did and what this lost when antd was
 *     removed. The animation is **enter-only on purpose**: an exit animation would make radix
 *     hold the subtree mounted for its duration, and instant unmount on close is the contract
 *     the call sites rely on.
 *   - `title` may be omitted for content panels that draw their own heading; `label` then
 *     supplies the accessible name, because an unlabelled dialog is unusable with a screen
 *     reader.
 *
 * Canvas coupling: this portal carries `data-canvas-overlay` and `data-canvas-dialog-content`
 * so the canvas overlay guards (`lib/canvas-overlay.ts`) treat clicks, wheel and the mention
 * menu correctly. Without them a dialog opened over the canvas would pan the canvas behind it.
 * Those attributes are the reason antd's `.ant-modal*` selectors could be dropped at all.
 */
type DialogProps = {
    open: boolean;
    onClose: () => void;
    /** Rendered in the header. Omit for panels that draw their own heading; see `label`. */
    title?: ReactNode;
    /** Accessible name when `title` is omitted. */
    label?: string;
    children?: ReactNode;
    /** Rendered as a right-aligned action row. Omit entirely for no footer. */
    footer?: ReactNode;
    /** See the note above: `panel` is fixed, `form` fits its contents, `auto` fits its media. */
    size: "panel" | "form" | "auto";
    className?: string;
    bodyClassName?: string;
    bodyStyle?: CSSProperties;
    /** Hide the top-right close button, for panels that must be answered. */
    closable?: boolean;
    /** Whether clicking the backdrop closes. Defaults to true. */
    maskClosable?: boolean;
};

const SIZE_CLASS: Record<DialogProps["size"], string> = {
    // Fixed in both directions on purpose — see the note on `size` above.
    panel: "h-[min(760px,88dvh)] w-[1000px]",
    form: "max-h-[90dvh] w-[520px]",
    auto: "max-h-[90dvh] w-auto",
};

export function Dialog({ open, onClose, title, label, children, footer, size, className, bodyClassName, bodyStyle, closable = true, maskClosable = true }: DialogProps) {
    return (
        <RadixDialog.Root
            open={open}
            onOpenChange={(next) => {
                if (!next) onClose();
            }}
        >
            <RadixDialog.Portal>
                <RadixDialog.Overlay data-canvas-overlay className="fixed inset-0 z-[1300] bg-black/45 data-[state=open]:animate-in data-[state=open]:fade-in-0" />
                <RadixDialog.Content
                    data-canvas-overlay
                    data-canvas-dialog-content
                    // No description is rendered, and radix otherwise warns on every open.
                    aria-describedby={undefined}
                    // antd's `maskClosable={false}`: the backdrop stops closing the dialog, while
                    // Escape still does, which is the contract the call sites expect.
                    onPointerDownOutside={(event) => {
                        if (!maskClosable) event.preventDefault();
                    }}
                    className={cn(
                        "fixed left-1/2 top-1/2 z-[1301] flex max-w-[calc(100vw-32px)] -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-xl border border-stone-200 bg-white shadow-2xl outline-none dark:border-stone-800 dark:bg-stone-900 data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95",
                        SIZE_CLASS[size],
                        className,
                    )}
                >
                    {title ? (
                        <header className="flex shrink-0 items-start justify-between gap-4 px-6 pt-5 pb-3">
                            <RadixDialog.Title className="min-w-0 text-base font-semibold text-stone-900 dark:text-stone-100">{title}</RadixDialog.Title>
                            {closable ? <CloseButton onClick={onClose} /> : null}
                        </header>
                    ) : (
                        <>
                            <RadixDialog.Title className="sr-only">{label}</RadixDialog.Title>
                            {closable ? (
                                <div className="absolute top-3 right-3 z-10">
                                    <CloseButton onClick={onClose} />
                                </div>
                            ) : null}
                        </>
                    )}
                    <div className={cn("min-h-0 flex-1 overflow-y-auto px-6 pb-6", bodyClassName)} style={bodyStyle}>
                        {children}
                    </div>
                    {footer ? <footer className="flex shrink-0 justify-end gap-2 border-t border-stone-200 px-6 py-3 dark:border-stone-800">{footer}</footer> : null}
                </RadixDialog.Content>
            </RadixDialog.Portal>
        </RadixDialog.Root>
    );
}

function CloseButton({ onClick }: { onClick: () => void }) {
    return (
        <button
            type="button"
            onClick={onClick}
            aria-label="Close"
            className="-mt-0.5 -mr-1 inline-flex size-7 shrink-0 items-center justify-center rounded-md text-stone-400 transition-colors hover:bg-stone-100 hover:text-stone-700 dark:hover:bg-stone-800 dark:hover:text-stone-200"
        >
            <X className="size-4" />
        </button>
    );
}
