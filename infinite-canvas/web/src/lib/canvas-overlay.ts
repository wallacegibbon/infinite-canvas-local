/**
 * Canvas overlay guards.
 *
 * Dialogs, dropdowns and popovers render in portals — outside the canvas
 * subtree — but React still bubbles their pointer/wheel events through the
 * canvas. So the canvas has to ask "did this event start inside a floating
 * layer?" before it zooms, blocks page scroll, or treats a click as a
 * background click. Getting it wrong is silent: nothing throws, the canvas just
 * stops panning or stops deselecting.
 *
 * The contract:
 *   - Every floating layer our components render carries `data-canvas-overlay`.
 *   - `data-canvas-no-zoom` is the narrower opt-out used by embedded content
 *     (audio players, scroll areas, composer inputs): it keeps zoom out, but the
 *     surrounding canvas still gets to close/reposition on click.
 *
 * Both are attributes we set ourselves, which is the point: this module used to
 * also match antd's `.ant-*` internals because those portals could not carry
 * attributes. With antd gone there is nothing here that knows a third-party class
 * name, and a new floating layer only has to set the attribute.
 */

const OWN_OVERLAY = "[data-canvas-overlay]";
const OWN_NO_ZOOM = "[data-canvas-no-zoom]";

const anyOf = (...selectors: string[]) => selectors.join(",");

/** Pointer-down / double-click guard: a floating layer, or opted-out content. */
export const CANVAS_CLICK_GUARD = anyOf(OWN_OVERLAY, OWN_NO_ZOOM);

/** Wheel guard: stop canvas zoom and page scroll from firing inside these. */
export const CANVAS_WHEEL_GUARD = anyOf(OWN_OVERLAY, OWN_NO_ZOOM);

/** A click inside this must not dismiss a menu already open on top of it. */
export const CANVAS_DISMISS_GUARD = OWN_OVERLAY;

/** Nearest surface a floating menu inside a dialog should stay within. */
export const CANVAS_DIALOG_CONTENT = anyOf("[data-canvas-dialog-content]", OWN_OVERLAY);
