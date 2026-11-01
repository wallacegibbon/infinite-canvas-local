import { ChevronDown, LoaderCircle } from "lucide-react";
import type { ReactNode } from "react";

import { SelectContent, SelectItem, SelectTrigger, SelectValue, Select as SelectRoot } from "@/components/kit/select-primitive";
import { cn } from "@/lib/utils";

/**
 * Single-value dropdown, replacing antd's `Select`.
 *
 * The prop shape is antd's (`value` / `onChange` / `options` / `placeholder` / `size` /
 * `loading` / `notFoundContent`), so call sites keep reading `onChange={(value) => …}` and
 * `options={[{ value, label }]}`. The implementation is the radix primitive already in
 * `kit/select-primitive`, which is what `model-picker` uses.
 *
 * Two antd features are not carried over, because radix covers them natively:
 *   - `showSearch` / `optionFilterProp` — the listbox has built-in typeahead, driven by
 *     `textValue`. Typing a prefix jumps to the matching option, no separate input.
 *   - `mode="tags"` — a multi-value free-text field, which is not a dropdown at all. It
 *     lives in `kit/tag-input` instead.
 *
 * Canvas coupling: the popup portal is marked `data-canvas-overlay`, so opening it over the
 * canvas stops zoom and deselect. That attribute is what lets `lib/canvas-overlay` drop its
 * `.ant-select-dropdown` constant.
 */
type SelectOption<T extends string = string> = { value: T; label?: ReactNode; disabled?: boolean };

type Size = "small" | "middle" | "large";

/**
 * Radix rejects `value=""`, which antd tolerated as "no selection". Everything travels
 * through this sentinel instead, so an empty value renders as the placeholder — which is
 * what antd did when no option carried `""`.
 */
const NONE = "\u0000none";

const HEIGHTS: Record<Size, string> = {
    small: "h-7 px-2 text-[13px]",
    middle: "h-8 px-2.5 text-sm",
    large: "h-10 px-3 text-base",
};

export function Select<T extends string = string>({
    value,
    onChange,
    options,
    placeholder,
    size = "middle",
    loading = false,
    notFoundContent,
    disabled = false,
    variant = "outlined",
    className,
}: {
    value?: T;
    onChange?: (value: T) => void;
    options: ReadonlyArray<SelectOption<T>>;
    placeholder?: ReactNode;
    size?: Size;
    loading?: boolean;
    notFoundContent?: ReactNode;
    disabled?: boolean;
    /** `borderless` drops the frame, for a control that sits in an already-framed row. */
    variant?: "outlined" | "borderless";
    className?: string;
}): ReactNode {
    const empty = !value || value === NONE;
    return (
        <SelectRoot value={empty ? NONE : value} disabled={disabled} onValueChange={(next) => onChange?.((next === NONE ? "" : next) as T)}>
            <SelectTrigger
                size={size === "small" ? "sm" : "default"}
                hideChevron
                className={cn(
                    "w-full justify-between gap-1.5 font-normal shadow-none transition-colors",
                    HEIGHTS[size],
                    variant === "borderless"
                        ? "border-transparent bg-transparent hover:bg-black/5 dark:hover:bg-white/10"
                        : "rounded-md border border-stone-300 bg-white text-stone-900 hover:border-stone-400 focus-visible:border-stone-400 focus-visible:ring-2 focus-visible:ring-stone-400/20 dark:border-stone-700 dark:bg-stone-900 dark:text-stone-100 dark:hover:border-stone-600",
                    className,
                )}
            >
                <SelectValue className="min-w-0 flex-1 truncate text-left" placeholder={<span className="text-stone-400 dark:text-stone-500">{placeholder}</span>} />
                {loading ? <LoaderCircle className="size-3.5 shrink-0 animate-spin text-stone-400" /> : <ChevronDown className="size-3.5 shrink-0 text-stone-400" />}
            </SelectTrigger>
            <SelectContent
                data-canvas-overlay
                position="popper"
                align="start"
                sideOffset={4}
                className="z-[1500] max-h-72 min-w-(--radix-select-trigger-width) rounded-lg border border-stone-200 bg-white p-1 shadow-xl dark:border-stone-700 dark:bg-stone-900"
            >
                {options.length ? (
                    options.map((option) => (
                        <SelectItem
                            key={option.value}
                            value={option.value === "" ? NONE : option.value}
                            textValue={typeof option.label === "string" ? option.label : option.value}
                            disabled={option.disabled}
                            className="rounded-md py-1.5 pr-7 pl-2 text-stone-800 focus:bg-stone-100 dark:text-stone-100 dark:focus:bg-stone-800"
                        >
                            {option.label ?? option.value}
                        </SelectItem>
                    ))
                ) : (
                    <SelectItem value={NONE} disabled className="text-stone-400 dark:text-stone-500">
                        {loading ? "" : (notFoundContent ?? "—")}
                    </SelectItem>
                )}
            </SelectContent>
        </SelectRoot>
    );
}
