import { saveAs } from "file-saver";
import { AlertTriangle, Download, RotateCcw } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/kit/button";
import { useAssetStore } from "@/stores/use-asset-store";
import { useCanvasStore } from "@/stores/canvas/use-canvas-store";
import { useStorageRecoveryStore } from "@/stores/use-storage-recovery-store";

/**
 * Shown when a persisted store could not be read back.
 *
 * Deliberately not a toast: a toast disappears, and this is the one message in the app that must
 * survive until the user acts on it. It also has to be visible over the canvas itself, not only in
 * the canvas list, because `/` resolves straight to a canvas and most sessions never open the list.
 *
 * The three actions are ordered by what protects the user. Export first, because it is the only one
 * that changes nothing and loses nothing; retry second, because it may resolve a transient read
 * failure; discard last and styled as a link, because even though it cannot destroy the data (a
 * copy is already on disk) it does let writes resume over the original key.
 */
export function StorageRecoveryBanner() {
    const { t } = useTranslation();
    const failures = useStorageRecoveryStore((state) => state.failures);
    const dismiss = useStorageRecoveryStore((state) => state.dismiss);

    if (!failures.length) return null;

    const discard = () => {
        dismiss();
        // Nudge both stores so the moment writes are allowed again the cleared state replaces the
        // unreadable blob. Without this, "start over" only stops showing the banner: nothing has
        // been written yet, so a reload reads the same broken value and shows it again.
        useCanvasStore.setState({});
        useAssetStore.setState({});
    };

    const exportRaw = () => {
        const stamp = new Date().toISOString().replace(/[:.]/g, "-");
        for (const failure of failures) {
            saveAs(new Blob([failure.raw], { type: "application/json" }), `${failure.store}-store-unreadable-${stamp}.json`);
        }
    };

    return (
        <div className="pointer-events-none fixed inset-x-0 top-16 z-[2999] flex justify-center px-4">
            <div className="pointer-events-auto w-full max-w-2xl rounded-xl border border-amber-300 bg-white/97 p-4 shadow-xl backdrop-blur dark:border-amber-900/70 dark:bg-stone-900/97">
                <div className="flex items-start gap-3">
                    <AlertTriangle className="mt-0.5 size-5 shrink-0 text-amber-600 dark:text-amber-500" />
                    <div className="min-w-0 flex-1">
                        <div className="text-sm font-semibold">{t("canvas.recovery.title")}</div>
                        <p className="mt-1 text-xs leading-relaxed text-stone-600 dark:text-stone-300">{t("canvas.recovery.description")}</p>
                        <div className="mt-3 flex flex-wrap items-center gap-2">
                            <Button type="primary" icon={<Download className="size-4" />} onClick={exportRaw}>
                                {t("canvas.recovery.export")}
                            </Button>
                            <Button icon={<RotateCcw className="size-4" />} onClick={() => window.location.reload()}>
                                {t("canvas.recovery.retry")}
                            </Button>
                            <button type="button" className="ml-1 text-xs text-stone-500 underline underline-offset-2 hover:text-stone-700 dark:text-stone-400 dark:hover:text-stone-200" onClick={discard}>
                                {t("canvas.recovery.discard")}
                            </button>
                        </div>
                        <p className="mt-2 text-[11px] leading-relaxed text-stone-500 dark:text-stone-400">{t("canvas.recovery.discardHint")}</p>
                    </div>
                </div>
            </div>
        </div>
    );
}
