import { LoaderCircle } from "lucide-react";
import { createContext, useContext, type ChangeEvent, type InputHTMLAttributes, type ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * Boolean and exclusive choices, replacing antd's `Switch` and `Checkbox` / `Checkbox.Group`.
 *
 * `Checkbox` hands its `onChange` the **native** change event, because every call site already
 * reads `event.target.checked`; keeping that shape avoided rewriting any handler. Groups supply
 * the state and the toggle through context so the items stay as plain markup at the call sites.
 */

/* ---------------------------------------------------------------- Switch */

export function Switch({
    checked = false,
    onChange,
    size = "middle",
    loading = false,
    disabled,
    className,
}: {
    checked?: boolean;
    onChange?: (checked: boolean) => void;
    size?: "small" | "middle";
    loading?: boolean;
    disabled?: boolean;
    className?: string;
}): ReactNode {
    const small = size === "small";
    return (
        <button
            type="button"
            role="switch"
            aria-checked={checked}
            disabled={disabled || loading}
            onClick={() => onChange?.(!checked)}
            className={cn(
                "relative inline-flex shrink-0 cursor-pointer items-center rounded-full transition-colors outline-none focus-visible:ring-2 focus-visible:ring-stone-400/40 disabled:cursor-not-allowed disabled:opacity-60",
                small ? "h-4 w-7" : "h-5 w-9",
                checked ? "bg-stone-900 dark:bg-stone-100" : "bg-stone-300 dark:bg-stone-700",
                className,
            )}
        >
            <span className={cn("inline-flex items-center justify-center rounded-full bg-white shadow-sm transition-transform dark:bg-stone-900", small ? "size-3" : "size-4", checked ? (small ? "translate-x-3.5" : "translate-x-4.5") : "translate-x-0.5")}>
                {loading ? <LoaderCircle className={small ? "size-2 animate-spin" : "size-3 animate-spin"} /> : null}
            </span>
        </button>
    );
}

/* -------------------------------------------------------------- Checkbox */

type CheckboxGroupContext = { values: readonly string[]; toggle: (value: string, checked: boolean) => void };

const CheckboxContext = createContext<CheckboxGroupContext | null>(null);

export function Checkbox({
    checked,
    onChange,
    disabled,
    className,
    children,
    value,
    ...props
}: Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange" | "type" | "size"> & { checked?: boolean; onChange?: (event: ChangeEvent<HTMLInputElement>) => void; value?: string; children?: ReactNode }): ReactNode {
    const group = useContext(CheckboxContext);
    const inGroup = Boolean(group && value !== undefined);
    const isChecked = inGroup ? group!.values.includes(value!) : Boolean(checked);
    return (
        <label className={cn("inline-flex cursor-pointer items-center gap-2 text-sm", disabled && "cursor-not-allowed opacity-60", className)}>
            <input
                type="checkbox"
                checked={isChecked}
                disabled={disabled}
                onChange={(event) => {
                    if (inGroup) group!.toggle(value!, event.target.checked);
                    onChange?.(event);
                }}
                className="size-3.5 shrink-0 cursor-pointer accent-stone-900 disabled:cursor-not-allowed dark:accent-stone-100"
                {...props}
            />
            {children}
        </label>
    );
}

function CheckboxGroup({ value = [], onChange, className, children }: { value?: readonly string[]; onChange?: (values: string[]) => void; className?: string; children?: ReactNode }): ReactNode {
    const toggle = (item: string, checked: boolean) => {
        const next = checked ? [...value, item] : value.filter((current) => current !== item);
        onChange?.(next);
    };
    return (
        <CheckboxContext.Provider value={{ values: value, toggle }}>
            <div className={className}>{children}</div>
        </CheckboxContext.Provider>
    );
}

Checkbox.Group = CheckboxGroup;
