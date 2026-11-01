import { describe, expect, it } from "vitest";
import { readFile, readdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";

/**
 * A portal that has to be visible over a dialog has to outrank it.
 *
 * The mask editor draws its own brush ring and hides the native cursor, so a ring that lands
 * underneath the dialog is a cursor that does not exist: the user paints blind. That is exactly
 * what happened — the ring carried `z-[1100]`, which was above antd's modal (1000) and below this
 * app's Radix dialog (1300), and the migration never revisited the number.
 *
 * The rule these tests state is the one the app already follows everywhere else: dialogs sit at
 * 1300+, drawers at 1350+, and anything that must float above them — tooltips, selects, menus,
 * a brush ring — sits at 1400 or higher.
 */

const here = path.dirname(fileURLToPath(import.meta.url));
const srcDir = path.resolve(here, "../src");

/** The highest z-index a dialog or drawer claims, i.e. the floor for anything drawn over it. */
async function overlayFloor(): Promise<number> {
    const files = ["components/kit/dialog.tsx", "components/kit/drawer.tsx"];
    let highest = 0;
    for (const file of files) {
        const raw = await readFile(path.join(srcDir, file), "utf8");
        for (const match of raw.matchAll(/z-\[(\d+)\]/g)) {
            highest = Math.max(highest, Number(match[1]));
        }
    }
    expect(highest).toBeGreaterThan(1000);
    return highest;
}

/**
 * Every `fixed z-[n]` portal declared in a component that renders a dialog or a drawer.
 *
 * A portalled menu or ring belongs to the component that renders it, so an overlay drawn by a
 * component living inside a dialog has to outrank that dialog. Portals belonging to the canvas —
 * its context menu, the @-mention list — are not in this set: the canvas is behind the dialog, and
 * the overlay eats the click before they can open.
 */
async function dialogPortals(): Promise<Array<{ file: string; z: number; line: string }>> {
    const out: Array<{ file: string; z: number; line: string }> = [];
    const walk = async (dir: string) => {
        for (const entry of await readdir(dir, { withFileTypes: true })) {
            const full = path.join(dir, entry.name);
            if (entry.isDirectory()) {
                await walk(full);
                continue;
            }
            if (!/\.tsx?$/.test(entry.name)) continue;
            if (file_endsWith(full, "dialog.tsx") || file_endsWith(full, "drawer.tsx")) continue;
            const raw = await readFile(full, "utf8");
            if (!/<Dialog\b|<Drawer\b/.test(raw)) continue;
            for (const line of raw.split("\n")) {
                if (!line.includes("fixed")) continue;
                const match = /z-\[(\d+)\]/.exec(line);
                if (!match) continue;
                out.push({ file: path.relative(srcDir, full), z: Number(match[1]), line: line.trim().slice(0, 120) });
            }
        }
    };
    await walk(srcDir);
    return out;
}

function file_endsWith(full: string, suffix: string) {
    return full.endsWith(path.sep + suffix) || full.endsWith("/" + suffix);
}

describe("overlay stacking", () => {
    it("draws every fixed overlay above the dialog it may be opened from", async () => {
        const floor = await overlayFloor();
        const portals = await dialogPortals();
        expect(portals.length).toBeGreaterThan(0);
        const buried = portals.filter((portal) => portal.z <= floor);
        expect(buried.map((portal) => `${portal.file}: ${portal.line}`)).toEqual([]);
    });

    it("never hides the cursor without drawing one", async () => {
        // `cursor-none` is only safe next to something that paints a cursor. The mask editor's
        // ring is the only such thing, and the two must stay in the same expression.
        const raw = await readFile(path.join(srcDir, "components/canvas/canvas-node-mask-edit-dialog.tsx"), "utf8");
        expect(raw).toContain("cursor-none");
        expect(raw).toContain('brushPreview ? "cursor-none" : "cursor-crosshair"');
    });
});
