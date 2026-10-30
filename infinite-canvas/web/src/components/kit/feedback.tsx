import { AlertTriangle, CheckCircle2, Info, XCircle } from "lucide-react";
import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * Status display, replacing antd's `Progress` and `Alert`.
 *
 * Both keep antd's prop names for the subset in use. `Progress` draws a single bar —
 * antd's circle and dashboard variants are not needed here — and `Alert` renders the
 * inline banner the storage panel uses when a read fails. `Alert` always shows its icon;
 * antd's `showIcon` was declared here and ignored, so it was deleted along with the one
 * call site that still passed it.
 */
type ProgressStatus = "normal" | "active" | "success" | "exception";

export function Progress({ percent = 0, showInfo = false, size = "middle", status = "normal", className }: { percent?: number; showInfo?: boolean; size?: "small" | "middle"; status?: ProgressStatus; className?: string }): ReactNode {
    const clamped = Math.max(0, Math.min(100, percent));
    const fill = status === "exception" ? "bg-red-500" : status === "success" ? "bg-emerald-500" : "bg-stone-900 dark:bg-stone-100";
    return (
        <div className={cn("flex items-center gap-2", className)}>
            <div role="progressbar" aria-valuenow={Math.round(clamped)} aria-valuemin={0} aria-valuemax={100} className={cn("flex-1 overflow-hidden rounded-full bg-stone-200 dark:bg-stone-800", size === "small" ? "h-1.5" : "h-2")}>
                <div className={cn("h-full rounded-full transition-[width] duration-300", fill)} style={{ width: `${clamped}%` }} />
            </div>
            {showInfo ? <span className="shrink-0 text-xs tabular-nums text-stone-500 dark:text-stone-400">{Math.round(clamped)}%</span> : null}
        </div>
    );
}

type AlertType = "info" | "success" | "warning" | "error";

const ALERT_STYLES: Record<AlertType, { box: string; icon: typeof Info }> = {
    info: { box: "border-blue-200 bg-blue-50 text-blue-800 dark:border-blue-500/30 dark:bg-blue-500/10 dark:text-blue-200", icon: Info },
    success: { box: "border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-200", icon: CheckCircle2 },
    warning: { box: "border-amber-200 bg-amber-50 text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200", icon: AlertTriangle },
    error: { box: "border-red-200 bg-red-50 text-red-800 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-200", icon: XCircle },
};

export function Alert({ type = "info", message, description, className }: { type?: AlertType; message?: ReactNode; description?: ReactNode; className?: string }): ReactNode {
    const style = ALERT_STYLES[type] ?? ALERT_STYLES.info;
    const Icon = style.icon;
    return (
        <div role="alert" className={cn("flex items-start gap-2 rounded-lg border px-3 py-2 text-sm", style.box, className)}>
            <Icon className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
            <div className="min-w-0">
                {message ? <div className="font-medium">{message}</div> : null}
                {description ? <div className="mt-0.5 text-xs opacity-90">{description}</div> : null}
            </div>
        </div>
    );
}
