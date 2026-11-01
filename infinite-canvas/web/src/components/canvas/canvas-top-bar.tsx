import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Check, ChevronDown, Images, LayoutGrid, PanelLeftClose, PanelLeftOpen, Pencil, Plus, Trash2 } from "lucide-react";

import { useTranslation } from "react-i18next";

import { Dialog } from "@/components/kit/dialog";
import { Dropdown } from "@/components/kit/floating";
import { Tooltip } from "@/components/kit/tooltip";
import { UserStatusActions } from "@/components/layout/user-status-actions";
import { CanvasSystemStatus } from "@/components/canvas/canvas-system-status";
import { canvasThemes } from "@/lib/canvas-theme";
import { useCanvasStore } from "@/stores/canvas/use-canvas-store";
import { useCanvasPanelStore } from "@/stores/canvas/use-canvas-panel-store";
import { useCanvasSidePanelStore } from "@/stores/canvas/use-canvas-side-panel-store";
import { useThemeStore } from "@/stores/use-theme-store";

export function CanvasTopBar({
    title,
    titleDraft,
    isTitleEditing,
    onTitleDraftChange,
    onStartTitleEditing,
    onFinishTitleEditing,
    onCancelTitleEditing,
    canUndo,
    canRedo,
    onSwitchProject,
    onCreateProject,
    onDeleteProject,
    onOpenPlugins,
}: {
    title: string;
    titleDraft: string;
    isTitleEditing: boolean;
    onTitleDraftChange: (value: string) => void;
    onStartTitleEditing: () => void;
    onFinishTitleEditing: () => void;
    onCancelTitleEditing: () => void;
    canUndo: boolean;
    canRedo: boolean;
    onSwitchProject: (id: string) => void;
    onCreateProject: () => void;
    onDeleteProject: () => void;
    onOpenPlugins: () => void;
}) {
    const colorTheme = useThemeStore((state) => state.theme);
    const { t } = useTranslation();
    const theme = canvasThemes[colorTheme];
    const titleRef = useRef<HTMLDivElement>(null);
    const [shortcutsOpen, setShortcutsOpen] = useState(false);
    const sidePanelOpen = useCanvasSidePanelStore((state) => state.panelOpen);
    const toggleSidePanel = useCanvasSidePanelStore((state) => state.togglePanel);
    const projects = useCanvasStore((state) => state.projects);
    // The list and the asset library are panels over the canvas, not routes.
    const openManager = useCanvasPanelStore((state) => state.setCanvasManagerOpen);
    const openAssets = useCanvasPanelStore((state) => state.setAssetsOpen);
    // Current canvas first, the rest most-recent-first: switching is a "where am I" question,
    // so the answer you are most likely looking for should not be buried.
    const orderedProjects = useMemo(() => {
        const current = projects.find((project) => project.title === title);
        const rest = projects.filter((project) => project !== current).sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
        return current ? [current, ...rest] : rest;
    }, [projects, title]);

    useEffect(() => {
        if (!isTitleEditing) return;
        const close = (event: PointerEvent) => {
            if (!titleRef.current?.contains(event.target as Node)) onFinishTitleEditing();
        };
        document.addEventListener("pointerdown", close, true);
        return () => document.removeEventListener("pointerdown", close, true);
    }, [isTitleEditing, onFinishTitleEditing]);

    return (
        <>
            <div className="pointer-events-none absolute left-0 right-0 top-0 z-50 flex h-16 items-center justify-between pl-1 pr-4">
                <div className="pointer-events-auto flex min-w-0 items-center gap-2">
                    <TopBarButton label={sidePanelOpen ? t("canvas.collapsePanel") : t("canvas.expandPanel")} onClick={toggleSidePanel}>
                        {sidePanelOpen ? <PanelLeftClose className="size-4" /> : <PanelLeftOpen className="size-4" />}
                    </TopBarButton>
                    <TopBarButton label={t("navigation.assets")} onClick={() => openAssets(true)}>
                        <Images className="size-4" />
                    </TopBarButton>
                    <TopBarButton label={t("canvas.projects")} onClick={() => openManager(true)}>
                        <LayoutGrid className="size-4" />
                    </TopBarButton>

                    <div ref={titleRef} className="flex min-w-0 items-center gap-2">
                        {isTitleEditing ? (
                            <input
                                autoFocus
                                value={titleDraft}
                                onChange={(event) => onTitleDraftChange(event.target.value)}
                                onBlur={onFinishTitleEditing}
                                onKeyDown={(event) => {
                                    if (event.key === "Enter") onFinishTitleEditing();
                                    if (event.key === "Escape") onCancelTitleEditing();
                                }}
                                className="max-w-[280px] bg-transparent p-0 text-left text-lg font-semibold tracking-normal outline-none"
                                style={{ color: theme.node.text }}
                            />
                        ) : (
                            <Dropdown
                                className="max-h-[60vh] overflow-y-auto"
                                menu={{
                                    items: [
                                        ...orderedProjects.map((project) => ({
                                            key: project.id,
                                            icon: project.title === title ? <Check className="size-4" /> : undefined,
                                            label: (
                                                <span className="flex min-w-0 items-center justify-between gap-4">
                                                    <span className="max-w-[200px] truncate">{project.title}</span>
                                                    <span className="shrink-0 text-xs opacity-45">{t("canvas.node.nodeCount", { count: project.nodes.length })}</span>
                                                </span>
                                            ),
                                            onClick: () => onSwitchProject(project.id),
                                        })),
                                        { type: "divider" as const },
                                        { key: "rename", icon: <Pencil className="size-4" />, label: t("canvas.project.rename"), onClick: onStartTitleEditing },
                                        { key: "new", icon: <Plus className="size-4" />, label: t("canvas.create"), onClick: onCreateProject },
                                        { key: "delete", danger: true, icon: <Trash2 className="size-4" />, label: t("canvas.deleteCurrent"), onClick: onDeleteProject },
                                    ],
                                }}
                            >
                                <button
                                    type="button"
                                    className="flex max-w-[280px] items-center gap-1.5 rounded-md border-b border-dashed border-transparent text-left text-lg font-semibold tracking-normal transition hover:border-current"
                                    title={t("canvas.switchProject")}
                                    aria-label={t("canvas.switchProject")}
                                >
                                    <span className="truncate">{title}</span>
                                    <ChevronDown className="size-4 shrink-0 opacity-50" />
                                </button>
                            </Dropdown>
                        )}
                    </div>
                </div>

                <div className="pointer-events-auto flex items-center gap-1.5">
                    <CanvasSystemStatus />
                    <UserStatusActions variant="canvas" onOpenShortcuts={() => setShortcutsOpen(true)} onOpenPlugins={onOpenPlugins} />
                </div>
            </div>
            <Dialog title={t("canvas.shortcuts")} open={shortcutsOpen} onClose={() => setShortcutsOpen(false)} size="form">
                <div className="space-y-2 border-t pt-4 text-sm" style={{ borderColor: theme.node.stroke }}>
                    <Shortcut keys={["Ctrl / Space", t("canvas.shortcut.drag")]} value={t("canvas.shortcut.toggleTool")} />
                    <Shortcut keys={[t("canvas.shortcut.wheel")]} value={t("canvas.shortcut.zoom")} />
                    <Shortcut keys={[t("canvas.shortcut.zoomSlider")]} value={t("canvas.shortcut.preciseZoom")} />
                    <Shortcut keys={[t("canvas.shortcut.drag")]} value={t("canvas.shortcut.boxSelect")} />
                    <Shortcut keys={["Shift / Cmd", t("canvas.shortcut.click")]} value={t("canvas.shortcut.addSelection")} />
                    <Shortcut keys={["Ctrl / Cmd", "A"]} value={t("canvas.shortcut.selectAll")} />
                    <Shortcut keys={["Ctrl / Cmd", "C / V"]} value={t("canvas.shortcut.copyPaste")} />
                    <Shortcut keys={["Ctrl / Cmd", "G"]} value={t("canvas.shortcut.group")} />
                    <Shortcut keys={["Ctrl / Cmd", "Shift", "G"]} value={t("canvas.shortcut.ungroup")} />
                    <Shortcut keys={["Ctrl / Cmd", "Z"]} value={t("canvas.undo")} />
                    <Shortcut keys={["Ctrl / Cmd", "Shift", "Z"]} value={t("canvas.redo")} />
                    <Shortcut keys={["Ctrl / Cmd", "Y"]} value={t("canvas.redo")} />
                    <Shortcut keys={["Delete / Backspace"]} value={t("canvas.shortcut.delete")} />
                    <Shortcut keys={["Esc"]} value={t("canvas.shortcut.escape")} />
                    <Shortcut keys={[t("canvas.shortcut.dropMedia")]} value={t("canvas.shortcut.upload")} />
                </div>
            </Dialog>
        </>
    );
}

