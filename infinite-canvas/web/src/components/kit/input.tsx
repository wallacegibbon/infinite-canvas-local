import { Eye, EyeOff, Search as SearchIcon } from "lucide-react";
import { useEffect, useRef, useState, type InputHTMLAttributes, type ReactNode, type TextareaHTMLAttributes } from "react";

import { cn } from "@/lib/utils";

/**
 * Text entry, replacing antd's `Input` and its `TextArea` / `Password` / `Search` children,
 * plus `InputNumber` and `Slider`.
 *
 * Props keep antd's names where call sites use them (`size`, `prefix`, `status`,
 * `onPressEnter`, `allowClear`, `rows`, `autoSize`). `onChange` receives the native change
 * event, which is what the call sites already read (`event.target.value`).
 */
type Size = "small" | "middle" | "large";
type Status = "normal" | "error" | "warning";

const HEIGHTS: Record<Size, string> = {
    small: "h-6 text-[13px]",
    middle: "h-8 text-sm",
    large: "h-10 text-base",
};

const BORDERS: Record<Status, string> = {
    normal: "border-stone-300 focus:border-stone-400 dark:border-stone-700 dark:focus:border-stone-600",
    error: "border-red-400 focus:border-red-500 dark:border-red-700 dark:focus:border-red-600",
    warning: "border-amber-400 focus:border-amber-500 dark:border-amber-600 dark:focus:border-amber-500",
};

const FIELD =
    "w-full rounded-md border bg-white text-stone-900 outline-none transition-colors placeholder:text-stone-400 focus:ring-2 focus:ring-stone-400/20 disabled:cursor-not-allowed disabled:opacity-60 dark:bg-stone-900 dark:text-stone-100 dark:placeholder:text-stone-500";

type InputProps = Omit<InputHTMLAttributes<HTMLInputElement>, "size" | "prefix"> & {
    size?: Size;
    prefix?: ReactNode;
    status?: Status;
    onPressEnter?: () => void;
    allowClear?: boolean;
};

export function Input({ size = "middle", prefix, status = "normal", onPressEnter, allowClear, className, value, onChange, ...props }: InputProps): ReactNode {
    const [hovered, setHovered] = useState(false);
    const showClear = allowClear && value !== undefined && value !== "" && (hovered || false);
    return (
        <span className={cn("relative inline-flex w-full items-center", className)} onMouseEnter={() => setHovered(true)} onMouseLeave={() => setHovered(false)}>
            {prefix ? <span className="pointer-events-none absolute left-2.5 flex items-center text-stone-400">{prefix}</span> : null}
            <input
                value={value}
                onChange={onChange}
                onKeyDown={(event) => {
                    if (event.key === "Enter") onPressEnter?.();
                }}
                className={cn(FIELD, HEIGHTS[size], BORDERS[status], prefix ? "pl-8" : "px-2.5", showClear && "pr-7")}
                {...props}
            />
            {showClear ? (
                <button type="button" aria-label="Clear" onClick={() => onChange?.({ target: { value: "" } } as unknown as React.ChangeEvent<HTMLInputElement>)} className="absolute right-2 text-stone-400 hover:text-stone-600 dark:hover:text-stone-200">
                    ×
                </button>
            ) : null}
        </span>
    );
}

/** Grow-with-content textarea limited by `autoSize.minRows` / `maxRows`. */
function TextArea({ rows = 3, autoSize, status = "normal", className, ...props }: TextareaHTMLAttributes<HTMLTextAreaElement> & { status?: Status; autoSize?: boolean | { minRows?: number; maxRows?: number } }): ReactNode {
    const ref = useRef<HTMLTextAreaElement | null>(null);
    const minRows = typeof autoSize === "object" ? (autoSize.minRows ?? 2) : undefined;
    const maxRows = typeof autoSize === "object" ? (autoSize.maxRows ?? 8) : undefined;

    useEffect(() => {
        const el = ref.current;
        if (!el || !autoSize) return;
        el.style.height = "auto";
        const line = parseFloat(getComputedStyle(el).lineHeight) || 20;
        const max = maxRows ? maxRows * line + 16 : undefined;
        const next = max ? Math.min(el.scrollHeight, max) : el.scrollHeight;
        el.style.height = `${next}px`;
    }, [autoSize, maxRows, props.value]);

    return <textarea ref={ref} rows={autoSize ? (minRows ?? 2) : rows} className={cn(FIELD, "resize-none px-2.5 py-1.5 text-sm", BORDERS[status], autoSize && "overflow-hidden", className)} {...props} />;
}

