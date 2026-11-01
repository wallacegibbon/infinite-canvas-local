import { useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";
import { ScanText } from "lucide-react";

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/kit/select-primitive";
import { agentSession, sendAgentMessage, subscribeAgentSessions, type AgentSession } from "@/services/api/agent";

/**
 * One session's two settings, displayed and changed.
 *
 * There is nothing to fetch and nothing to remember: the agent announces its models, its active model
 * and its reasoning level when it starts and whenever they change, so both controls render whatever
 * that session last said. Changing one sends the agent's own command down the same socket — `model_set`
 * with an id, `reasoning_level_set` with 0/1/2 — and the answer moves the display.
 *
 * `session` is which agent is being configured. Each surface has its own, and they do not share a model
 * choice: a node panel's session is not the canvas's.
 */
export function AgentSettingsControls({ session, className }: { session: string; className?: string }) {
    const { t } = useTranslation();
    const state = useSyncExternalStore(subscribeAgentSessions, () => agentSession(session));

    const send = (command: string, args: string) => sendAgentMessage(session, { command, args });

    const levels: Array<{ value: string; label: string }> = [
        { value: "0", label: t("canvas.agent.reasoningOff") },
        { value: "1", label: t("canvas.agent.reasoningNormal") },
        { value: "2", label: t("canvas.agent.reasoningMax") },
    ];

    return (
        <div className={className || "flex min-w-0 items-center gap-2"}>
            <Select value={state.active ? String(state.active.id) : ""} onValueChange={(value) => send("model_set", value)} disabled={!state.models.length}>
                <SelectTrigger className="canvas-compact-control !h-10 max-w-[220px] gap-2 rounded-lg px-2 text-sm">
                    <ScanText className="size-3.5 shrink-0" />
                    <SelectValue placeholder={state.active?.name || t("canvas.agent.noModel")} />
                </SelectTrigger>
                <SelectContent>
                    {state.models.map((model) => (
                        <SelectItem key={model.id} value={String(model.id)}>
                            {model.name}
                        </SelectItem>
                    ))}
                </SelectContent>
            </Select>
            <Select value={typeof state.level === "number" ? String(state.level) : "1"} onValueChange={(value) => send("reasoning_level_set", value)} disabled={typeof state.level !== "number"}>
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

export type { AgentSession };