function TopBarButton({ label, onClick, children }: { label: string; onClick: () => void; children: ReactNode }) {
    const theme = canvasThemes[useThemeStore((state) => state.theme)];

    return (
        <Tooltip title={label}>
            <button type="button" onClick={onClick} aria-label={label} className="grid size-7 place-items-center rounded-full transition hover:bg-black/5 dark:hover:bg-white/10" style={{ color: theme.node.text }}>
                {children}
            </button>
        </Tooltip>
    );
}

function Shortcut({ keys, value }: { keys: string[]; value: string }) {
    return (
        <div className="grid grid-cols-[minmax(0,1fr)_120px] items-center gap-6 rounded-lg px-1 py-1.5">
            <span className="flex min-w-0 flex-wrap items-center gap-1.5">
                {keys.map((key, index) => (
                    <span key={`${key}-${index}`} className="flex items-center gap-1.5">
                        {index ? <span className="text-xs opacity-35">+</span> : null}
                        <kbd
                            className="min-w-9 rounded-md border px-2.5 py-1.5 text-center text-xs font-medium leading-none shadow-[inset_0_-1px_0_rgba(0,0,0,.08),0_1px_2px_rgba(0,0,0,.06)]"
                            style={{ borderColor: "rgba(120,113,108,.28)", background: "linear-gradient(#fff, rgba(245,245,244,.92))", color: "rgb(68,64,60)" }}
                        >
                            {key}
                        </kbd>
                    </span>
                ))}
            </span>
            <span className="text-right text-sm opacity-55">{value}</span>
        </div>
    );
}
