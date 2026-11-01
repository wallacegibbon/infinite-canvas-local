import i18n from "@/i18n";
import { LOCAL_GATEWAY_BASE_URL } from "@/constant/app-mode";
import { gatewayFetch } from "@/services/api/gateway-status";

const gatewayBase = LOCAL_GATEWAY_BASE_URL;
const apiText = (key: string, options?: Record<string, unknown>) => i18n.t(`apiErrors.${key}`, options);

/** A download URL. Resources staged under payload/ have none. */
type ResourceSource = string;

export type ResourceItem = {
    id: string;
    type: string;
    label: string;
    target: string;
    size: number;
    sha256?: string;
    license?: string;
    note?: string;
    sources: ResourceSource[];
    requires: string[];
    builder?: Record<string, unknown> | null;
    status: "present" | "missing" | "blocked";
    present: boolean;
    registered: boolean;
    exists: boolean;
    matchedFile?: string;
    matchedBy?: string;
    sizeMismatch?: string;
    actualSize: number;
    installed?: { version?: string; installedAt?: string } | null;
    missingRequires: string[];
    /** Set for type "bake": the command that produces the file locally. */
    manualCommand?: string;
    overridden?: boolean;
    overriddenBy?: string;
    wheelVersions?: string[];
    pythonRuntime?: string;
    pythonVersion?: string;
    pipName?: string;
    pipVersion?: string;
    pipRuntime?: { python: string; torch: string; cuda: string } | null;
    pipWheel?: { url: string; size?: number; sha256?: string } | null;
    pipWheelMissing?: boolean;
    pipMinVersion?: string;
    pipOutdated?: boolean;
    pipBroken?: boolean;
    restartAfterInstall?: boolean;
};

type PackMainModelOption = {
    file: string;
    label: string;
    installed: boolean;
    source: "pack" | "local";
    matches?: boolean;
    resourceId?: string;
};

type PackMainModel = {
    workflowIds: string[];
    files: string[];
    defaultFile: string;
    defaultLabel: string;
    selected: string;
    selectedLabel: string;
    selectedInstalled: boolean;
    available: boolean;
    options: PackMainModelOption[];
};

type WorkflowDependency = {
    id: string;
    label: string;
    available: boolean;
    enabled?: boolean;
    modelVisible?: boolean;
    variantLabels?: Record<string, string>;
    recommended?: boolean;
    problems: Array<{ type?: string; message?: string; classType?: string; input?: string; value?: string; candidates?: string[] }>;
    resources: ResourceItem[];
    missing: number;
    missingSize: number;
    installIds: string[];
};

export type ResourcePack = {
    id: string;
    label: string;
    description: string;
    optional?: boolean;
    recommended?: boolean;
    planned: boolean;
    workflows: string[];
    resourceIds: string[];
    requiredIds: string[];
    optionalIds: string[];
    resources: ResourceItem[];
    requiredItems: ResourceItem[];
    optionalItems: ResourceItem[];
    mainModel?: PackMainModel | null;
    workflowDependencies?: WorkflowDependency[];
    total: number;
    present: number;
    missing: number;
    missingSize: number;
    optionalTotal: number;
    optionalPresent: number;
    optionalMissing: number;
    optionalSize: number;
};

type PromptEngineCapability = {
    id: string;
    label: string;
    available: boolean;
    missingResources: Array<{ id: string; label: string }>;
    missingNodes: string[];
    reasons: string[];
};

type PromptCapabilities = {
    packId: string;
    comfyReachable: boolean;
    engines: Record<string, PromptEngineCapability>;
};

/**
 * Whether this gateway may install models at all. Configured, never inferred: installs are
 * refused unless INSTALL_INTO_COMFY=1 states that ComfyUI's model folders are reachable.
 */
type ResourceModelInstall = {
    allowed: boolean;
    /** COMFY_URL host:port — the machine the gateway has to run on. */
    comfyHost?: string;
};

export type ResourceStatus = {
    ok: boolean;
    loadedAt?: string;
    indexSource?: string;
    indexError?: string;
    indexWarning?: string;
    indexVersion?: string;
    channel?: string;
    comfyRoot?: string;
    comfyReachable?: boolean;
    modelInstall?: ResourceModelInstall;
    runtimeDeps?: Array<{ id: string; packages: string[] }>;
    promptCapabilities?: PromptCapabilities;
    modelRoots?: Record<string, string[]>;
    packs: ResourcePack[];
    resources: ResourceItem[];
    summary: {
        packs: number;
        resources: number;
        present: number;
        missing: number;
        missingSize: number;
        recommendedPacks?: number;
        recommendedResources?: number;
        recommendedPresent?: number;
        recommendedMissing?: number;
        recommendedMissingSize?: number;
    };
    lock?: { installed?: Record<string, unknown>; updatedAt?: string };
};

type ResourceJobItem = {
    id: string;
    label: string;
    type: string;
    status: string;
    bytes: number;
    size: number;
    source?: string | null;
    error?: string;
    hint?: string;
    skipped?: boolean;
};

