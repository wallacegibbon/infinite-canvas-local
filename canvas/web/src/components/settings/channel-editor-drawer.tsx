import { Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/kit/button";
import { Space } from "@/components/kit/display";
import { Drawer } from "@/components/kit/drawer";
import { Input } from "@/components/kit/input";
import { Segmented } from "@/components/kit/segmented";
import { channelName, normalizeChannelModels, type ChannelModel, type ModelCapability, type ModelChannel } from "@/stores/use-config-store";

/**
 * Rename the gateway channel, and override a model's media kind.
 *
 * There is nothing else to edit: the channel is the local gateway, its address comes from the page
 * (docs/decisions.md #71), and its model list is whatever `GET /api/workflows` reports. The one thing
 * worth keeping is the capability override — the sync preserves a value the user set here, so a
 * workflow whose `type` is not what you want it to be can be re-tagged without editing the registry.
 */
export function ChannelEditorDrawer({ open, channel, onSave, onClose }: { open: boolean; channel: ModelChannel | null; onSave: (channel: ModelChannel) => void; onClose: () => void }) {
    const { t } = useTranslation();
    const [draft, setDraft] = useState<ModelChannel | null>(channel);
    const capabilityOptions: Array<{ label: string; value: ModelCapability }> = (["image", "video", "audio"] as ModelCapability[]).map((value) => ({ label: t(`config.channelEditor.capabilities.${value}`), value }));

    useEffect(() => {
        if (open && channel) setDraft(channel);
    }, [open, channel]);

    if (!draft) return null;

    // Typing a name makes it the user's own: the key that would re-translate it goes away.
    const patch = (value: Partial<ModelChannel>) => setDraft((current) => (current ? { ...current, ...value, ...(value.name !== undefined ? { nameKey: undefined } : {}) } : current));
    const setModels = (models: ChannelModel[]) => patch({ models });

    const setCapability = (name: string, capability: ModelCapability) => setModels(draft.models.map((model) => (model.name === name ? { ...model, capability } : model)));
    const removeModel = (name: string) => setModels(draft.models.filter((model) => model.name !== name));

    const save = () => {
        onSave({
            ...draft,
            name: draft.name.trim() || t("config.channels.unnamed"),
            apiKey: "local",
            models: normalizeChannelModels(draft.models),
        });
        onClose();
    };

    return (
        <Drawer
            open={open}
            width={640}
            title={t("config.channelEditor.title")}
            onClose={onClose}
            bodyStyle={{ paddingTop: 16 }}
            extra={
                <Space>
                    <Button onClick={onClose}>{t("common.cancel")}</Button>
                    <Button type="primary" onClick={save}>
                        {t("common.save")}
                    </Button>
                </Space>
            }
        >
            <div className="grid gap-4 md:grid-cols-2">
                <label className="block">
                    <span className="mb-1 block text-sm font-medium">{t("config.channelEditor.name")}</span>
                    <Input value={channelName(draft)} onChange={(event) => patch({ name: event.target.value })} />
                </label>
                <div className="block">
                    <span className="mb-1 block text-sm font-medium">{t("config.channelEditor.protocol")}</span>
                    <div className="flex h-8 items-center text-sm text-stone-500">{t("config.channelEditor.localProtocol")}</div>
                </div>
                <div className="block md:col-span-2">
                    <span className="mb-1 block text-sm font-medium">{t("config.channelEditor.baseUrl")}</span>
                    <div className="flex h-8 items-center font-mono text-sm text-stone-500">{draft.baseUrl}</div>
                </div>
            </div>

            <div className="mt-6 mb-3">
                <div className="text-sm font-semibold">{t("config.channelEditor.models")}</div>
                <div className="mt-0.5 text-xs text-stone-500">{t("config.channelEditor.localModelDescription", { count: draft.models.length })}</div>
            </div>

            <div className="space-y-2 rounded-lg border border-stone-200 p-2 dark:border-stone-800">
                {draft.models.length ? (
                    draft.models.map((model) => (
                        <div key={model.name} className="flex flex-wrap items-center gap-3 rounded-md px-2 py-1.5 hover:bg-stone-50 dark:hover:bg-stone-900/40">
                            <span className="min-w-0 flex-1 truncate text-sm" title={model.name}>
                                {model.name}
                            </span>
                            <div className="flex shrink-0 items-center gap-2">
                                <Segmented size="small" value={model.capability} options={capabilityOptions} onChange={(value) => setCapability(model.name, value as ModelCapability)} />
                                <Button size="small" danger type="text" icon={<Trash2 className="size-3.5" />} onClick={() => removeModel(model.name)} />
                            </div>
                        </div>
                    ))
                ) : (
                    <div className="px-2 py-8 text-center text-sm text-stone-500">{t("config.channelEditor.empty")}</div>
                )}
            </div>
        </Drawer>
    );
}
