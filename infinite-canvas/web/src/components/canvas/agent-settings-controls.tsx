import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ScanText } from "lucide-react";

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/kit/select-primitive";
import { requestAgentStream } from "@/services/api/agent";
import { useConfigStore, useEffectiveConfig } from "@/stores/use-config-store";

/**
 * The agent's two settings, displayed and changed.
 *
 * There is nothing to fetch: the agent announces its models, its active model and its reasoning level
 * in every stream's first events, so both controls render whatever it last said. Changing one sends
 * the agent's own command through the same route — `model_set` with an id, `reasoning_level_set` with
 * 0/1/2 — and the answer updates the display.
 */
export function AgentSettingsControls({ className }: { className?: string }) {
    const { t } = useTranslation();
    const config = useEffectiveConfig();
    const agent = useConfigStore((state) => state.agent);
    const [busy, setBusy] = useState(false);

    const send = async (command: string, args: string) => {
        setBusy(true);
        try {
            await requestAgentStream(config, { command, args });
        } finally {
            setBusy(false);
        }
    };

    const levels = [
        { value: "0", label: t("canvas.agent.reasoningOff") },
        { value: "1", label: t("canvas.agent.reasoningNormal") },
        { value: "2", label: t("canvas.agent.reasoningMax") },
    ];

    return (
        <div className={className || "flex min-w-0 items-center gap-2"}>
            <Select value={agent.active ? String(agent.active.id) : ""} onValueChange={(value) => void send("model_set", value)} disabled={busy || !agent.models.length}>
                <SelectTrigger className="canvas-compact-control !h-10 max-w-[220px] gap-2 rounded-lg px-2 text-sm">
                    <ScanText className="size-3.5 shrink-0" />
                    <SelectValue placeholder={agent.active?.name || t("canvas.agent.noModel")} />
                </SelectTrigger>
                <SelectContent>
                    {agent.models.map((model) => (
                        <SelectItem key={model.id} value={String(model.id)}>
                            {model.name}
                        </SelectItem>
                    ))}
                </SelectContent>
            </Select>
            <Select value={typeof agent.level === "number" ? String(agent.level) : "1"} onValueChange={(value) => void send("reasoning_level_set", value)} disabled={busy || typeof agent.level !== "number"}>
                <SelectTrigger className="canvas-compact-control !h-10 w-[130px] gap-2 rounded-lg px-2 text-sm">
                    <SelectValue />
                </SelectTrigger>
                <SelectContent>
                    {levels.map((level) => (
                        <SelectItem key={level.value} value={level.value}>
                            {level.label}
                        </SelectItem>
                    ))}
                </SelectContent>
            </Select>
        </div>
    );
}
