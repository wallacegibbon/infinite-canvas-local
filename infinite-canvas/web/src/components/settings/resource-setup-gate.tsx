import { Button } from "@/components/kit/button";
import { Dialog } from "@/components/kit/dialog";
import { Download, X } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { ResourceCenterPanel } from "@/components/settings/resource-center-panel";
import { fetchResourceStatus, type ResourceStatus } from "@/services/api/resources";

const SKIP_KEY = "resource-setup-skipped";

export function ResourceSetupGate() {
    const { t } = useTranslation();
    const [open, setOpen] = useState(false);
    const [status, setStatus] = useState<ResourceStatus | null>(null);
    const [checked, setChecked] = useState(false);

    useEffect(() => {
        let cancelled = false;
        const run = async () => {
            try {
                const next = await fetchResourceStatus();
                if (cancelled) return;
                setStatus(next);
                // Decide on the missing-resource count, not the old recommendedMissing: that one relies on a
                // GPU assessment, only applies to autoRecommend packs, and measured as always 0, so this
                // dialog never showed up at all.
                if ((next.summary.missing || 0) > 0 && window.localStorage.getItem(SKIP_KEY) !== "1") setOpen(true);
            } catch {
                // Stay out of the way while the gateway or ComfyUI is not ready; Add workflows can still be opened by hand.
            } finally {
                if (!cancelled) setChecked(true);
            }
        };
        void run();
        return () => {
            cancelled = true;
        };
    }, []);

    if (!checked || !status || status.summary.missing <= 0) return null;

    const skip = () => {
        window.localStorage.setItem(SKIP_KEY, "1");
        setOpen(false);
    };

    return (
        <Dialog
            title={
                <div className="flex items-center gap-2">
                    <Download className="size-5" />
                    <div>
                        <div className="text-base font-semibold">{t("resources.setup.title")}</div>
                        <div className="mt-0.5 text-xs font-normal text-stone-500 dark:text-stone-400">{t("resources.setup.description")}</div>
                    </div>
                </div>
            }
            open={open}
            size="panel"
            closable={false}
            onClose={skip}
            bodyStyle={{ maxHeight: "72vh" }}
            bodyClassName="pr-3"
        >
            <ResourceCenterPanel
                compact
                footer={
                    <div className="flex flex-wrap items-center justify-between gap-2 border-t border-stone-200 pt-3 dark:border-stone-800">
                        <span className="text-[11px] text-stone-400 dark:text-stone-500">{t("resources.setup.skipHint")}</span>
                        <Button onClick={skip}>
                            <X className="size-4" />
                            {t("resources.setup.skipButton")}
                        </Button>
                    </div>
                }
            />
        </Dialog>
    );
}
