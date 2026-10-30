import { AlertDialog } from "radix-ui";
import { LoaderCircle } from "lucide-react";
import { useState } from "react";
import { create } from "zustand";

/**
 * Imperative confirmation dialog — the replacement for antd's `modal.confirm`.
 *
 * Why imperative: the three places that need it call it from an event handler
 * deep inside a component (`confirmStopGeneration`, `deleteCloudApi`,
 * `handleDelete`), and threading an `open` boolean up and down just to ask
 * "really?" is more code than the question is worth. Everything this opens is a
 * yes/no with an optional async action.
 *
 * `onOk` may return a promise: the dialog then keeps itself open with the OK
 * button disabled until it settles, and stays open if it rejects — the same
 * contract antd had. Callers that need the result must handle their own errors.
 *
 * accessibility (focus trap, ESC, aria wiring) comes from radix's AlertDialog;
 * the styling is ours, so nothing here depends on antd's CSS.
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

const cancelClass =
    "inline-flex h-8 items-center rounded-md border border-stone-300 bg-white px-3 text-sm text-stone-700 transition-colors hover:border-stone-400 hover:bg-stone-50 disabled:cursor-not-allowed disabled:opacity-60 dark:border-stone-700 dark:bg-stone-900 dark:text-stone-200 dark:hover:border-stone-600 dark:hover:bg-stone-800";

const confirmClass = (danger: boolean) =>
    `inline-flex h-8 items-center gap-1.5 rounded-md px-3 text-sm font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-60 ${
        danger ? "bg-red-600 text-white hover:bg-red-500" : "bg-stone-900 text-white hover:bg-stone-800 dark:bg-stone-100 dark:text-stone-900 dark:hover:bg-white"
    }`;

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
                <AlertDialog.Overlay className="fixed inset-0 z-[1400] bg-black/45" />
                <AlertDialog.Content
                    // `content` is optional here, and radix warns when an alert dialog has no
                    // description; an explicit undefined opts out of that check.
                    aria-describedby={undefined}
                    className="fixed left-1/2 top-1/2 z-[1401] w-[min(92vw,420px)] -translate-x-1/2 -translate-y-1/2 rounded-xl border border-stone-200 bg-white p-5 shadow-2xl dark:border-stone-800 dark:bg-stone-900"
                >
                    <AlertDialog.Title className="text-base font-semibold text-stone-900 dark:text-stone-100">{request?.title}</AlertDialog.Title>
                    {request?.content ? <p className="mt-2 text-sm leading-6 text-stone-500 dark:text-stone-400">{request.content}</p> : null}
                    <div className="mt-5 flex justify-end gap-2">
                        <button type="button" className={cancelClass} disabled={pending} onClick={dismiss}>
                            {request?.cancelText}
                        </button>
                        <button type="button" className={confirmClass(Boolean(request?.danger))} disabled={pending} onClick={handleOk}>
                            {pending ? <LoaderCircle className="size-3.5 animate-spin" /> : null}
                            {request?.okText}
                        </button>
                    </div>
                </AlertDialog.Content>
            </AlertDialog.Portal>
        </AlertDialog.Root>
    );
}
