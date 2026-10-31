import type { AiConfig } from "@/stores/use-config-store";
import { httpJson } from "@/services/api/http";
import { gatewayProtocolRoot } from "@/services/api/runs";

/**
 * The gateway's workflow list (GET /api/workflows).
 *
 * A workflow is what the gateway runs, and a run names one. This is the only description of them the
 * canvas needs: `type` says which kind of media comes out, `parameters` carries the loader's option
 * lists, `availability` says whether ComfyUI can run it right now, and `models` lists the ids it
 * answers to. Which of them appear in a model dropdown, and which are driven from a button, is our
 * decision, made in the client.
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

/**
 * One id a workflow answers to.
 *
 * The same graph can be run with different base models, and each combination has its own spelling
 * (`img-zit-int8`). That spelling is built and resolved by the gateway, so it arrives from there; the
 * canvas only decides that each one is worth an entry in the model picker.
 */
type GatewayWorkflowModel = {
    id: string;
    label?: string;
    baseModel?: string | null;
    baseModelLabel?: string | null;
    imageInputs?: number;
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
    /** The variant modes an image workflow offers (`t2i`, `edit`, `inpaint`, `outpaint`). */
    variantModes?: string[];
    variantLabels?: Record<string, string>;
    models?: GatewayWorkflowModel[];
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
