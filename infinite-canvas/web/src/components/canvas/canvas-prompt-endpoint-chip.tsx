import { useTranslation } from "react-i18next";
import { ScanText } from "lucide-react";

import { Button } from "@/components/kit/button";
import type { AiConfig } from "@/stores/use-config-store";

/**
 * The model that answers text: the prompt endpoint's.
 *
 * There is nothing to pick. No workflow has a text capability, so a model picker here would list
 * nothing and mean nothing — what a text request needs is the endpoint in the settings, and that is
 * what this shows and opens. It is the same chip in both places a text model used to be offered.
 */
export function CanvasPromptEndpointChip({ config, className, onClick }: { config: AiConfig; className?: string; onClick: () => void }) {
    const { t } = useTranslation();
    const model = (config.promptModel || "").trim();
    return (
        <Button type="text" className={className} icon={<ScanText className="size-3.5 shrink-0" />} title={t("canvas.promptEndpoint.title")} onClick={onClick}>
            <span className="truncate">{model || t("canvas.promptEndpoint.missing")}</span>
        </Button>
    );
}
