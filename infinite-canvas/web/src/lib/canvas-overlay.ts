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
 *   - Our own floating layers are marked with `data-canvas-overlay`.
 *   - `data-canvas-no-zoom` is the narrower opt-out used by embedded content
 *     (audio players, scroll areas, composer inputs): it keeps zoom out, but the
 *     surrounding canvas still gets to close/reposition on click.
 *   - antd portals can't carry those attributes, so they are still matched by
 *     their internal `.ant-*` class names. Everything below that starts with
 *     `ANTD_` is the only place in the codebase that knows about them: dropping
 *     antd means deleting those constants and nothing else.
 */

const OWN_OVERLAY = "[data-canvas-overlay]";
const OWN_NO_ZOOM = "[data-canvas-no-zoom]";

/** antd's floating layers, matched by internal class name. */
export const ANTD_POPOVER = ".ant-popover";
export const ANTD_SELECT_DROPDOWN = ".ant-select-dropdown";
export const ANTD_MODAL_CONTENT = ".ant-modal-content";
const ANTD_DROPDOWN = ".ant-dropdown";
const ANTD_PICKER_DROPDOWN = ".ant-picker-dropdown";
const ANTD_TOOLTIP = ".ant-tooltip";
const ANTD_MODAL_ROOT = ".ant-modal-root";
const ANTD_NOTIFICATION = ".ant-notification";
const ANTD_MESSAGE = ".ant-message";
const ANTD_MODAL = ".ant-modal";

// Every floating layer the canvas must treat as "not mine". Tooltips, messages
// and notifications are included: clicking through them would otherwise
// deselect the node the user just clicked on.
const ANTD_FLOATING_LAYERS = [ANTD_SELECT_DROPDOWN, ANTD_DROPDOWN, ANTD_POPOVER, ANTD_PICKER_DROPDOWN, ANTD_TOOLTIP, ANTD_MODAL_ROOT, ANTD_NOTIFICATION, ANTD_MESSAGE];

// The narrower set used before zooming or before swallowing page scroll.
// `.ant-modal` (the dialog panel itself) is enough here — `.ant-modal-root`
// would also cover the mask, which is the click-to-close surface.
const ANTD_SCROLL_LAYERS = [ANTD_MODAL, ANTD_POPOVER, ANTD_DROPDOWN, ANTD_SELECT_DROPDOWN, ANTD_PICKER_DROPDOWN];

const anyOf = (...selectors: string[]) => selectors.join(",");

/** Pointer-down / double-click guard: a floating layer, or opted-out content. */
export const CANVAS_CLICK_GUARD = anyOf(OWN_OVERLAY, OWN_NO_ZOOM, ...ANTD_FLOATING_LAYERS);

/** Wheel guard: stop canvas zoom and page scroll from firing inside these. */
export const CANVAS_WHEEL_GUARD = anyOf(OWN_OVERLAY, OWN_NO_ZOOM, ...ANTD_SCROLL_LAYERS);

/** A click inside this must not dismiss a menu already open on top of it. */
export const CANVAS_DISMISS_GUARD = anyOf(OWN_OVERLAY, ANTD_POPOVER);

/** Nearest surface a floating menu inside a dialog should stay within. */
export const CANVAS_DIALOG_CONTENT = anyOf("[data-canvas-dialog-content]", ANTD_MODAL_CONTENT);
