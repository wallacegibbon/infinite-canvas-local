import { useEffect, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { Settings2 } from "lucide-react";

import { useTranslation } from "react-i18next";

import { ImageSettingsPanel, OptionPill, SettingTitle, imageSizeLabel } from "@/components/image-settings-panel";
import { Button } from "@/components/kit/button";
import { Switch } from "@/components/kit/choice";
import i18n from "@/i18n";
import { canvasThemes } from "@/lib/canvas-theme";
import { useCanvasTheme } from "@/lib/canvas/use-canvas-theme";
import { gatewayVariantLabel } from "@/lib/gateway-labels";
import { channelModelOf } from "@/stores/use-config-store";
import type { AiConfig } from "@/stores/use-config-store";

type CanvasImageSettingsPopoverProps = {
    config: AiConfig;
    onConfigChange: (key: keyof AiConfig, value: string) => void;
    onParamsChange?: (params: Record<string, unknown>) => void;
    onMissingConfig?: () => void;
    onOpenChange?: (open: boolean) => void;
    onOpenMaskEditor?: () => void;
    onOpenOutpaintEditor?: () => void;
    onOpenMossLayoutEditor?: () => void;
    /**
     * Whether this node has image references connected. A conditioned run is one answer per press
     * (docs/decisions.md #79), so the trigger stops promising a count that will not be used.
     */
    hasReferences?: boolean;
    buttonClassName?: string;
    getPopupContainer?: (triggerNode: HTMLElement) => HTMLElement;
    placement?: "topLeft" | "top" | "topRight" | "bottomLeft" | "bottom" | "bottomRight";
    autoAdjustOverflow?: boolean;
};

// Variant labels arrive from the gateway in English, because registry.json is data
// (docs/decisions.md #8). `gatewayVariantLabel` answers with the Chinese one from the
// dictionary when the UI is Chinese and hands back the gateway text — or the bare mode id —
// otherwise, which is why the four strings are not repeated here.
const qwenVariantLabel = (mode: string, labels: Record<string, string> | undefined) => labels?.[mode] || gatewayVariantLabel(mode, mode);
const editRatioOptions = ["1:1", "4:3", "3:4", "16:9", "9:16", "3:2", "2:3"] as const;

function qwenCanvasSize(resolution: string | number, ratio: string) {
    const value = Number(resolution);
    const area = value > 0 ? value : 1024;
    const [width, height] = ratio.split(":").map(Number);
    if (!width || !height) return "";
    const snap = (number: number) => Math.max(256, Math.round(number / 32) * 32);
    return `${snap(Math.sqrt(area * area * (width / height)))}x${snap(Math.sqrt(area * area * (height / width)))}`;
}

/**
 * Which ratio pill the stored size came from, or "" for a size no pill wrote.
 *
 * The selected ratio used to be saved as its own param, `editCanvasRatio`, and travelled to the gateway
 * with every request — where it is not a workflow input, so the run was refused. Only `editCanvasSize` is
 * the workflow's, and the pill can be read back through the same table that wrote it.
 */
function canvasRatioOf(size: string, resolution: string) {
    if (!size) return "auto";
    for (const ratio of editRatioOptions) {
        if (qwenCanvasSize(resolution, ratio) === size) return ratio;
    }
    return "";
}

function mossModelInfo(config: AiConfig) {
    const workflowId = channelModelOf(config, config.model)?.model.workflowId || "";
    if (!workflowId.startsWith("image-moss-sft-")) return null;
    return { label: i18n.t(workflowId.includes("50step") ? "canvas.imagePopover.moss50" : "canvas.imagePopover.moss8") };
}

function qwenModelInfo(config: AiConfig) {
    const model = channelModelOf(config, config.model)?.model;
    if (model?.workflowId !== "image-qwen21") return null;
    const modes = model.variantModes?.length ? model.variantModes : ["t2i", "edit", "inpaint", "outpaint"];
    // Modes whose own nodes are missing are shown but refused: choosing inpaint without the inpaint
    // node pack used to fail inside ComfyUI, after the mask editor had already opened.
    return { modes, labels: model.variantLabels, blocked: model.blockedModes || [] };
}

export function CanvasImageSettingsPopover({
    config,
    onConfigChange,
    onParamsChange,
    onOpenChange,
    onOpenMaskEditor,
    onOpenOutpaintEditor,
    onOpenMossLayoutEditor,
    hasReferences = false,
    buttonClassName,
    placement = "topLeft",
}: CanvasImageSettingsPopoverProps) {
    const { t } = useTranslation();
    const theme = useCanvasTheme();
    const buttonRef = useRef<HTMLSpanElement>(null);
    const panelRef = useRef<HTMLDivElement>(null);
    const [open, setOpen] = useState(false);
    const [buttonRect, setButtonRect] = useState<DOMRect | null>(null);
    const count = Math.max(1, Math.min(15, Math.floor(Math.abs(Number(config.count)) || 1)));
    const activeSize = config.size || "auto";
    const qwen = qwenModelInfo(config);
    const editCanvasRatio = canvasRatioOf(String((config.params as Record<string, unknown> | undefined)?.editCanvasSize || ""), String((config.params as Record<string, unknown> | undefined)?.resolution ?? 1024));
    // A stored imageMode can outlive the model it was chosen for (switching to Qwen while the
    // config still says "layout"), so an unknown mode reads as plain text-to-image.
    const imageMode = qwen && qwen.modes.includes(String(config.imageMode)) && !qwen.blocked.includes(String(config.imageMode)) ? String(config.imageMode) : "t2i";
    // The trigger reads `mode · scope`: the mode name, then what the canvas will follow.
    const qwenTriggerLabel = !qwen
        ? ""
        : `${qwenVariantLabel(imageMode, qwen.labels)}${imageMode === "t2i" ? "" : imageMode === "edit" ? ` · ${!editCanvasRatio || editCanvasRatio === "auto" ? t("canvas.imagePopover.followFirst") : editCanvasRatio}` : ` · ${t("canvas.imagePopover.followSource")}`}`;
    const updateOpen = (nextOpen: boolean) => {
        setOpen(nextOpen);
        onOpenChange?.(nextOpen);
    };

    useEffect(() => {
        if (!open) return;
        const syncPosition = () => setButtonRect(buttonRef.current?.getBoundingClientRect() || null);
        const closeOnOutsidePointer = (event: PointerEvent) => {
            const target = event.target;
            if (!(target instanceof Node)) return;
            if (buttonRef.current?.contains(target) || panelRef.current?.contains(target)) return;
            if (document.activeElement instanceof HTMLElement && panelRef.current?.contains(document.activeElement)) document.activeElement.blur();
            setOpen(false);
            onOpenChange?.(false);
        };

        syncPosition();
        window.addEventListener("resize", syncPosition);
        window.addEventListener("scroll", syncPosition, true);
        window.addEventListener("pointerdown", closeOnOutsidePointer, true);
        return () => {
            window.removeEventListener("resize", syncPosition);
            window.removeEventListener("scroll", syncPosition, true);
            window.removeEventListener("pointerdown", closeOnOutsidePointer, true);
        };
    }, [onOpenChange, open]);

    const panel =
        open && buttonRect ? (
            <ImageSettingsPortal
                buttonRect={buttonRect}
                panelRef={panelRef}
                placement={placement}
                theme={theme}
                config={config}
                onConfigChange={onConfigChange}
                onParamsChange={onParamsChange}
                onOpenMaskEditor={onOpenMaskEditor}
                onOpenOutpaintEditor={onOpenOutpaintEditor}
                onOpenMossLayoutEditor={onOpenMossLayoutEditor}
                onClose={() => updateOpen(false)}
            />
        ) : null;

    return (
        <>
            <span ref={buttonRef} className="inline-flex min-w-0">
                <Button
                    size="small"
                    type="text"
                    className={buttonClassName || "!h-8 !max-w-[180px] !justify-start !rounded-full !px-2.5"}
                    style={{ background: theme.node.fill, color: theme.node.text }}
                    icon={<Settings2 className="size-3.5" />}
                    onClick={() => updateOpen(!open)}
                >
                    <span className="truncate">{qwen ? qwenTriggerLabel : hasReferences ? imageSizeLabel(activeSize) : `${imageSizeLabel(activeSize)} · ${t("canvas.controls.images", { count })}`}</span>
                </Button>
            </span>
            {panel}
        </>
    );
}

function ImageSettingsPortal({
    buttonRect,
    panelRef,
    placement,
    theme,
    config,
    onConfigChange,
    onParamsChange,
    onOpenMaskEditor,
    onOpenOutpaintEditor,
    onOpenMossLayoutEditor,
    onClose,
}: {
    buttonRect: DOMRect;
    panelRef: RefObject<HTMLDivElement | null>;
    placement: CanvasImageSettingsPopoverProps["placement"];
    theme: (typeof canvasThemes)[keyof typeof canvasThemes];
    config: AiConfig;
    onConfigChange: (key: keyof AiConfig, value: string) => void;
    onParamsChange?: (params: Record<string, unknown>) => void;
    onOpenMaskEditor?: () => void;
    onOpenOutpaintEditor?: () => void;
    onOpenMossLayoutEditor?: () => void;
    onClose: () => void;
}) {
    const { t } = useTranslation();
    const qwen = qwenModelInfo(config);
    const moss = mossModelInfo(config);
    const qwenMode = String(config.imageMode || "t2i");
    const modeHints: Record<string, string> = {
        t2i: t("canvas.imagePopover.hints.t2i"),
        edit: t("canvas.imagePopover.hints.edit"),
        inpaint: t("canvas.imagePopover.hints.inpaint"),
        outpaint: t("canvas.imagePopover.hints.outpaint"),
    };
    const refResolution = String((config.params as Record<string, unknown> | undefined)?.resolution ?? 1024);
    const editCanvasRatio = canvasRatioOf(String((config.params as Record<string, unknown> | undefined)?.editCanvasSize || ""), refResolution);
    const width = 356;
    const gap = 8;
    const margin = 12;
    const alignRight = placement?.endsWith("Right");
    const alignCenter = placement === "top" || placement === "bottom";
    const left = alignCenter ? buttonRect.left + buttonRect.width / 2 - width / 2 : alignRight ? buttonRect.right - width : buttonRect.left;
    const topPlacement = placement?.startsWith("top");
    const style = {
        position: "fixed",
        zIndex: 1200,
        width,
        left: Math.max(margin, Math.min(window.innerWidth - width - margin, left)),
        ...(topPlacement ? { bottom: window.innerHeight - buttonRect.top + gap, maxHeight: Math.max(260, buttonRect.top - margin * 2) } : { top: buttonRect.bottom + gap, maxHeight: Math.max(260, window.innerHeight - buttonRect.bottom - margin * 2) }),
        background: theme.toolbar.panel,
        borderRadius: 18,
        boxShadow: "0 18px 54px rgba(28, 25, 23, 0.16)",
        padding: 18,
        overflowY: "auto",
        color: theme.node.text,
    } as const;

    return createPortal(
        <div ref={panelRef} className="canvas-image-settings-popover" style={style} onPointerDown={(event) => event.stopPropagation()} onMouseDown={(event) => event.stopPropagation()} onClick={(event) => event.stopPropagation()}>
            {qwen ? (
                <div className="space-y-4">
                    <div className="text-lg font-semibold">Qwen Image 2.1</div>
                    <div className="space-y-2.5">
                        <SettingTitle color={theme.node.muted}>{t("canvas.imagePopover.mode")}</SettingTitle>
                        <div className="grid grid-cols-2 gap-2.5">
                            {["t2i", "edit", "inpaint", "outpaint"]
                                .filter((mode) => qwen.modes.includes(mode))
                                .map((mode) => (
                                    <OptionPill
                                        key={mode}
                                        selected={qwenMode === mode}
                                        theme={theme}
                                        disabled={qwen.blocked.includes(mode)}
                                        onClick={() => {
                                            onConfigChange("imageMode", mode);
                                            if (mode === "inpaint" || mode === "outpaint") onClose();
                                            if (mode === "inpaint") onOpenMaskEditor?.();
                                            if (mode === "outpaint") onOpenOutpaintEditor?.();
                                        }}
                                    >
                                        {qwenVariantLabel(mode, qwen.labels)}
                                        {qwen.blocked.includes(mode) ? `（${t("canvas.imagePopover.modeBlocked")}）` : ""}
                                    </OptionPill>
                                ))}
                        </div>
                        <div className="text-xs leading-4" style={{ color: theme.node.muted }}>
                            {modeHints[qwenMode] || modeHints.t2i}
                        </div>
                        {qwenMode === "t2i" ? (
                            <ImageSettingsPanel config={config} onConfigChange={(key, value) => onConfigChange(key, value)} theme={theme} className="space-y-4" maxCount={10} />
                        ) : qwenMode === "edit" ? (
                            <div className="space-y-3">
                                <div className="space-y-2 rounded-xl border p-3" style={{ borderColor: theme.node.stroke }}>
                                    <SettingTitle color={theme.node.muted}>{t("canvas.imagePopover.canvasRatio")}</SettingTitle>
                                    <div className="grid grid-cols-3 gap-2">
                                        <OptionPill selected={editCanvasRatio === "auto"} theme={theme} onClick={() => onParamsChange?.({ editCanvasSize: "" })}>
                                            {t("canvas.imagePopover.followFirst")}
                                        </OptionPill>
                                        {editRatioOptions.map((ratio) => (
                                            <OptionPill key={ratio} selected={editCanvasRatio === ratio} theme={theme} onClick={() => onParamsChange?.({ editCanvasSize: qwenCanvasSize(refResolution, ratio) })}>
                                                {ratio}
                                            </OptionPill>
                                        ))}
                                    </div>
                                    <div className="text-xs leading-4" style={{ color: theme.node.muted }}>
                                        {t("canvas.imagePopover.editRatioHint")}
                                    </div>
                                </div>
                                <div className="space-y-2 rounded-xl border p-3" style={{ borderColor: theme.node.stroke }}>
                                    <SettingTitle color={theme.node.muted}>{t("canvas.imagePopover.referenceResolution")}</SettingTitle>
                                    <div className="grid grid-cols-4 gap-2">
                                        {[
                                            ["1024", "1k"],
                                            ["1536", "1.5k"],
                                            ["2048", "2k"],
                                            ["0", t("canvas.imagePopover.resolutionOriginal")],
                                        ].map(([value, label]) => (
                                            <OptionPill
                                                key={value}
                                                selected={refResolution === value}
                                                theme={theme}
                                                onClick={() => onParamsChange?.({ resolution: Number(value), ...(editCanvasRatio && editCanvasRatio !== "auto" ? { editCanvasSize: qwenCanvasSize(value, editCanvasRatio) } : {}) })}
                                            >
                                                {label}
                                            </OptionPill>
                                        ))}
                                    </div>
                                    <div className="text-xs leading-4" style={{ color: theme.node.muted }}>
                                        {t("canvas.imagePopover.resolutionHint")}
                                    </div>
                                </div>
                                <ImageSettingsPanel config={config} onConfigChange={(key, value) => onConfigChange(key, value)} theme={theme} className="space-y-4" maxCount={10} showSize={false} />
                            </div>
                        ) : (
                            <div className="space-y-2 rounded-xl border p-3" style={{ borderColor: theme.node.stroke }}>
                                <div className="flex items-center justify-between gap-3">
                                    <SettingTitle color={theme.node.muted}>{t("canvas.imagePopover.transparent")}</SettingTitle>
                                    <Switch size="small" checked={config.background === "transparent"} onChange={(checked) => onConfigChange("background", checked ? "transparent" : "")} />
                                </div>
                                <div className="text-xs leading-4" style={{ color: theme.node.muted }}>
                                    {t("canvas.imagePopover.transparentHint")}
                                </div>
                            </div>
                        )}
                    </div>
                </div>
            ) : (
                <div className="space-y-4">
                    {moss && onOpenMossLayoutEditor ? (
                        <div className="space-y-2.5 rounded-xl border p-3" style={{ borderColor: theme.node.stroke }}>
                            <SettingTitle color={theme.node.muted}>{t("canvas.imagePopover.layout")}</SettingTitle>
                            {config.imageMode === "layout" && config.layout ? (
                                <>
                                    <button
                                        type="button"
                                        className="h-9 w-full cursor-pointer rounded-full border text-sm transition hover:opacity-80"
                                        style={{ background: "transparent", borderColor: theme.node.text, color: theme.node.text }}
                                        onClick={() => {
                                            onClose();
                                            onOpenMossLayoutEditor();
                                        }}
                                    >
                                        {t("canvas.imagePopover.editLayout", { count: config.layout.regions.length })}
                                    </button>
                                    <button
                                        type="button"
                                        className="h-8 w-full cursor-pointer rounded-full border text-xs transition hover:opacity-80"
                                        style={{ background: "transparent", borderColor: theme.node.stroke, color: theme.node.muted }}
                                        onClick={() => onConfigChange("imageMode", "edit")}
                                    >
                                        {t("canvas.imagePopover.usePlainMode")}
                                    </button>
                                </>
                            ) : (
                                <button
                                    type="button"
                                    className="h-9 w-full cursor-pointer rounded-full border text-sm transition hover:opacity-80"
                                    style={{ background: "transparent", borderColor: theme.node.text, color: theme.node.text }}
                                    onClick={() => {
                                        onConfigChange("imageMode", "layout");
                                        onClose();
                                        onOpenMossLayoutEditor();
                                    }}
                                >
                                    {t("canvas.imagePopover.openLayoutEditor")}
                                </button>
                            )}
                            <div className="text-xs leading-4" style={{ color: theme.node.muted }}>
                                {moss.label} · {t("canvas.imagePopover.layoutHint")}
                            </div>
                        </div>
                    ) : null}
                    <ImageSettingsPanel config={config} onConfigChange={(key, value) => onConfigChange(key, value)} theme={theme} className="space-y-4" maxCount={10} />
                </div>
            )}
        </div>,
        document.body,
    );
}
