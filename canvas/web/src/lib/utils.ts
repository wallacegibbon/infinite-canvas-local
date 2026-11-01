import { twMerge, type ClassNameValue } from "tailwind-merge";

/**
 * Merge Tailwind class lists, letting later classes win.
 *
 * This was `twMerge(clsx(inputs))`. `clsx` was only ever adding two things here:
 * object syntax (`{ "text-red": isError }`) and number handling. All 85 call sites use
 * strings and `condition && "class"`, which `twMerge` accepts directly — so `clsx` went,
 * and with it a dependency. Passing an object is now a type error rather than a class
 * that silently disappears, which is the failure mode this signature change prevents.
 */
export function cn(...inputs: ClassNameValue[]): string {
    return twMerge(...inputs);
}
