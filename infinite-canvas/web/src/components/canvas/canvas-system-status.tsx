import { useCallback, useEffect, useState } from "react";

import { Gauge } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/kit/button";
import { Dialog } from "@/components/kit/dialog";
import { Spin } from "@/components/kit/display";
import { LOCAL_GATEWAY_BASE_URL } from "@/constant/app-mode";
import { canvasThemes } from "@/lib/canvas-theme";
import { useThemeStore } from "@/stores/use-theme-store";

type GpuStats = {
    index: number;
    name: string;
    memoryUsedMb: number;
    memoryTotalMb: number;
    utilization: number;
    temperature: number;
};

type SystemStats = {
    at: string;
    gpu: GpuStats[];
    gpuError?: string | null;
    ram: { totalBytes: number; usedBytes: number; freeBytes: number };
    comfy: { url: string; reachable: boolean };
    lastOOM: { at: string; message: string } | null;
};

type FreeResult = {
    ok: boolean;
    freedVramMb?: number;
    freedRamBytes?: number;
    durationMs?: number;
    error?: string;
};

const gatewayRoot = LOCAL_GATEWAY_BASE_URL.replace(/\/v1\/?$/, "");

function gb(bytes: number) {
    return (bytes / 1024 ** 3).toFixed(1);
}

function vramGb(mb: number) {
    return (mb / 1024).toFixed(1);
}

function percent(used: number, total: number) {
    return total > 0 ? Math.max(0, Math.min(100, (used / total) * 100)) : 0;
}