export type ResourceJob = {
    id: string;
    status: string;
    message: string;
    createdAt: string;
    updatedAt: string;
    items: ResourceJobItem[];
    completed: number;
    total: number;
    progress: number;
    error?: string;
};

async function readJson(response: Response) {
    return (await response.json().catch(() => ({}))) as Record<string, any>;
}

function errorMessage(data: Record<string, any>, fallback: string) {
    return String(data?.error?.message || data?.message || fallback);
}

export async function fetchResourceStatus(): Promise<ResourceStatus> {
    const response = await gatewayFetch(`${gatewayBase}/api/resources/status`, { headers: { Accept: "application/json" } });
    const data = await readJson(response);
    if (!response.ok || data.ok === false) throw new Error(errorMessage(data, apiText("resourceStatusFailed", { status: response.status })));
    return data as ResourceStatus;
}

export async function refreshResourceStatus(): Promise<ResourceStatus> {
    const response = await gatewayFetch(`${gatewayBase}/api/resources/refresh`, { method: "POST", headers: { Accept: "application/json" } });
    const data = await readJson(response);
    if (!response.ok || data.ok === false) throw new Error(errorMessage(data, apiText("resourceRefreshFailed", { status: response.status })));
    return data as ResourceStatus;
}

export async function installResources(input: { ids?: string[]; packs?: string[] }): Promise<ResourceJob> {
    const response = await gatewayFetch(`${gatewayBase}/api/resources/install`, {
        method: "POST",
        headers: { Accept: "application/json", "Content-Type": "application/json" },
        body: JSON.stringify(input),
    });
    const data = await readJson(response);
    if (!response.ok || data.ok === false) throw new Error(errorMessage(data, apiText("resourceInstallStartFailed", { status: response.status })));
    return data.job as ResourceJob;
}

export async function fetchResourceJob(id: string): Promise<ResourceJob> {
    const response = await gatewayFetch(`${gatewayBase}/api/resources/jobs/${encodeURIComponent(id)}`, { headers: { Accept: "application/json" } });
    const data = await readJson(response);
    if (!response.ok || data.ok === false) throw new Error(errorMessage(data, apiText("resourceInstallProgressFailed", { status: response.status })));
    return data.job as ResourceJob;
}

export async function pickResourceReplacement(resourceId: string): Promise<string> {
    const controller = new AbortController();
    const timer = window.setTimeout(() => controller.abort(), 300000);
    try {
        const response = await gatewayFetch(`${gatewayBase}/api/resources/pick-replacement`, {
            method: "POST",
            headers: { Accept: "application/json", "Content-Type": "application/json" },
            body: JSON.stringify({ resourceId }),
            signal: controller.signal,
        });
        const data = await readJson(response);
        if (!response.ok || data.ok === false) throw new Error(errorMessage(data, apiText("resourcePickFailed", { status: response.status })));
        return String(data.file || "");
    } catch (error) {
        if (error instanceof DOMException && error.name === "AbortError") throw new Error(apiText("resourcePickTimeout"));
        throw error;
    } finally {
        window.clearTimeout(timer);
    }
}

export async function setResourceReplacement(resourceId: string, file: string): Promise<ResourceStatus> {
    const response = await gatewayFetch(`${gatewayBase}/api/resources/replacement`, {
        method: "POST",
        headers: { Accept: "application/json", "Content-Type": "application/json" },
        body: JSON.stringify({ resourceId, file }),
    });
    const data = await readJson(response);
    if (!response.ok || data.ok === false) throw new Error(errorMessage(data, apiText("resourceReplacementSaveFailed", { status: response.status })));
    return data.status as ResourceStatus;
}

export async function setWorkflowVisibility(id: string, enabled: boolean): Promise<void> {
    const response = await gatewayFetch(`${gatewayBase}/api/workflows/${encodeURIComponent(id)}/visibility`, {
        method: "POST",
        headers: { Accept: "application/json", "Content-Type": "application/json" },
        body: JSON.stringify({ enabled }),
    });
    const data = await readJson(response);
    if (!response.ok || data.ok === false) throw new Error(errorMessage(data, apiText("resourceVisibilityFailed", { status: response.status })));
}

export async function cancelResourceJob(id: string): Promise<ResourceJob> {
    const response = await gatewayFetch(`${gatewayBase}/api/resources/jobs/${encodeURIComponent(id)}/cancel`, { method: "POST", headers: { Accept: "application/json" } });
    const data = await readJson(response);
    if (!response.ok || data.ok === false) throw new Error(errorMessage(data, apiText("resourceCancelFailed", { status: response.status })));
    return data.job as ResourceJob;
}

export function formatBytes(bytes: number) {
    const value = Number(bytes) || 0;
    if (value <= 0) return "0 B";
    const units = ["B", "KB", "MB", "GB", "TB"];
    let index = 0;
    let size = value;
    while (size >= 1024 && index < units.length - 1) {
        size /= 1024;
        index += 1;
    }
    return `${size >= 10 || index === 0 ? size.toFixed(0) : size.toFixed(1)} ${units[index]}`;
}
