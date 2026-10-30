import { toast as sonnerToast } from "sonner";

/**
 * Application toast — the only way UI code should surface a transient message.
 *
 * The shape deliberately mirrors antd's `message.success/error/warning/info/loading`
 * so that swapping the implementation (antd → sonner → whatever comes next) never
 * touches a call site: this module owns the library choice, call sites own the copy.
 *
 * `loading()` is sticky and hands back its own dismiss handler, which is the antd
 * idiom it replaces (`const hide = message.loading(text, 0)` … `hide()`). Do not
 * "simplify" it into a fire-and-forget call — the export/import flows rely on the
 * toast surviving until the work finishes, however long that takes.
 *
 * The `<Toaster />` that renders these is mounted in
 * `components/layout/app-providers.tsx` alongside the other app-level providers.
 */
export const toast = {
    success: (text: string) => sonnerToast.success(text),
    error: (text: string) => sonnerToast.error(text),
    warning: (text: string) => sonnerToast.warning(text),
    info: (text: string) => sonnerToast.info(text),
    loading(text: string): () => void {
        const id = sonnerToast.loading(text, { duration: Infinity });
        return () => sonnerToast.dismiss(id);
    },
};
