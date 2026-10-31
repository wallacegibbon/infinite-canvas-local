import { ArrowLeft, ArrowRight, ChevronDown, Lock, Pencil, Plus, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/kit/button";
import { Switch } from "@/components/kit/choice";
import { Dialog } from "@/components/kit/dialog";
import { Field } from "@/components/kit/field";
import { Input } from "@/components/kit/input";
import { Tabs } from "@/components/kit/nav";
import { Select } from "@/components/kit/select";
import { toast } from "@/components/kit/toast";
import { confirm } from "@/components/kit/confirm";
import { ModelPicker, WorkflowModelPicker } from "@/components/model-picker";
import { imageAspectOptions } from "@/components/image-settings-panel";
import { videoResolutionOptions } from "@/components/video-settings-panel";
import { ChannelEditorDrawer } from "@/components/settings/channel-editor-drawer";
import { ConfigLocalStorage } from "@/components/settings/config-local-storage";
import { ResourceCenterPanel } from "@/components/settings/resource-center-panel";
import { computeMediaSize, inferMediaRatio, inferMediaScale, mediaScaleOptions, parseVideoResolution } from "@/lib/media-size";
import { progressRingSkins } from "@/lib/canvas/progress-ring-skins";
import { cardSkinIds, getCanvasTheme, type CardSkinId } from "@/lib/canvas-theme";
import { stone } from "@/lib/design-tokens";
import { isCardSkinLocked, verifySkinPassword } from "@/lib/canvas/skin-unlocks";
import { persistSkinUnlock } from "@/services/api/skin-unlocks";
import { useThemeStore } from "@/stores/use-theme-store";
import { channelName, modelOptionsFromChannels, normalizeModelOptionValue, selectableModelsByCapability, useConfigStore, type AiConfig, type ConfigTabKey, type ModelCapability, type ModelChannel } from "@/stores/use-config-store";

type PromptEndpointDraft = { name: string; baseUrl: string; apiKey: string; model: string };

const EMPTY_PROMPT_ENDPOINT: PromptEndpointDraft = { name: "", baseUrl: "", apiKey: "", model: "" };

type ModelGroup = {
    capability: ModelCapability;
    modelKey: "imageModel" | "videoModel" | "textModel" | "audioModel";
    labelKey: string;
};

const modelGroups: ModelGroup[] = [
    { capability: "image", modelKey: "imageModel", labelKey: "config.preferences.defaultImageModel" },
    { capability: "video", modelKey: "videoModel", labelKey: "config.preferences.defaultVideoModel" },
    { capability: "text", modelKey: "textModel", labelKey: "config.preferences.defaultTextModel" },
    { capability: "audio", modelKey: "audioModel", labelKey: "config.preferences.defaultAudioModel" },
];

function AppConfigPanel({ showDoneButton = false, initialTab = "channels" }: { showDoneButton?: boolean; initialTab?: ConfigTabKey }) {
    const { t } = useTranslation();
    const [activeTab, setActiveTab] = useState<ConfigTabKey>(initialTab);
    const [editingChannelId, setEditingChannelId] = useState("");
    const [passwordSkinId, setPasswordSkinId] = useState<CardSkinId | null>(null);
    const [skinPassword, setSkinPassword] = useState("");
    const [skinPasswordError, setSkinPasswordError] = useState("");
    const [verifyingSkinPassword, setVerifyingSkinPassword] = useState(false);
    const [fishOptionsOpen, setFishOptionsOpen] = useState(false);
    const config = useConfigStore((state) => state.config);
    const appTheme = useThemeStore((state) => state.theme);
    const cardSkin = useThemeStore((state) => state.cardSkin) || "warm";
    const unlockedCardSkins = useThemeStore((state) => state.unlockedCardSkins);
    const setCardSkin = useThemeStore((state) => state.setCardSkin);
    const unlockCardSkin = useThemeStore((state) => state.unlockCardSkin);
    const fishWhipEnabled = useThemeStore((state) => state.fishWhipEnabled);
    const setFishWhipEnabled = useThemeStore((state) => state.setFishWhipEnabled);
    const fishWhipSide = useThemeStore((state) => state.fishWhipSide);
    const setFishWhipSide = useThemeStore((state) => state.setFishWhipSide);
    const updateConfig = useConfigStore((state) => state.updateConfig);
    const shouldPromptContinue = useConfigStore((state) => state.shouldPromptContinue);
    const setConfigDialogOpen = useConfigStore((state) => state.setConfigDialogOpen);
    const clearPromptContinue = useConfigStore((state) => state.clearPromptContinue);
    const editingChannel = config.channels.find((channel) => channel.id === editingChannelId) || null;
    const imageResolutionOptions = mediaScaleOptions.map((value) => ({ value, label: value === "auto" ? t("settingsPanels.common.auto") : value.toUpperCase() }));
    useEffect(() => setActiveTab(initialTab), [initialTab]);

    const saveConfig = (nextConfig: AiConfig) => {
        (Object.keys(nextConfig) as Array<keyof AiConfig>).forEach((key) => updateConfig(key, nextConfig[key]));
    };

    const selectCardSkin = (skinId: CardSkinId) => {
        if (isCardSkinLocked(skinId) && !unlockedCardSkins.includes(skinId)) {
            setFishOptionsOpen(true);
            setPasswordSkinId(skinId);
            setSkinPassword("");
            setSkinPasswordError("");
            return;
        }
        if (skinId === "fish" || skinId === "angel") {
            setFishOptionsOpen(cardSkin === skinId ? !fishOptionsOpen : true);
        } else {
            setFishOptionsOpen(false);
        }
        setCardSkin(skinId);
    };

    const closeSkinPasswordModal = () => {
        if (verifyingSkinPassword) return;
        setPasswordSkinId(null);
        setSkinPassword("");
        setSkinPasswordError("");
    };

    const unlockSelectedSkin = async () => {
        if (!passwordSkinId || verifyingSkinPassword) return;
        setVerifyingSkinPassword(true);
        setSkinPasswordError("");
        try {
            const valid = await verifySkinPassword(passwordSkinId, skinPassword);
            if (!valid) {
                setSkinPasswordError(t("config.preferences.cardSkinPasswordWrong"));
                return;
            }
            unlockCardSkin(passwordSkinId);
            if (passwordSkinId === "fish") setFishOptionsOpen(true);
            void persistSkinUnlock(passwordSkinId).catch(() => undefined);
            toast.success(t("config.preferences.cardSkinUnlocked", { name: t(`config.preferences.cardSkins.${passwordSkinId}.label`) }));
            setPasswordSkinId(null);
            setSkinPassword("");
        } catch {
            setSkinPasswordError(t("config.preferences.cardSkinPasswordUnavailable"));
        } finally {
            setVerifyingSkinPassword(false);
        }
    };

    useEffect(() => {
        if ((cardSkin === "fish" && unlockedCardSkins.includes("fish")) || cardSkin === "angel") setFishOptionsOpen(true);
    }, [cardSkin, unlockedCardSkins]);

    const finishConfig = () => {
        const ready = config.channels.some((channel) => channel.baseUrl.trim() && channel.apiKey.trim() && channel.models.length);
        setConfigDialogOpen(false);
        if (!ready) return;
        toast.success(t(shouldPromptContinue ? "config.savedContinue" : "config.saved"));
        clearPromptContinue();
    };

    const updateChannels = (channels: ModelChannel[]) => saveConfig(withChannels(config, channels));

    const saveChannel = (channel: ModelChannel) => {
        updateChannels(config.channels.map((item) => (item.id === channel.id ? channel : item)));
    };

    return (
        <>
            <div className="border-b border-stone-200 pb-3 text-xs text-stone-500 dark:border-stone-800">{t("config.localStorageNote")}</div>
            <Tabs
                activeKey={activeTab}
                onChange={(key) => setActiveTab(key as ConfigTabKey)}
                items={[
                    {
                        key: "channels",
                        label: t("config.tabs.channels"),
                        children: (
                            <div>
                                <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
                                    <div className="text-xs text-stone-500">{t("config.channels.localOnlyDescription")}</div>
                                </div>
                                <div className="space-y-2">
                                    {config.channels.map((channel) => (
                                        <div key={channel.id} className="flex items-center justify-between gap-3 rounded-lg border border-stone-200 px-4 py-3 dark:border-stone-800">
                                            <div className="min-w-0">
                                                <div className="truncate text-sm font-semibold">{channelName(channel) || t("config.channels.unnamed")}</div>
                                                <div className="mt-1 truncate text-xs text-stone-500">
                                                    {t("config.channels.localGateway")} · {t("config.channels.modelCount", { count: channel.models.length })} · {channel.baseUrl || t("config.channels.missingUrl")}
                                                </div>
                                            </div>
                                            <div className="flex shrink-0 gap-2">
                                                <Button size="small" icon={<Pencil className="size-3.5" />} onClick={() => setEditingChannelId(channel.id)}>
                                                    {t("common.edit")}
                                                </Button>
                                            </div>
                                        </div>
                                    ))}
                                </div>
                            </div>
                        ),
                    },
                    {
                        key: "preferences",
                        label: t("config.tabs.preferences"),
                        children: (
                            <div>
                                <div className="mb-2 text-sm font-semibold">{t("config.preferences.defaultModels")}</div>
                                <div className="mb-4 grid gap-4 md:grid-cols-2 xl:grid-cols-4">
                                    {modelGroups
                                        .filter((group) => group.capability === "image" || group.capability === "video")
                                        .map((group) => (
                                            <Field key={group.modelKey} label={t(group.labelKey)} className="mb-0">
                                                {group.capability === "image" || group.capability === "video" ? (
                                                    <WorkflowModelPicker config={config} value={config[group.modelKey]} onChange={(model) => updateConfig(group.modelKey, model)} capability={group.capability} fullWidth />
                                                ) : (
                                                    <ModelPicker config={config} value={config[group.modelKey]} onChange={(model) => updateConfig(group.modelKey, model)} capability={group.capability} fullWidth />
                                                )}
                                            </Field>
                                        ))}
                                </div>
                                <div className="mb-2 text-sm font-semibold">{t("config.preferences.generation")}</div>
                                <div className="grid gap-4 md:grid-cols-4">
                                    <Field label={t("config.preferences.canvasImageCount")} extra={t("config.preferences.canvasImageCountDescription")} className="mb-4">
                                        <Input
                                            type="number"
                                            min={1}
                                            max={15}
                                            value={config.canvasImageCount}
                                            onChange={(event) => updateConfig("canvasImageCount", event.target.value)}
                                            onBlur={(event) => updateConfig("canvasImageCount", normalizeImageCount(event.target.value))}
                                        />
                                    </Field>
                                    <Field label={t("config.preferences.imageResolution")} className="mb-4">
                                        <Select value={inferMediaScale(config.size)} options={imageResolutionOptions} onChange={(scale) => updateConfig("size", computeMediaSize(scale, inferMediaRatio(config.size)))} />
                                    </Field>
                                    <Field label={t("config.preferences.imageAspectRatio")} className="mb-4">
                                        <Select value={inferMediaRatio(config.size)} options={imageAspectOptions} onChange={(ratio) => updateConfig("size", computeMediaSize(inferMediaScale(config.size), ratio))} />
                                    </Field>
                                    <Field label={t("config.preferences.videoResolution")} className="mb-4">
                                        <Select value={parseVideoResolution(config.vquality)} options={videoResolutionOptions} onChange={(value) => updateConfig("vquality", value)} />
                                    </Field>
                                </div>
                                <Field label={t("config.preferences.cardSkin")} extra={t("config.preferences.cardSkinDescription")} className="mb-4">
                                    <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
                                        {cardSkinIds.map((id) => {
                                            const locked = isCardSkinLocked(id) && !unlockedCardSkins.includes(id);
                                            const selected = cardSkin === id && !locked;
                                            const palette = getCanvasTheme(appTheme, id).node;
                                            return (
                                                <button
                                                    key={id}
                                                    type="button"
                                                    title={locked ? t("config.preferences.cardSkinLockedHint") : undefined}
                                                    onClick={() => selectCardSkin(id)}
                                                    className={`flex items-center gap-3 rounded-lg border px-3 py-2.5 text-left transition ${selected ? "border-stone-900 bg-stone-100 dark:border-stone-100 dark:bg-white/10" : "border-stone-200 hover:bg-stone-50 dark:border-stone-800 dark:hover:bg-white/5"}`}
                                                >
                                                    <span className="relative grid h-10 w-16 shrink-0 place-items-center overflow-hidden rounded-md border" style={{ background: palette.fill, borderColor: palette.stroke }}>
                                                        {id === "fish" ? (
                                                            <img src="/skins/lantern-guard-idle.png" alt="" draggable={false} className="size-10 object-contain" />
                                                        ) : id === "angel" ? (
                                                            <img src="/skins/angel-girl-idle.webp" alt="" draggable={false} className="size-10 object-contain" />
                                                        ) : (
                                                            <span className="space-y-1">
                                                                <span className="block h-1 w-8 rounded-full" style={{ background: palette.text, opacity: 0.85 }} />
                                                                <span className="block h-1 w-5 rounded-full" style={{ background: palette.muted, opacity: 0.65 }} />
                                                            </span>
                                                        )}
                                                        {locked ? (
                                                            <span className="absolute inset-0 grid place-items-center bg-stone-950/45 text-white">
                                                                <Lock className="size-4" />
                                                            </span>
                                                        ) : null}
                                                    </span>
                                                    <span className="min-w-0">
                                                        <span className="flex items-center gap-1.5 text-sm font-medium">
                                                            <span className="truncate">{t(`config.preferences.cardSkins.${id}.label`)}</span>
                                                            {locked ? <span className="shrink-0 rounded-full border border-current px-1.5 py-0.5 text-[9px] font-medium opacity-70">{t("config.preferences.cardSkinLocked")}</span> : null}
                                                            {(id === "fish" || id === "angel") && !locked ? <ChevronDown className={`ml-auto size-3.5 shrink-0 opacity-45 transition-transform ${selected && fishOptionsOpen ? "rotate-180" : ""}`} /> : null}
                                                        </span>
                                                        <span className="mt-0.5 block truncate text-[11px] opacity-60">{locked ? t("config.preferences.cardSkinLockedHint") : t(`config.preferences.cardSkins.${id}.description`)}</span>
                                                    </span>
                                                </button>
                                            );
                                        })}
                                    </div>
                                    {((cardSkin === "fish" && unlockedCardSkins.includes("fish")) || cardSkin === "angel") && fishOptionsOpen ? (
                                        <div className="mt-3 rounded-lg border border-stone-200 bg-stone-50/80 px-3 py-3 dark:border-stone-800 dark:bg-white/5">
                                            <div className="flex items-start justify-between gap-4">
                                                <div className="min-w-0">
                                                    <div className="text-sm font-medium">{t("config.preferences.fishWhip")}</div>
                                                    <div className="mt-1 text-[11px] leading-5 opacity-60">{t("config.preferences.fishWhipDescription")}</div>
                                                </div>
                                                <Switch checked={fishWhipEnabled} onChange={setFishWhipEnabled} />
                                            </div>
                                            <div className="mt-3 flex items-center justify-between gap-3 border-t border-stone-200/70 pt-3 dark:border-stone-800/70">
                                                <div className="text-xs opacity-60">{t("config.preferences.fishWhipSide")}</div>
                                                <Button size="small" icon={fishWhipSide === "right" ? <ArrowLeft className="size-3.5" /> : <ArrowRight className="size-3.5" />} onClick={() => setFishWhipSide(fishWhipSide === "right" ? "left" : "right")}>
                                                    {fishWhipSide === "right" ? t("config.preferences.fishWhipMoveLeft") : t("config.preferences.fishWhipMoveRight")}
                                                </Button>
                                            </div>
                                        </div>
                                    ) : null}
                                </Field>
                                <Field label={t("config.preferences.progressRingSkin")} extra={t("config.preferences.progressRingSkinDescription")} className="mb-4">
                                    <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
                                        {progressRingSkins.map((skin) => {
                                            const selected = (config.progressRingSkin || "cyan") === skin.id;
                                            const palette = appTheme === "dark" ? skin.dark : skin.light;
                                            return (
                                                <button
                                                    key={skin.id}
                                                    type="button"
                                                    onClick={() => updateConfig("progressRingSkin", skin.id)}
                                                    className={`flex items-center gap-3 rounded-lg border px-3 py-2.5 text-left transition ${selected ? "border-stone-900 bg-stone-100 dark:border-stone-100 dark:bg-white/10" : "border-stone-200 hover:bg-stone-50 dark:border-stone-800 dark:hover:bg-white/5"}`}
                                                >
                                                    <span className="relative grid h-7 w-14 shrink-0 place-items-center overflow-hidden rounded-md" style={{ background: appTheme === "dark" ? stone[900] : stone[100] }}>
                                                        {skin.mode === "tide" ? (
                                                            <span
                                                                className="absolute inset-x-0 bottom-0 h-[62%]"
                                                                style={{ background: `linear-gradient(to top, ${palette.glow}66, ${palette.core}2e)`, borderTop: `1px solid ${palette.glow}`, boxShadow: `0 -3px 8px ${palette.glow}66` }}
                                                            />
                                                        ) : (
                                                            <span
                                                                className="absolute inset-x-1.5 top-1/2 h-[2px] -translate-y-1/2 rounded-full"
                                                                style={{ background: `linear-gradient(90deg, ${palette.gradient[0]}, ${palette.gradient[2]})`, boxShadow: `0 0 8px ${palette.glow}` }}
                                                            />
                                                        )}
                                                    </span>
                                                    <span className="min-w-0">
                                                        <span className="block text-sm font-medium">{t(`config.preferences.progressRingSkins.${skin.id}.label`)}</span>
                                                        <span className="mt-0.5 block truncate text-[11px] opacity-60">{t(`config.preferences.progressRingSkins.${skin.id}.description`)}</span>
                                                    </span>
                                                </button>
                                            );
                                        })}
                                    </div>
                                </Field>
                                <Field className="mb-4">
                                    <div className="flex items-center justify-between gap-3">
                                        <span className="text-sm">{t("config.preferences.autoFocusOnSelect")}</span>
                                        <Switch checked={config.autoFocusOnSelect} onChange={(checked) => updateConfig("autoFocusOnSelect", checked)} />
                                    </div>
                                </Field>
                                <Field label={t("config.preferences.systemPrompt")} className="mb-0">
                                    <Input.TextArea rows={4} value={config.systemPrompt} placeholder={t("config.preferences.systemPromptPlaceholder")} onChange={(event) => updateConfig("systemPrompt", event.target.value)} />
                                </Field>
                            </div>
                        ),
                    },
                    {
                        key: "resources",
                        label: t("config.tabs.resources"),
                        children: <ResourceCenterPanel />,
                    },
                    {
                        key: "local-storage",
                        label: t("config.tabs.localStorage"),
                        children: <ConfigLocalStorage active={activeTab === "local-storage"} />,
                    },
                ]}
            />
            {showDoneButton ? (
                <div className="mt-4 flex justify-end">
                    <Button type="primary" onClick={finishConfig}>
                        {t("common.done")}
                    </Button>
                </div>
            ) : null}
            <ChannelEditorDrawer open={Boolean(editingChannel)} channel={editingChannel} onSave={saveChannel} onClose={() => setEditingChannelId("")} />
            <Dialog
                open={Boolean(passwordSkinId)}
                size="form"
                maskClosable={false}
                title={passwordSkinId ? t("config.preferences.cardSkinUnlockTitle", { name: t(`config.preferences.cardSkins.${passwordSkinId}.label`) }) : ""}
                onClose={closeSkinPasswordModal}
                footer={
                    <>
                        <Button disabled={verifyingSkinPassword} onClick={closeSkinPasswordModal}>
                            {t("common.cancel")}
                        </Button>
                        <Button type="primary" loading={verifyingSkinPassword} disabled={!skinPassword.trim()} onClick={() => void unlockSelectedSkin()}>
                            {t("config.preferences.cardSkinUnlockAction")}
                        </Button>
                    </>
                }
            >
                <p className="mb-3 text-sm text-stone-500">{t("config.preferences.cardSkinUnlockHint")}</p>
                <Input.Password
                    autoFocus
                    value={skinPassword}
                    status={skinPasswordError ? "error" : undefined}
                    placeholder={t("config.preferences.cardSkinPasswordPlaceholder")}
                    onChange={(event) => {
                        setSkinPassword(event.target.value);
                        if (skinPasswordError) setSkinPasswordError("");
                    }}
                    onPressEnter={() => {
                        if (skinPassword.trim() && !verifyingSkinPassword) void unlockSelectedSkin();
                    }}
                />
                {skinPasswordError ? <p className="mt-2 text-xs text-red-500">{skinPasswordError}</p> : null}
            </Dialog>
        </>
    );
}

export function AppConfigModal() {
    const { t } = useTranslation();
    const isConfigOpen = useConfigStore((state) => state.isConfigOpen);
    const configTab = useConfigStore((state) => state.configTab);
    const setConfigDialogOpen = useConfigStore((state) => state.setConfigDialogOpen);
    return (
        <Dialog
            title={
                <div>
                    <div className="text-lg font-semibold">{t("config.title")}</div>
                    <div className="mt-1 text-xs font-normal text-stone-500">{t("config.modalDescription")}</div>
                </div>
            }
            open={isConfigOpen}
            size="panel"
            onClose={() => setConfigDialogOpen(false)}
            bodyStyle={{ maxHeight: "72vh" }}
            bodyClassName="pr-3"
        >
            <AppConfigPanel showDoneButton initialTab={configTab} />
        </Dialog>
    );
}

function withChannels(config: AiConfig, channels: ModelChannel[]): AiConfig {
    const next: AiConfig = {
        ...config,
        channels,
        models: modelOptionsFromChannels(channels),
        baseUrl: channels[0]?.baseUrl || config.baseUrl,
        apiKey: channels[0]?.apiKey || config.apiKey,
    };
    return {
        ...next,
        imageModel: pickDefaultModel(next, "image", config.imageModel),
        videoModel: pickDefaultModel(next, "video", config.videoModel),
        textModel: pickDefaultModel(next, "text", config.textModel),
        audioModel: pickDefaultModel(next, "audio", config.audioModel),
    };
}

function pickDefaultModel(config: AiConfig, capability: ModelCapability, current: string) {
    const options = selectableModelsByCapability(config, capability);
    const normalized = normalizeModelOptionValue(current, config.channels);
    return options.includes(normalized) ? normalized : options[0] || "";
}

function normalizeImageCount(value: string) {
    return String(Math.max(1, Math.min(15, Math.floor(Math.abs(Number(value)) || 3))));
}
