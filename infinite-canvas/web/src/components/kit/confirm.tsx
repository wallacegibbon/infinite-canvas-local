import { AlertDialog } from "radix-ui";
import { LoaderCircle } from "lucide-react";
import { useState } from "react";
import { create } from "zustand";

import { Button } from "@/components/kit/button";

/**
 * Imperative confirmation dialog — the replacement for antd's `modal.confirm`.
 *
 * Why imperative: the call sites fire from an event handler deep inside a component, and
 * threading an `open` boolean up and down just to ask "really?" is more code than the
 * question is worth. Everything this opens is a yes/no with an optional async action.
 *
 * `onOk` may return a promise: the dialog then keeps itself open with the OK button disabled
 * until it settles, and stays open if it rejects — the same contract antd had. Callers that
 * need the result must handle their own errors.
 *
 * Accessibility (focus trap, ESC, aria wiring) comes from radix's AlertDialog; the styling is
 * ours. Like `kit/dialog`, the portal carries `data-canvas-overlay` so a confirmation opened
 * from the canvas does not let clicks pan the canvas behind it.
 */
export type ConfirmOptions = {
    title: string;
    content?: string;
    okText?: string;
    cancelText?: string;
    /** Renders the OK button in red. Used for anything destructive. */
    danger?: boolean;
    onOk?: () => void | Promise<void>;
};

type ConfirmStore = {
    request: ConfirmOptions | null;
    show: (request: ConfirmOptions) => void;
    dismiss: () => void;
};

const useConfirmStore = create<ConfirmStore>()((set) => ({
    request: null,
    show: (request) => set({ request }),
    dismiss: () => set({ request: null }),
}));

/** Opens the confirmation dialog. Safe to call from any event handler. */
export function confirm(request: ConfirmOptions) {
    useConfirmStore.getState().show(request);
}

/** Mounted once, in `AppProviders`. */
export function ConfirmHost() {
    const request = useConfirmStore((state) => state.request);
    const dismiss = useConfirmStore((state) => state.dismiss);
    const [pending, setPending] = useState(false);

    const handleOk = async () => {
        if (!request || pending) return;
        const result = request.onOk?.();
        if (result instanceof Promise) {
            setPending(true);
            try {
                await result;
            } catch {
                // Keep the dialog open so the error is not swallowed silently.
                setPending(false);
                return;
            }
            setPending(false);
        }
        dismiss();
    };

    return (
        <AlertDialog.Root
            open={Boolean(request)}
            onOpenChange={(next) => {
                if (!next && !pending) dismiss();
            }}
        >
            <AlertDialog.Portal>
                <AlertDialog.Overlay data-canvas-overlay className="fixed inset-0 z-[1400] bg-black/45" />
                <AlertDialog.Content
                    data-canvas-overlay
                    data-canvas-dialog-content
                    className="fixed left-1/2 top-1/2 z-[1401] w-[min(92vw,420px)] -translate-x-1/2 -translate-y-1/2 rounded-xl border border-stone-200 bg-white p-5 shadow-2xl dark:border-stone-800 dark:bg-stone-900"
                >
                    <AlertDialog.Title className="text-base font-semibold text-stone-900 dark:text-stone-100">{request?.title}</AlertDialog.Title>
                    {/* radix requires a description on an alert dialog and wires `aria-describedby`
                        from this child, so it is always rendered; with no copy it is hidden visually
                        and the title carries the meaning. */}
                    <AlertDialog.Description className={request?.content ? "mt-2 text-sm leading-6 text-stone-500 dark:text-stone-400" : "sr-only"}>{request?.content || request?.title}</AlertDialog.Description>
                    <div className="mt-5 flex justify-end gap-2">
                        <Button disabled={pending} onClick={dismiss}>
                            {request?.cancelText}
                        </Button>
                        <Button variant={request?.danger ? "danger" : "primary"} disabled={pending} onClick={handleOk}>
                            {pending ? <LoaderCircle className="size-3.5 animate-spin" /> : null}
                            {request?.okText}
                        </Button>
                    </div>
                </AlertDialog.Content>
            </AlertDialog.Portal>
        </AlertDialog.Root>
    );
}
