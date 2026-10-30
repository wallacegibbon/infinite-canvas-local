import { useState, type CSSProperties, type ReactNode } from "react";

import { Dialog } from "@/components/kit/dialog";
import { cn } from "@/lib/utils";

/**
 * An image with an optional lightbox, replacing antd's `Image`.
 *
 * `preview` covers both shapes the call sites use: `{ src }` for a click-to-enlarge
 * thumbnail, and `{ visible, onVisibleChange }` for a **controlled** preview with no
 * visible trigger — the canvas composer uses that to open a preview from elsewhere in the
 * node. When `visible` is present the component is controlled and never opens itself.
 *
 * The lightbox is `kit/dialog`, so it inherits the canvas overlay attributes.
 */
type PreviewOptions = { src?: string; visible?: boolean; onVisibleChange?: (visible: boolean) => void };

export function Image({ src, alt, preview, className, style }: { src?: string; alt?: string; preview?: boolean | PreviewOptions; className?: string; style?: CSSProperties }): ReactNode {
    const [open, setOpen] = useState(false);
    const controlled = typeof preview === "object" && preview !== null && "visible" in preview;
    const visible = controlled ? Boolean((preview as PreviewOptions).visible) : open;
    const enlarged = (typeof preview === "object" && preview !== null ? preview.src : undefined) || src;

    const setVisible = (next: boolean) => {
        if (controlled) (preview as PreviewOptions).onVisibleChange?.(next);
        else setOpen(next);
    };

    return (
        <>
            {/* A controlled preview has no thumbnail of its own; the caller hides it and just
                needs the lightbox to exist. */}
            <img src={src} alt={alt} style={style} className={cn(!controlled && preview !== false && "cursor-zoom-in", className)} onClick={!controlled && preview !== false ? () => setVisible(true) : undefined} />
            {preview !== false ? (
                <Dialog open={visible} onClose={() => setVisible(false)} width="auto" bodyClassName="p-0" label={alt}>
                    {enlarged ? <img src={enlarged} alt={alt} className="max-h-[80vh] max-w-[80vw] object-contain" /> : null}
                </Dialog>
            ) : null}
        </>
    );
}
