import { useEffect } from "react";
import { useNavigate } from "react-router-dom";

import { useConfigStore } from "@/stores/use-config-store";
import { useCanvasPanelStore } from "@/stores/canvas/use-canvas-panel-store";

/**
 * `/config` is not a page any more. Settings are a panel over the canvas, reached from the
 * gear in the canvas toolbar — the same `AppConfigPanel` the old `/config` route wrapped in
 * a 19-line shell. The address stays so a bookmark or a pasted link still works: it opens
 * the panel and puts you back on a canvas.
 */
export function AssetsEntry() {
    const navigate = useNavigate();
    const setAssetsOpen = useCanvasPanelStore((state) => state.setAssetsOpen);

    useEffect(() => {
        setAssetsOpen(true);
        navigate("/", { replace: true });
    }, [navigate, setAssetsOpen]);

    return null;
}

export function ConfigEntry() {
    const navigate = useNavigate();
    const openConfigDialog = useConfigStore((state) => state.openConfigDialog);

    useEffect(() => {
        openConfigDialog(false);
        navigate("/", { replace: true });
    }, [navigate, openConfigDialog]);

    return null;
}