function Password({ value, onChange, className, size = "middle", status = "normal", ...props }: Omit<InputProps, "prefix">): ReactNode {
    const [visible, setVisible] = useState(false);
    return (
        <span className={cn("relative inline-flex w-full items-center", className)}>
            <input type={visible ? "text" : "password"} value={value} onChange={onChange} className={cn(FIELD, HEIGHTS[size], BORDERS[status], "px-2.5 pr-9")} {...props} />
            <button type="button" aria-label={visible ? "Hide" : "Show"} onClick={() => setVisible((current) => !current)} className="absolute right-2 text-stone-400 hover:text-stone-600 dark:hover:text-stone-200">
                {visible ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
            </button>
        </span>
    );
}

function Search({ onSearch, className, value, onChange, ...props }: InputProps & { onSearch?: (value: string) => void }): ReactNode {
    return (
        <span className={cn("inline-flex w-full items-stretch", className)}>
            <Input value={value} onChange={onChange} onPressEnter={() => onSearch?.(String(value ?? ""))} prefix={<SearchIcon className="size-4" />} className="flex-1 [&>input]:rounded-r-none" {...props} />
            {/* Flush with the field, the way antd joined them into one control. */}
            <button
                type="button"
                onClick={() => onSearch?.(String(value ?? ""))}
                className="-ml-px rounded-r-md border border-l-0 border-stone-300 px-3 text-stone-600 transition-colors hover:bg-stone-50 dark:border-stone-700 dark:text-stone-300 dark:hover:bg-stone-800"
            >
                <SearchIcon className="size-4" />
            </button>
        </span>
    );
}

Input.TextArea = TextArea;
Input.Password = Password;
Input.Search = Search;

/** Number entry, replacing antd's `InputNumber`. Emits `number | null`, as antd did. */
export function InputNumber({
    value,
    onChange,
    min,
    max,
    step = 1,
    precision,
    size = "middle",
    className,
    ...props
}: Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange" | "size"> & { value?: number | null; onChange?: (value: number | null) => void; min?: number; max?: number; step?: number; precision?: number; size?: Size }): ReactNode {
    return (
        <input
            type="number"
            value={value ?? ""}
            min={min}
            max={max}
            step={step}
            onChange={(event) => {
                const raw = event.target.value;
                if (raw === "") return onChange?.(null);
                const parsed = Number(raw);
                if (Number.isNaN(parsed)) return;
                onChange?.(precision === undefined ? parsed : Number(parsed.toFixed(precision)));
            }}
            className={cn(FIELD, HEIGHTS[size], BORDERS.normal, "px-2.5 tabular-nums", className)}
            {...props}
        />
    );
}

/** Single-thumb range, replacing antd's `Slider` in its one-thumb form. */
export function Slider({ value = 0, onChange, min = 0, max = 100, step = 1, className }: { value?: number; onChange?: (value: number) => void; min?: number; max?: number; step?: number; className?: string }): ReactNode {
    return (
        <input
            type="range"
            value={value}
            min={min}
            max={max}
            step={step}
            onChange={(event) => onChange?.(Number(event.target.value))}
            className={cn("h-1.5 w-full cursor-pointer appearance-none rounded-full bg-stone-200 accent-stone-900 dark:bg-stone-800 dark:accent-stone-100", className)}
        />
    );
}
