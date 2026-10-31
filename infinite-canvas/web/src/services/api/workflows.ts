import type { AiConfig } from "@/stores/use-config-store";
import { httpJson } from "@/services/api/http";
import { gatewayProtocolRoot } from "@/services/api/runs";

/**
 * The gateway's workflow list (GET /api/workflows).
 *
 * A workflow is what the gateway runs, and a run names one. This is the only description of them the
 * canvas needs: `type` says which kind of media comes out, `parameters` carries the loader's option
 * lists, and `availability` says whether ComfyUI can actually run it right now. Which of them appear
 * in a model dropdown, and which are driven from a button, is our decision, made in the client.
 */
export type WorkflowParameter = {
    key: string;
    label?: string;
    type?: string;
    default?: unknown;
    min?: number;
    max?: number;
    step?: number;
    options?: Array<string | { value: string; label: string }>;
    description?: string;
};

type WorkflowAvailability = {
    available?: boolean;
    problems?: Array<{ message?: string }>;
};

export type GatewayWorkflow = {
    id: string;
    name: string;
    type: string;
    label?: string;
    description?: string;
    category?: string | null;
    parameters?: WorkflowParameter[];
    availability?: WorkflowAvailability;
    enabled?: boolean;
};

type WorkflowsPayload = { workflows?: GatewayWorkflow[] };

/** Read every registered workflow. */
export async function requestWorkflows(config: Pick<AiConfig, "baseUrl" | "apiKey">, options: { signal?: AbortSignal } = {}): Promise<GatewayWorkflow[]> {
    const payload = await httpJson<WorkflowsPayload>(`${gatewayProtocolRoot(config.baseUrl)}/api/workflows`, {
        headers: { Authorization: `Bearer ${config.apiKey}` },
        signal: options.signal,
        timeout: 8000,
    });
    return Array.isArray(payload?.workflows) ? payload.workflows.filter((workflow) => Boolean(workflow?.id)) : [];
}
