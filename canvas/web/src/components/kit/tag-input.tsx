import { X } from "lucide-react";
import { useState, type KeyboardEvent, type ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * Free-text multi-value field, replacing antd's `<Select mode="tags">`.
 *
 * That was never a dropdown: there are no options to pick from, the control is a chip list
 * plus a text field. Rendering it through a listbox cost a portal, an aria listbox and a
 * separate search box, none of which did anything. Here it is the markup it always was.
 *
 * Same `value` / `onChange` contract as antd, so it still drops into a `Form.Item` and
 * still hands back `string[]`.
 */
const FIELD =
    "flex min-h-8 flex-wrap items-center gap-1 rounded-md border border-stone-300 bg-white px-1.5 py-1 transition-colors focus-within:border-stone-400 focus-within:ring-2 focus-within:ring-stone-400/20 dark:border-stone-700 dark:bg-stone-900 dark:focus-within:border-stone-600";

export function TagInput({
    value = [],
    onChange,
    placeholder,
    separators = [",", "，"],
    className,
}: {
    value?: string[];
    onChange?: (value: string[]) => void;
    placeholder?: string;
    /** Characters that commit the pending text, as antd's `tokenSeparators`. */
    separators?: string[];
    className?: string;
}): ReactNode {
    const [draft, setDraft] = useState("");

    // A separator may arrive in the middle of a paste, so commit every token in the draft
    // rather than assuming the keystroke ended it.
    const commit = (raw: string) => {
        const normalized = separators.reduce<string>((text, separator) => text.split(separator).join("\n"), raw);
        const added = normalized
            .split("\n")
            .map((part) => part.trim())
            .filter((part) => part && !value.includes(part));
        if (added.length) onChange?.([...value, ...added]);
        setDraft("");
    };

    const handleKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
        if (event.key === "Enter") {
            event.preventDefault();
            commit(draft);
            return;
        }
        if (event.key === "Backspace" && !draft && value.length) {
            onChange?.(value.slice(0, -1));
        }
    };

    return (
        <div className={cn(FIELD, className)} onClick={(event) => (event.currentTarget.querySelector("input") as HTMLInputElement | null)?.focus()}>
            {value.map((tag) => (
                <span key={tag} className="inline-flex items-center gap-1 rounded border border-stone-200 bg-stone-100 px-1.5 py-0.5 text-xs text-stone-700 dark:border-stone-700 dark:bg-stone-800 dark:text-stone-300">
                    {tag}
                    <button
                        type="button"
                        aria-label={`Remove ${tag}`}
                        onClick={(event) => {
                            event.stopPropagation();
                            onChange?.(value.filter((item) => item !== tag));
                        }}
                        className="text-stone-400 transition-colors hover:text-stone-700 dark:hover:text-stone-200"
                    >
                        <X className="size-3" />
                    </button>
                </span>
            ))}
            <input
                value={draft}
                placeholder={value.length ? undefined : placeholder}
                onChange={(event) => {
                    const next = event.target.value;
                    // Commit immediately when a separator was typed, so the chip appears as
                    // the character lands instead of on the next keystroke.
                    if (separators.some((separator) => next.includes(separator))) commit(next);
                    else setDraft(next);
                }}
                onKeyDown={handleKeyDown}
                onBlur={() => commit(draft)}
                className="min-w-16 flex-1 bg-transparent text-sm text-stone-900 outline-none placeholder:text-stone-400 dark:text-stone-100 dark:placeholder:text-stone-500"
            />
        </div>
    );
}
