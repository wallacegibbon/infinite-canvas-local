/**
 * The block the canvas puts in front of a prompt when the user cites nodes.
 *
 * The agent needs it — `@补全背景` means nothing without the text that follows — but it is plumbing, not
 * something anybody typed. So the same module builds it and takes it off again: the panel shows the words
 * the person wrote, and a conversation replayed from the agent's session file shows them too, even though
 * what was sent was the marked-up version.
 */

/** The marker in either language: a conversation can be read back in a UI that has switched languages. */
const MARKERS = ["【画布引用】", "[Canvas references]"];

/** One line describing a cited node: `- <id> (<kind>) <title>: <prompt>`. */
export function referenceLine(id: string, kind: string, title: string, prompt: string): string {
    return `- ${id} (${kind}) ${title}${prompt ? `: ${prompt}` : ""}`;
}

/** The whole leading block, including the blank line that separates it from the prompt. */
export function referenceBlock(marker: string, lines: string[]): string {
    if (!lines.length) return "";
    return `${marker}\n${lines.join("\n")}\n\n`;
}

/**
 * The prompt without its leading reference block — what the user actually typed.
 *
 * A prompt that does not begin with a marker is returned unchanged, and so is one whose marker is not
 * followed by a blank line: guessing where the block ends would cut words off a message that only looked
 * like one.
 */
export function withoutReferences(prompt: string): string {
    const marker = MARKERS.find((candidate) => prompt.startsWith(`${candidate}\n`));
    if (!marker) return prompt;
    const blank = prompt.indexOf("\n\n");
    if (blank < 0) return prompt;
    return prompt.slice(blank + 2);
}

/**
 * The nodes a prompt cited, as the block recorded them: `- <id> (<kind>) <title>: <prompt>`.
 *
 * Reading the block back is what lets a mention in a message be a link to the node: the visible text says
 * `@补全背景`, and only the block says which node that is.
 */
export function referenceEntries(prompt: string): { id: string; title: string }[] {
    const marker = MARKERS.find((candidate) => prompt.startsWith(`${candidate}\n`));
    if (!marker) return [];
    const end = prompt.indexOf("\n\n");
    if (end < 0) return [];
    const entries: { id: string; title: string }[] = [];
    for (const line of prompt.slice(marker.length + 1, end).split("\n")) {
        const match = /^-\s+(\S+)\s+\(([^)]*)\)\s*(.*)$/.exec(line);
        if (!match) continue;
        const rest = match[3] || "";
        const colon = rest.indexOf(": ");
        entries.push({ id: match[1], title: (colon >= 0 ? rest.slice(0, colon) : rest).trim() });
    }
    return entries;
}
