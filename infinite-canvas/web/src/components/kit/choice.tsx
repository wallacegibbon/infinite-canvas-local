import { LoaderCircle } from "lucide-react";
import { createContext, useContext, type ChangeEvent, type InputHTMLAttributes, type ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * Boolean and exclusive choices, replacing antd's `Switch`, `Checkbox` / `Checkbox.Group`
 * and `Radio` / `Radio.Group` / `Radio.Button`.
 *
 * `Checkbox` and `Radio` hand their `onChange` the **native** change event, because every
 * call site already reads `event.target.checked` or `event.target.value`; keeping that
 * shape avoided rewriting any handler. Groups supply `checked` and the toggle through
 * context so the items stay as plain markup at the call sites.
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

/* ----------------------------------------------------------------- Radio */

type RadioGroupContext = { value?: string; select: (value: string) => void };

const RadioContext = createContext<RadioGroupContext | null>(null);

/** antd gave handlers an event; the call sites read `event.target.value`. */
const asValueEvent = (value: string) => ({ target: { value } }) as unknown as ChangeEvent<HTMLInputElement>;

export function Radio({
    checked,
    onChange,
    disabled,
    className,
    children,
    value,
    ...props
}: Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange" | "type" | "size"> & { checked?: boolean; onChange?: (event: ChangeEvent<HTMLInputElement>) => void; value?: string; children?: ReactNode }): ReactNode {
    const group = useContext(RadioContext);
    const inGroup = Boolean(group && value !== undefined);
    const isChecked = inGroup ? group!.value === value : Boolean(checked);
    return (
        <label className={cn("inline-flex cursor-pointer items-center gap-2 text-sm", disabled && "cursor-not-allowed opacity-60", className)}>
            <input
                type="radio"
                checked={isChecked}
                disabled={disabled}
                onChange={(event) => {
                    if (inGroup) group!.select(value!);
                    onChange?.(event);
                }}
                className="size-3.5 shrink-0 cursor-pointer accent-stone-900 disabled:cursor-not-allowed dark:accent-stone-100"
                {...props}
            />
            {children}
        </label>
    );
}

/** A radio drawn as a segmented button, the way `optionType="button"` did. */
function RadioButton({ value, disabled, children }: { value: string; disabled?: boolean; children?: ReactNode }): ReactNode {
    const group = useContext(RadioContext);
    const active = group?.value === value;
    return (
        <button
            type="button"
            role="radio"
            aria-checked={active}
            disabled={disabled}
            onClick={() => group?.select(value)}
            className={cn(
                "inline-flex h-7 flex-1 items-center justify-center rounded px-2.5 text-sm whitespace-nowrap transition-colors disabled:cursor-not-allowed disabled:opacity-50",
                active ? "bg-white text-stone-900 shadow-sm dark:bg-stone-700 dark:text-stone-50" : "text-stone-600 hover:text-stone-900 dark:text-stone-300 dark:hover:text-stone-100",
            )}
        >
            {children}
        </button>
    );
}

function RadioGroup({ value, onChange, className, children }: { value?: string; onChange?: (event: ChangeEvent<HTMLInputElement>) => void; className?: string; children?: ReactNode; optionType?: "default" | "button" }): ReactNode {
    // `optionType="button"` used to switch antd's rendering; the buttons below read the same
    // context either way, so the prop is accepted and not needed.
    const select = (next: string) => onChange?.(asValueEvent(next));
    const buttonMode = isButtonGroup(children);
    return (
        <RadioContext.Provider value={{ value, select }}>
            <div role="radiogroup" className={cn(buttonMode ? "inline-flex items-center gap-0.5 rounded-md bg-stone-100 p-0.5 dark:bg-stone-800" : "flex flex-wrap items-center gap-4", className)}>
                {children}
            </div>
        </RadioContext.Provider>
    );
}

function isButtonGroup(children: ReactNode): boolean {
    return Array.isArray(children) ? children.some((child) => typeof child === "object" && child !== null && (child as { type?: unknown }).type === RadioButton) : (children as { type?: unknown } | null)?.type === RadioButton;
}

Radio.Group = RadioGroup;
Radio.Button = RadioButton;
