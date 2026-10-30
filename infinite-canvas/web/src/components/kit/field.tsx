import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * Labelled form row, replacing antd's `Form.Item`.
 *
 * It is layout only — a label, the control, an optional hint and an optional error. That is
 * all antd's `Form.Item` was doing at 15 of its call sites; the rest bound a value and ran
 * validation, and those two call sites now hold their values in plain `useState` instead. The
 * reason is the same in both cases: antd injected `value`/`onChange` into the child by cloning
 * the element and guessing how to read a value out of each control type, and every control it
 * guessed wrong needed a per-field escape hatch (`getValueFromEvent`, `valuePropName`). With
 * seven fields, writing `value={values.title}` and `onChange` explicitly is shorter and has no
 * guessing in it.
 *
 * antd's `requiredMark` was set to `false` at every call site, so no asterisk is rendered; the
 * error text is what tells the user a field is required.
 */
export function Field({
    label,
    extra,
    error,
    htmlFor,
    className,
    children,
}: {
    label?: ReactNode;
    /** Hint under the control, antd's `extra`. */
    extra?: ReactNode;
    /** Validation message under the control. */
    error?: ReactNode;
    /** Set to make the label a real `<label>`; omitted, it renders as a `<div>`. */
    htmlFor?: string;
    /** antd's 24px gap; call sites that passed `mb-4` / `mb-0` still win. */
    className?: string;
    children?: ReactNode;
}): ReactNode {
    const labelClass = "text-sm text-stone-600 dark:text-stone-400";
    return (
        <div className={cn("mb-6 flex flex-col gap-1.5", className)}>
            {label === undefined ? null : htmlFor ? (
                <label htmlFor={htmlFor} className={labelClass}>
                    {label}
                </label>
            ) : (
                <div className={labelClass}>{label}</div>
            )}
            {children}
            {extra ? <div className="text-xs leading-5 text-stone-400 dark:text-stone-500">{extra}</div> : null}
            {error ? <div className="text-xs leading-5 text-red-500 dark:text-red-400">{error}</div> : null}
        </div>
    );
}
