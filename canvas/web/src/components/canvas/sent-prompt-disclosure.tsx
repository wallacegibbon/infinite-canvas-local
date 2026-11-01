import { useState } from "react";
import { useTranslation } from "react-i18next";

/**
 * What was actually sent, when it is not what the user typed.
 *
 * The canvas appends connected text (`【文本1】…`), numbers reference images and may prepend the
 * settings' system prompt. That composition is necessary — the request means nothing without it —
 * but it must not be silent, so the panel shows the exact string that went out.
 */
export function SentPromptDisclosure({ sent, current, className }: { sent?: string; current: string; className?: string }) {
    const { t } = useTranslation();
    const [open, setOpen] = useState(false);
    if (!sent || sent.trim() === current.trim()) return null;

    return (
        <div className={className || "mt-1.5 text-[11px] opacity-70"}>
            <button type="button" className="underline underline-offset-2" onClick={() => setOpen((value) => !value)}>
                {open ? t("canvas.promptPanel.sentPromptHide") : t("canvas.promptPanel.sentPrompt", { count: sent.length })}
            </button>
            {open ? <pre className="thin-scrollbar mt-1 max-h-40 overflow-auto whitespace-pre-wrap break-words rounded-md bg-black/10 p-2 font-mono text-[11px] leading-5">{sent}</pre> : null}
        </div>
    );
}
