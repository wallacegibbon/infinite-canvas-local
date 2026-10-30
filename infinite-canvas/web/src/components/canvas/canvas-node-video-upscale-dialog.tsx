import { useEffect, useState } from "react";
import { Button } from "antd";
import { Maximize2 } from "lucide-react";
import { useTranslation } from "react-i18next";


import { Dialog } from "@/components/kit/dialog";
import type { CanvasNodeData } from "@/types/canvas";
import { useCanvasTheme } from "@/lib/canvas/use-canvas-theme";

export type CanvasVideoUpscaleParams = { scale: 1.5 | 2; resolution: number; fps: number };

type Props = {
    open: boolean;
    node: CanvasNodeData | null;
    onClose: () => void;
    onConfirm: (params: CanvasVideoUpscaleParams) => void;
};

export function CanvasNodeVideoUpscaleDialog({ open, node, onClose, onConfirm }: Props) {
    const theme = useCanvasTheme();
    const { t } = useTranslation();
    const [scale, setScale] = useState<1.5 | 2>(1.5);
    const [sourceSize, setSourceSize] = useState({ width: 0, height: 0 });

    useEffect(() => {
        if (!open || !node) return;
        setScale(1.5);
        setSourceSize({
            width: node.metadata?.naturalWidth || node.width || 0,
            height: node.metadata?.naturalHeight || node.height || 0,
        });
    }, [node, open]);

    if (!node) return null;

    const sourceWidth = sourceSize.width;
    const sourceHeight = sourceSize.height;
    const hasVideo = Boolean(node.metadata?.content);
    const shortest = Math.min(sourceWidth, sourceHeight);
    const resolution = shortest ? Math.min(4096, Math.max(256, Math.round(shortest * scale))) : 1104;
    const outputWidth = sourceWidth ? Math.round(sourceWidth * scale) : 0;
    const outputHeight = sourceHeight ? Math.round(sourceHeight * scale) : 0;

    return (
        <Dialog
            open={open}
            onClose={onClose}
            width={560}
            title={
                <span className="flex items-center gap-2">
                    <Maximize2 className="size-4" />
                    {t("canvas.videoUpscale.title")}
                </span>
            }
        >
            <div className="space-y-4" style={{ color: theme.node.text }}>
                <div className="overflow-hidden rounded-2xl border bg-black" style={{ borderColor: theme.node.stroke }}>
                    {hasVideo ? (
                        <video
                            src={node.metadata?.content}
                            controls
                            playsInline
                            className="max-h-[260px] w-full bg-black object-contain"
                            onLoadedMetadata={(event) =>
                                setSourceSize({
                                    width: event.currentTarget.videoWidth || sourceWidth,
                                    height: event.currentTarget.videoHeight || sourceHeight,
                                })
                            }
                        />
                    ) : (
                        <div className="flex h-40 items-center justify-center text-xs" style={{ color: theme.node.muted }}>
                            {t("canvas.videoUpscale.emptySource")}
                        </div>
                    )}
                </div>

                <div className="text-xs" style={{ color: theme.node.muted }}>
                    {t("canvas.videoUpscale.sourceInfo", { width: sourceWidth || "?", height: sourceHeight || "?" })}
                </div>

                <div className="space-y-2">
                    <div className="text-xs font-medium" style={{ color: theme.node.muted }}>
                        {t("canvas.videoUpscale.scale")}
                    </div>
                    <div className="grid grid-cols-2 gap-2">
                        {([1.5, 2] as const).map((value) => (
                            <button
                                key={value}
                                type="button"
                                className="h-10 rounded-xl border text-sm font-medium transition"
                                style={{
                                    borderColor: scale === value ? theme.node.text : theme.node.stroke,
                                    background: scale === value ? `${theme.node.text}12` : "transparent",
                                    color: theme.node.text,
                                }}
                                onClick={() => setScale(value)}
                            >
                                {value}×
                            </button>
                        ))}
                    </div>
                </div>

                <div className="space-y-1 rounded-xl border p-3 text-xs" style={{ borderColor: theme.node.stroke, color: theme.node.muted }}>
                    <div>
                        {t("canvas.videoUpscale.targetShortEdge")} <span style={{ color: theme.node.text }}>{resolution}px</span>
                    </div>
                    <div>
                        {t("canvas.videoUpscale.expectedOutput")} <span style={{ color: theme.node.text }}>{outputWidth || "?"} × {outputHeight || "?"}</span>
                    </div>
                    <div>{t("canvas.videoUpscale.engineInfo")}</div>
                </div>

                <div className="text-[11px] leading-5" style={{ color: theme.node.muted }}>
                    {t("canvas.videoUpscale.timeHint")}
                </div>

                <div className="flex justify-end gap-2">
                    <Button onClick={onClose}>{t("common.cancel")}</Button>
                    <Button type="primary" disabled={!hasVideo} onClick={() => onConfirm({ scale, resolution, fps: 24 })}>
                        {t("canvas.videoUpscale.start")}
                    </Button>
                </div>
            </div>
        </Dialog>
    );
}
