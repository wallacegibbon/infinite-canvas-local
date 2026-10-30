import { Tooltip as RadixTooltip } from "radix-ui";
import { isValidElement, type CSSProperties, type ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * Hover labels, replacing antd's `Tooltip`.
 *
 * Props keep antd's names (`title`, `placement`, `mouseEnterDelay`) plus the two styling
 * escape hatches the call sites use (`color` for the bubble background, `styles.root` for
 * text colour/shadow/typography) — those two are always used together, for the white
 * bubble in the canvas editors.
 *
 * Two behaviours are deliberate:
 *   - **A disabled child is wrapped in a `<span>`.** Ten tooltips here explain why a
 *     button is disabled, and a disabled `<button>` emits no pointer events, so without the
 *     wrapper the explanation would never appear. antd did the same thing internally.
 *   - **An empty `title` renders no tooltip at all**, matching antd, so call sites can pass
 *     a conditional string without guarding the element.
 *
 * The portal carries `data-canvas-overlay`; without it a tooltip opened over the canvas
 * would let the wheel and clicks through to the canvas underneath.
 */
type Placement = "top" | "bottom" | "left" | "right";

export function Tooltip({
    title,
    children,
    placement = "top",
    mouseEnterDelay = 0.2,
    color,
    styles,
    className,
}: {
    title?: ReactNode;
    children?: ReactNode;
    placement?: Placement;
    /** Seconds, as antd took it. */
    mouseEnterDelay?: number;
    /** Bubble background. */
    color?: string;
    styles?: { root?: CSSProperties };
    className?: string;
}): ReactNode {
    if (title === undefined || title === null || title === "" || title === false) return children;

    // A disabled child swallows pointer events, so it needs a hoverable wrapper.
    const disabled = isValidElement(children) && Boolean((children.props as { disabled?: boolean }).disabled);
    // `pointer-events-none` on the disabled child is what makes the wrapper the hit target;
    // a disabled control swallows pointer events, so without it the wrapper never sees a
    // hover and the explanation never appears.
    const trigger = disabled ? <span className="inline-flex [&>*]:pointer-events-none">{children}</span> : children;

    return (
        <RadixTooltip.Provider delayDuration={Math.round(mouseEnterDelay * 1000)}>
            <RadixTooltip.Root>
                <RadixTooltip.Trigger asChild>{trigger}</RadixTooltip.Trigger>
                <RadixTooltip.Portal>
                    <RadixTooltip.Content
                        data-canvas-overlay
                        side={placement}
                        sideOffset={6}
                        style={{ background: color, ...styles?.root }}
                        className={cn("z-[1500] max-w-[250px] rounded-md px-2 py-1.5 text-xs leading-snug font-normal", color ? "text-stone-900" : "bg-stone-900/95 text-stone-50 dark:bg-stone-700", className)}
                    >
                        {title}
                    </RadixTooltip.Content>
                </RadixTooltip.Portal>
            </RadixTooltip.Root>
        </RadixTooltip.Provider>
    );
}
