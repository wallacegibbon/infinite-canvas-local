import { useEffect } from "react";

import { isLocalGatewayUrl, LOCAL_GATEWAY_BASE_URL } from "@/constant/app-mode";
import { gatewayModels, modelSignature } from "@/lib/gateway-models";
import { markGatewayUp, noteGatewayError } from "@/services/api/gateway-status";
import { requestWorkflows } from "@/services/api/workflows";
import { encodeChannelModel, modelOptionsFromChannels, useConfigStore, type ChannelModel, type ModelCapability } from "@/stores/use-config-store";

const SYNC_INTERVAL_MS = 8000;
const REQUEST_TIMEOUT_MS = 4000;

const SYNC_CONFIG = { baseUrl: LOCAL_GATEWAY_BASE_URL, apiKey: "local" };

/** Sync models from the gateway automatically, so adding or removing a workflow needs no manual channel refresh. */
export function useLocalGatewaySync() {
    useEffect(() => {
        let stopped = false;
        let timer: number | undefined;

        const applyModels = (models: ChannelModel[]) => {
            const state = useConfigStore.getState();
            const config = state.config;
            const channel = config.channels.find((item) => isLocalGatewayUrl(item.baseUrl)) || config.channels[0];
            if (!channel) return;
            if (!models.length && !channel.models.length) return;

            const nextModels = models.map((model) => {
                const previous = channel.models.find((item) => item.name === model.name);
                return { ...model, capability: previous?.capability || model.capability };
            });
            if (modelSignature(channel.models) === modelSignature(nextModels)) return;

            const channels = config.channels.map((item) => (item.id === channel.id ? { ...item, apiKey: item.apiKey || "local", baseUrl: LOCAL_GATEWAY_BASE_URL, models: nextModels } : item));
            const optionsFor = (capability: ModelCapability) => channels.flatMap((item) => item.models.filter((model) => model.capability === capability).map((model) => encodeChannelModel(item.id, model.name)));
            const pick = (current: string, options: string[]) => (options.includes(current) ? current : options[0] || "");
            const imageOptions = optionsFor("image");
            useConfigStore.setState({
                config: {
                    ...config,
                    channels,
                    models: modelOptionsFromChannels(channels),
                    imageModel: pick(config.imageModel, imageOptions),
                    videoModel: pick(config.videoModel, optionsFor("video")),
                    audioModel: pick(config.audioModel, optionsFor("audio")),
                    // The image model is also the general one: text-to-image is the default action.
                    model: pick(config.model, imageOptions),
                    baseUrl: LOCAL_GATEWAY_BASE_URL,
                    apiKey: channel.apiKey || "local",
                },
            });
        };

        const sync = async () => {
            if (timer) {
                window.clearTimeout(timer);
                timer = undefined;
            }
            try {
                const controller = new AbortController();
                const timeout = window.setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
                const workflows = await requestWorkflows(SYNC_CONFIG, { signal: controller.signal }).finally(() => window.clearTimeout(timeout));
                markGatewayUp();
                applyModels(gatewayModels(workflows));
            } catch (error) {
                // Retry silently while the gateway is still starting so the canvas stays usable; the shared banner reports disconnects.
                noteGatewayError(error, `${LOCAL_GATEWAY_BASE_URL.replace(/\/+$/, "")}/api/workflows`);
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