export function CanvasSystemStatus() {
    const { t } = useTranslation();
    const colorTheme = useThemeStore((state) => state.theme);
    const theme = canvasThemes[colorTheme];
    const [stats, setStats] = useState<SystemStats | null>(null);
    const [open, setOpen] = useState(false);
    const [freeing, setFreeing] = useState(false);
    const [freeResult, setFreeResult] = useState<FreeResult | null>(null);
    const [freeError, setFreeError] = useState("");

    const refresh = useCallback(async () => {
        try {
            const response = await fetch(`${gatewayRoot}/api/system/stats`);
            if (!response.ok) return;
            setStats(await response.json());
        } catch {
            /* keep the last data while the gateway is not ready */
        }
    }, []);

    useEffect(() => {
        void refresh();
        const timer = window.setInterval(() => {
            if (document.visibilityState !== "hidden") void refresh();
        }, 5000);
        return () => window.clearInterval(timer);
    }, [refresh]);

    const gpu = stats?.gpu?.[0];
    const vramPercent = gpu ? percent(gpu.memoryUsedMb, gpu.memoryTotalMb) : 0;
    const ramPercent = stats ? percent(stats.ram.usedBytes, stats.ram.totalBytes) : 0;
    const oom = stats?.lastOOM;

    const freeMemory = async () => {
        setFreeing(true);
        setFreeError("");
        setFreeResult(null);
        try {
            const response = await fetch(`${gatewayRoot}/api/system/free-memory`, { method: "POST" });
            const data = (await response.json().catch(() => ({}))) as FreeResult;
            if (!response.ok || data.ok === false) throw new Error(data.error || t("canvas.systemStatus.freeFailed"));
            setFreeResult(data);
            await refresh();
        } catch (error) {
            setFreeError(error instanceof Error ? error.message : String(error));
        } finally {
            setFreeing(false);
        }
    };

    const barStyle = (value: number) => ({ width: `${value}%`, background: value >= 90 ? "#dc2626" : theme.node.text });

    return (
        <>
            <button
                type="button"
                className="flex h-8 items-center gap-1.5 rounded-lg px-2 text-[11px] transition-colors hover:bg-black/5 dark:hover:bg-white/10"
                style={{ color: theme.node.muted, background: "transparent" }}
                title={t("canvas.systemStatus.tooltip")}
                onClick={() => setOpen(true)}
            >
                <Gauge className="size-3.5" />
                <span>
                    {gpu
                        ? t("canvas.systemStatus.vramShort", { used: vramGb(gpu.memoryUsedMb), total: vramGb(gpu.memoryTotalMb) })
                        : t("canvas.systemStatus.vramShortEmpty")}
                </span>
                <span className="hidden opacity-40 lg:inline">·</span>
                <span className="hidden lg:inline">
                    {t("canvas.systemStatus.ramShort", {
                        used: stats ? gb(stats.ram.usedBytes) : "--",
                        total: stats ? gb(stats.ram.totalBytes) : "--",
                    })}
                </span>
                {oom ? <span className="size-1.5 rounded-full" style={{ background: "#dc2626" }} title={t("canvas.systemStatus.oomBadge")} /> : null}
            </button>
            <Dialog title={t("canvas.systemStatus.title")} open={open} onClose={() => setOpen(false)} size="form">
                <div className="space-y-4 pt-1 text-sm" style={{ color: theme.node.text }}>
                    <div className="space-y-1.5">
                        <div className="flex items-center justify-between text-xs" style={{ color: theme.node.muted }}>
                            <span>{t("canvas.systemStatus.vramLabel")}</span>
                            <span>{gpu ? `${vramGb(gpu.memoryUsedMb)} / ${vramGb(gpu.memoryTotalMb)} GB` : "--"}</span>
                        </div>
                        <div className="h-1.5 overflow-hidden rounded-full" style={{ background: theme.node.fill }}>
                            <div className="h-full rounded-full transition-[width]" style={barStyle(vramPercent)} />
                        </div>
                        {gpu ? (
                            <div className="flex items-center justify-between text-[11px]" style={{ color: theme.node.faint }}>
                                <span className="truncate">{gpu.name}</span>
                                <span>{t("canvas.systemStatus.utilizationLine", { utilization: gpu.utilization, temperature: gpu.temperature })}</span>
                            </div>
                        ) : (
                            <div className="text-[11px]" style={{ color: theme.node.faint }}>
                                {stats?.gpuError ? t("canvas.systemStatus.gpuError", { error: stats.gpuError }) : t("canvas.systemStatus.gpuLoading")}
                            </div>
                        )}
                    </div>
                    <div className="space-y-1.5">
                        <div className="flex items-center justify-between text-xs" style={{ color: theme.node.muted }}>
                            <span>{t("canvas.systemStatus.ramLabel")}</span>
                            <span>{stats ? `${gb(stats.ram.usedBytes)} / ${gb(stats.ram.totalBytes)} GB` : "--"}</span>
                        </div>
                        <div className="h-1.5 overflow-hidden rounded-full" style={{ background: theme.node.fill }}>
                            <div className="h-full rounded-full transition-[width]" style={barStyle(ramPercent)} />
                        </div>
                    </div>
                    <div className="flex items-center justify-between text-xs" style={{ color: theme.node.muted }}>
                        <span>ComfyUI</span>
                        <span style={{ color: stats?.comfy.reachable ? theme.node.text : "#dc2626" }}>
                            {stats?.comfy.reachable ? t("canvas.systemStatus.online") : t("canvas.systemStatus.offline")}
                        </span>
                    </div>
                    {oom ? (
                        <div className="rounded-lg border px-3 py-2 text-[11px]" style={{ borderColor: "#fca5a5", color: "#b91c1c" }}>
                            {t("canvas.systemStatus.lastOom", { message: oom.message })}
                        </div>
                    ) : null}
                    <div className="border-t pt-3" style={{ borderColor: theme.node.stroke }}>
                        <div className="space-y-2">
                            <div className="flex items-start justify-between gap-3">
                                <span className="text-[11px]" style={{ color: theme.node.faint }}>
                                    {t("canvas.systemStatus.freeHint")}
                                </span>
                                <div className="flex shrink-0 items-center gap-2">
                                    <Button
                                        size="small"
                                        loading={freeing}
                                        disabled={!stats?.comfy.reachable || freeing}
                                        onClick={() => void freeMemory()}
                                        style={{ background: "#16a34a", borderColor: "#16a34a", color: "#fff" }}
                                    >
                                        {t("canvas.systemStatus.freeAction")}
                                    </Button>
                                </div>
                            </div>
                            {freeing ? (
                                <div className="flex items-center gap-2 text-[11px]" style={{ color: theme.node.muted }}>
                                    <Spin size="small" />
                                    <span>{t("canvas.systemStatus.freeing")}</span>
                                </div>
                            ) : null}
                            {freeResult?.ok ? (
                                <div className="rounded-lg border px-3 py-2 text-[11px]" style={{ borderColor: theme.node.stroke, color: theme.node.muted }}>
                                    {t("canvas.systemStatus.freeDone", {
                                        vram: freeResult.freedVramMb || 0,
                                        ram: gb(freeResult.freedRamBytes || 0),
                                        seconds: Math.round((freeResult.durationMs || 0) / 1000),
                                    })}
                                </div>
                            ) : null}
                            {freeError ? (
                                <div className="text-[11px]" style={{ color: "#b91c1c" }}>
                                    {freeError}
                                </div>
                            ) : null}
                        </div>
                    </div>
                </div>
            </Dialog>
        </>
    );
}
