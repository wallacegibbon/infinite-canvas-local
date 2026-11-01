import { Check, Download, Pencil, Trash2, X } from "lucide-react";
import { useNavigate } from "react-router-dom";

import { useTranslation } from "react-i18next";

import { Button } from "@/components/kit/button";
import { Input } from "@/components/kit/input";
import { useCanvasStore, type CanvasProject } from "@/stores/canvas/use-canvas-store";
import { useCanvasListStore } from "@/stores/canvas/use-canvas-list-store";
import { useCanvasPanelStore } from "@/stores/canvas/use-canvas-panel-store";
import { exportCanvasProjects } from "@/lib/canvas/canvas-export";

export function CanvasProjectCard({ project }: { project: CanvasProject }) {
    const { i18n, t } = useTranslation();
    const navigate = useNavigate();
    const renameProject = useCanvasStore((state) => state.renameProject);
    const selectedIds = useCanvasListStore((state) => state.selectedProjectIds);
    const editingId = useCanvasListStore((state) => state.editingProjectId);
    const editingTitle = useCanvasListStore((state) => state.editingProjectTitle);
    const startEditing = useCanvasListStore((state) => state.startEditingProject);
    const setEditingTitle = useCanvasListStore((state) => state.setEditingProjectTitle);
    const stopEditing = useCanvasListStore((state) => state.stopEditingProject);
    const toggleSelected = useCanvasListStore((state) => state.toggleSelectedProjectId);
    const setDeleteIds = useCanvasListStore((state) => state.setDeleteProjectIds);
    const setManagerOpen = useCanvasPanelStore((state) => state.setCanvasManagerOpen);
    const editing = editingId === project.id;
    const selected = selectedIds.includes(project.id);
    // No query passthrough any more: the manager is a panel, not a route, so there is no URL
    // to carry parameters through. Nothing ever built such a link anyway.
    const open = () => {
        setManagerOpen(false);
        navigate(`/canvas/${project.id}`);
    };
    const saveTitle = () => {
        renameProject(project.id, editingTitle);
        stopEditing();
    };

    return (
        <article className="group flex min-h-44 cursor-pointer flex-col justify-between rounded-2xl bg-[#f1eee8] p-5 transition hover:bg-[#ebe6dc] dark:bg-white/5 dark:hover:bg-white/10" onClick={() => !editing && open()}>
            <div className="flex items-start gap-3">
                <input
                    type="checkbox"
                    checked={selected}
                    onClick={(event) => event.stopPropagation()}
                    onChange={(event) => toggleSelected(project.id, event.target.checked)}
                    className="mt-1 size-4 accent-stone-950 dark:accent-stone-100"
                    aria-label={t("canvas.project.select", { name: project.title })}
                />
                {editing ? (
                    <Input className="min-w-0" value={editingTitle} onClick={(event) => event.stopPropagation()} onChange={(event) => setEditingTitle(event.target.value)} onKeyDown={(event) => event.key === "Enter" && saveTitle()} autoFocus />
                ) : (
                    <button
                        type="button"
                        className="min-w-0 cursor-pointer text-left"
                        onClick={(event) => {
                            event.stopPropagation();
                            open();
                        }}
                    >
                        <h2 className="truncate text-xl font-semibold">{project.title}</h2>
                        <p className="mt-3 text-sm leading-6 text-stone-600 dark:text-stone-400">{t("canvas.project.stats", { nodes: project.nodes.length, connections: project.connections.length })}</p>
                    </button>
                )}
            </div>
            <div className="mt-8 flex items-end justify-between gap-3">
                <p className="text-xs text-stone-500">{t("canvas.project.updated", { date: new Date(project.updatedAt).toLocaleString(i18n.resolvedLanguage, { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" }) })}</p>
                <div className="flex items-center gap-1" onClick={(event) => event.stopPropagation()}>
                    {editing ? (
                        <>
                            <Button type="text" size="small" shape="circle" icon={<Check className="size-4" />} onClick={saveTitle} aria-label={t("canvas.project.saveName")} />
                            <Button type="text" size="small" shape="circle" icon={<X className="size-4" />} onClick={stopEditing} aria-label={t("canvas.project.cancelRename")} />
                        </>
                    ) : (
                        <>
                            <Button type="text" size="small" shape="circle" icon={<Download className="size-4" />} onClick={() => void exportCanvasProjects([project], project.title || t("canvas.title"))} aria-label={t("canvas.project.export")} />
                            <Button type="text" size="small" shape="circle" icon={<Pencil className="size-4" />} onClick={() => startEditing(project.id, project.title)} aria-label={t("canvas.project.rename")} />
                            <Button type="text" size="small" shape="circle" icon={<Trash2 className="size-4" />} onClick={() => setDeleteIds([project.id])} aria-label={t("canvas.project.delete")} />
                        </>
                    )}
                </div>
            </div>
        </article>
    );
}
