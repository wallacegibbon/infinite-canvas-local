import { useEffect, useRef, useState } from "react";
import { Navigate } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { useCanvasStore } from "@/stores/canvas/use-canvas-store";

/**
 * `/` is not a page, it is the way in.
 *
 * This renders nothing: it picks a canvas and replaces the URL with that canvas's own
 * address, so the app has no landing screen to sit on. The choice, in order:
 *
 *   1. the canvas you were last inside — read from a stored id rather than inferred from
 *      `updatedAt`, because opening a canvas to look at it does not change `updatedAt`
 *      and the most recently *modified* canvas is often not the one you left;
 *   2. otherwise the most recently modified one, for when that memory is stale;
 *   3. otherwise a new canvas, which is the first-run case.
 *
 * `replace` matters: without it the history keeps `/`, and every Back press bounces
 * through the redirect again.
 */
export function CanvasEntry() {
    const { t } = useTranslation();
    const hydrated = useCanvasStore((state) => state.hydrated);
    const lastProjectId = useCanvasStore((state) => state.lastProjectId);
    const [targetId, setTargetId] = useState("");
    const resolved = useRef(false);

    useEffect(() => {
        if (!hydrated || resolved.current) return;
        resolved.current = true;

        // Read the list at this instant; `projects` is not a dependency on purpose, since
        // creating the fallback canvas changes it and would re-enter this effect.
        const { projects, createProject } = useCanvasStore.getState();
        const remembered = projects.find((project) => project.id === lastProjectId);
        const newest = projects.reduce<{ id: string; updatedAt: string } | null>((best, project) => (!best || project.updatedAt > best.updatedAt ? project : best), null);
        setTargetId(remembered?.id || newest?.id || createProject(t("canvas.defaultTitle", { count: projects.length + 1 })));
    }, [hydrated, lastProjectId, t]);

    if (!targetId) return null;
    return <Navigate replace to={`/canvas/${targetId}`} />;
}
