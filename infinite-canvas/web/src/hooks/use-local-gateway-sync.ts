import { useEffect } from "react";

import { isLocalGatewayUrl, LOCAL_GATEWAY_BASE_URL } from "@/constant/app-mode";
import { gatewayModelCapability } from "@/lib/gateway-capability";
import { markGatewayUp, noteGatewayError } from "@/services/api/gateway-status";
import { encodeChannelModel, modelOptionsFromChannels, useConfigStore, type ModelCapability } from "@/stores/use-config-store";

const SYNC_INTERVAL_MS = 8000;
const REQUEST_TIMEOUT_MS = 4000;

/** Sync models from the gateway automatically, so adding or removing a workflow needs no manual channel refresh. */
export function useLocalGatewaySync() {
    useEffect(() => {
        let stopped = false;
        let timer: number | undefined;

        const sync = async () => {
            if (timer) {
                window.clearTimeout(timer);
                timer = undefined;
            }
            try {
                const controller = new AbortController();
                const timeout = window.setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
                const response = await fetch(`${LOCAL_GATEWAY_BASE_URL.replace(/\/+$/, "")}/models`, {
                    cache: "no-store",
                    signal: controller.signal,
                });
                window.clearTimeout(timeout);
                if (!response.ok) throw new Error(`HTTP ${response.status}`);
                markGatewayUp();
                const payload = (await response.json()) as {
                    data?: Array<{
                        id?: string;
                        metadata?: { type?: string; workflowId?: string; label?: string; baseModel?: string; baseModelLabel?: string; imageInputs?: number; variantModes?: string[]; variantLabels?: Record<string, string> };
                    }>;
                };
                const synced = (payload.data || [])
                    .map((item) => {
                        const name = String(item.id || "").trim();
                        const meta = item.metadata || {};
                        const capability = gatewayModelCapability(name, meta.type);
                        return {
                            name,
                            capability,
                            label: meta.label?.trim() || undefined,
                            workflowId: meta.workflowId?.trim() || undefined,
                            baseModel: meta.baseModel?.trim() || undefined,
                            baseModelLabel: meta.baseModelLabel?.trim() || undefined,
                            imageInputs: typeof meta.imageInputs === "number" ? meta.imageInputs : undefined,
                            variantModes: Array.isArray(meta.variantModes) ? meta.variantModes.map(String) : undefined,
                            variantLabels: meta.variantLabels && typeof meta.variantLabels === "object" ? meta.variantLabels : undefined,
                        };
                    })
                    .filter((model) => Boolean(model.name) && Boolean(model.capability));
                if (synced.length) {
                    const state = useConfigStore.getState();
                    const config = state.config;
                    const channel = config.channels.find((item) => isLocalGatewayUrl(item.baseUrl)) || config.channels[0];
                    if (channel) {
                        const nextModels = synced.map((model) => {
                            const previous = channel.models.find((item) => item.name === model.name);
                            return { ...model, capability: previous?.capability || (model.capability as ModelCapability) };
                        });
                        const signature = (
                            models: Array<{ name: string; capability: string; label?: string; workflowId?: string; baseModel?: string; baseModelLabel?: string; imageInputs?: number; variantModes?: string[]; variantLabels?: Record<string, string> }>,
                        ) =>
                            JSON.stringify(
                                models.map((model) => [
                                    model.name,
                                    model.capability,
                                    model.label || "",
                                    model.workflowId || "",
                                    model.baseModel || "",
                                    model.baseModelLabel || "",
                                    model.imageInputs ?? -1,
                                    model.variantModes || [],
                                    model.variantLabels || {},
                                ]),
                            );
                        if (signature(channel.models) !== signature(nextModels)) {
                            // After a gateway port change the stale baseUrl saved in the browser must follow the runtime address, or every request fails with "Failed to fetch".
                            const channels = config.channels.map((item) => (item.id === channel.id ? { ...item, apiKey: item.apiKey || "local", baseUrl: LOCAL_GATEWAY_BASE_URL, models: nextModels } : item));
                            const imageOptions = channels.flatMap((item) => item.models.filter((model) => model.capability === "image").map((model) => encodeChannelModel(item.id, model.name)));
                            const videoOptions = channels.flatMap((item) => item.models.filter((model) => model.capability === "video").map((model) => encodeChannelModel(item.id, model.name)));
                            const audioOptions = channels.flatMap((item) => item.models.filter((model) => model.capability === "audio").map((model) => encodeChannelModel(item.id, model.name)));
                            const pick = (current: string, options: string[]) => (options.includes(current) ? current : options[0] || "");
                            const pickImage = (current: string) => {
                                if (imageOptions.includes(current)) return current;
                                if (String(current).endsWith("::image-krea2-aesthetics")) {
                                    const turbo = imageOptions.find((option) => option.endsWith("::image-krea2-turbo"));
                                    if (turbo) return turbo;
                                }
                                return pick(current, imageOptions);
                            };
                            useConfigStore.setState({
                                config: {
                                    ...config,
                                    channels,
                                    models: modelOptionsFromChannels(channels),
                                    imageModel: pickImage(config.imageModel),
                                    videoModel: pick(config.videoModel, videoOptions),
                                    audioModel: pick(config.audioModel, audioOptions),
                                    model: pickImage(config.model),
                                    baseUrl: LOCAL_GATEWAY_BASE_URL,
                                    apiKey: channel.apiKey || "local",
                                },
                            });
                        }
                    }
                } else {
                    const state = useConfigStore.getState();
                    const config = state.config;
                    const channel = config.channels.find((item) => isLocalGatewayUrl(item.baseUrl)) || config.channels[0];
                    if (channel && channel.models.length) {
                        const channels = config.channels.map((item) => (item.id === channel.id ? { ...item, apiKey: item.apiKey || "local", baseUrl: LOCAL_GATEWAY_BASE_URL, models: [] } : item));
                        useConfigStore.setState({
                            config: {
                                ...config,
                                channels,
                                models: modelOptionsFromChannels(channels),
                                imageModel: "",
                                videoModel: "",
                                audioModel: "",
                                model: "",
                                baseUrl: LOCAL_GATEWAY_BASE_URL,
                                apiKey: channel.apiKey || "local",
                            },
                        });
                    }
                }
            } catch (error) {
                // Retry silently while the gateway is still starting so the canvas stays usable; the shared banner reports disconnects.
                noteGatewayError(error, `${LOCAL_GATEWAY_BASE_URL.replace(/\/+$/, "")}/models`);
            }
            if (!stopped) timer = window.setTimeout(sync, SYNC_INTERVAL_MS);
        };

        void sync();
        const refresh = () => void sync();
        window.addEventListener("local-gateway-refresh-models", refresh);
        return () => {
            stopped = true;
            window.removeEventListener("local-gateway-refresh-models", refresh);
            if (timer) window.clearTimeout(timer);
        };
    }, []);
}
