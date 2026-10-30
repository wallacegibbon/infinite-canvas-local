#!/usr/bin/env node
// 翻译字典的两把尺子：有没有没人用的 key，有没有用了却不存在的 key。
//
// 为什么需要它：`fallbackLng` 是 zh-CN，所以英文缺一个 key 时界面会**静默显示中文**，
// 不报错也不警告。反过来，删掉一个还在被引用的 key，界面会**原样印出 key 名**
// （`assets.kinds.audio`）。这两种都不产生类型错误，只有跑一遍对比才能发现。
//
// 用法：npm run check:i18n   （字典或调用点有问题时以非零码退出）

const fs = require("fs");
const path = require("path");
const ts = require("typescript");

const WEB = path.resolve(__dirname, "..");
const SRC = path.join(WEB, "src");

function walk(dir, out = []) {
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
        const full = path.join(dir, entry.name);
        if (entry.isDirectory()) walk(full, out);
        else if (/\.tsx?$/.test(entry.name)) out.push(full);
    }
    return out;
}

const parse = (file) => ts.createSourceFile(file, fs.readFileSync(file, "utf8"), ts.ScriptTarget.ESNext, true, /\.tsx$/.test(file) ? ts.ScriptKind.TSX : ts.ScriptKind.TS);

/** Dotted paths to every leaf value, plus every object path on the way down. */
function flatten(node, prefix, leaves, branches) {
    if (!ts.isObjectLiteralExpression(node)) return;
    for (const prop of node.properties) {
        if (!ts.isPropertyAssignment(prop)) continue;
        const name = prop.name;
        const key = ts.isIdentifier(name) || ts.isStringLiteral(name) || ts.isNumericLiteral(name) ? name.text : null;
        if (!key) continue;
        const full = prefix ? `${prefix}.${key}` : key;
        if (ts.isObjectLiteralExpression(prop.initializer)) {
            branches.add(full);
            flatten(prop.initializer, full, leaves, branches);
        } else {
            leaves.add(full);
        }
    }
}

function loadLocale(name) {
    const sf = parse(path.join(SRC, "i18n/locales", name));
    const leaves = new Set();
    const branches = new Set();
    sf.forEachChild((node) => {
        if (ts.isExportAssignment(node)) flatten(node.expression, "", leaves, branches);
    });
    return { leaves, branches };
}

const zh = loadLocale("zh-CN.ts");
const en = loadLocale("en-US.ts");

// 通过 `t(item.labelKey)` 取到的 key，来源是同文件的常量表。逐条列出，因为工具无法解析。
// 新增一处 `t(变量)` 时这里必须同步，否则那条分支的 key 会被误判为死 key。
const HAND_RESOLVED = [
    "canvas.mossLayout.kindObject",
    "canvas.mossLayout.kindCharacter",
    "canvas.mossLayout.kindText",
    "canvas.outpaint.original",
    "config.preferences.defaultImageModel",
    "config.preferences.defaultVideoModel",
    "config.preferences.defaultTextModel",
    "config.preferences.defaultAudioModel",
];

const reachable = new Set(HAND_RESOLVED);
const prefixes = new Set();
const unsound = [];
const stripDot = (s) => (s.endsWith(".") ? s.slice(0, -1) : s);

function classify(node) {
    if (ts.isParenthesizedExpression(node)) return classify(node.expression);
    if (ts.isNoSubstitutionTemplateLiteral(node) || ts.isStringLiteral(node)) {
        reachable.add(node.text);
        return true;
    }
    if (ts.isTemplateExpression(node)) {
        prefixes.add(stripDot(node.head.text));
        return true;
    }
    if (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.PlusToken && ts.isStringLiteral(node.left)) {
        prefixes.add(stripDot(node.left.text));
        return true;
    }
    if (ts.isConditionalExpression(node)) return classify(node.whenTrue) && classify(node.whenFalse);
    return false;
}

// `chinese(...)` 在 lib/gateway-labels.ts 里只问 zh 字典，所以那些 key 没有 en 对应项。
const KEY_FNS = new Set(["t", "chinese"]);

for (const file of walk(SRC)) {
    if (file.split(path.sep).includes("locales")) continue;
    const sf = parse(file);
    const visit = (node) => {
        if (ts.isCallExpression(node)) {
            const callee = node.expression;
            const takesKey = (ts.isIdentifier(callee) && KEY_FNS.has(callee.text)) || (ts.isPropertyAccessExpression(callee) && callee.name.text === "t");
            if (takesKey && node.arguments.length && !classify(node.arguments[0])) {
                unsound.push(`${path.relative(WEB, file)}:${sf.getLineAndCharacterOfPosition(node.getStart()).line + 1}`);
            }
            // i18n.getFixedT("zh-CN")(key)
            if (ts.isCallExpression(callee) && ts.isPropertyAccessExpression(callee.expression) && callee.expression.name.text === "getFixedT" && node.arguments.length) {
                classify(node.arguments[0]);
            }
        }
        ts.forEachChild(node, visit);
    };
    visit(sf);
}

const used = (key) => reachable.has(key) || [...prefixes].some((p) => key === p || key.startsWith(`${p}.`)) || [...reachable].some((r) => key.startsWith(`${r}.`));

const dead = (locale) => [...locale.leaves].filter((k) => !used(k)).sort();
// 一个被 `t("...", { returnObjects: true })` 整体取走的对象路径不是叶子，不算缺失。
const missing = (locale) => [...reachable].filter((k) => k.includes(".") && !locale.leaves.has(k) && !locale.branches.has(k) && !prefixes.has(k)).sort();

const problems = [];

const deadZh = dead(zh);
const deadEn = dead(en);
if (deadZh.length) problems.push(`死 key（zh-CN 无人引用）:\n${deadZh.map((k) => `    ${k}`).join("\n")}`);
if (deadEn.length) problems.push(`死 key（en-US 无人引用）:\n${deadEn.map((k) => `    ${k}`).join("\n")}`);

const missingZh = missing(zh);
const missingEn = missing(en);
if (missingZh.length) problems.push(`缺失 key（代码引用了，zh-CN 没有）:\n${missingZh.map((k) => `    ${k}`).join("\n")}`);
if (missingEn.length) problems.push(`缺失 key（代码引用了，en-US 没有）:\n${missingEn.map((k) => `    ${k}`).join("\n")}`);

// 两份字典必须描述同一个界面，除了这几处：`lib/gateway-labels.ts` 的 `chinese()` 只问
// zh-CN 字典，用来给 registry.json 里的英文模型/variant id 配中文标签。英文界面直接用
// registry 里的英文原文，所以那边没有对应项是正确的。
const ZH_ONLY_PREFIXES = ["gateway.models.", "gateway.variants."];
const zhOnlyByDesign = (key) => ZH_ONLY_PREFIXES.some((prefix) => key.startsWith(prefix));

const onlyZh = [...zh.leaves].filter((k) => !en.leaves.has(k) && !zhOnlyByDesign(k)).sort();
const onlyEn = [...en.leaves].filter((k) => !zh.leaves.has(k) && !zhOnlyByDesign(k)).sort();
if (onlyZh.length) problems.push(`只有 zh-CN 有:\n${onlyZh.map((k) => `    ${k}`).join("\n")}`);
if (onlyEn.length) problems.push(`只有 en-US 有:\n${onlyEn.map((k) => `    ${k}`).join("\n")}`);

if (unsound.length) {
    console.warn(`注意：${unsound.length} 处 t(变量) 无法静态解析，需与 HAND_RESOLVED 人工核对:`);
    for (const site of unsound) console.warn(`    ${site}`);
}

if (problems.length) {
    console.error(problems.join("\n\n"));
    console.error(`\n失败：${deadZh.length + deadEn.length} 个死 key，${missingZh.length + missingEn.length} 个缺失 key。`);
    process.exit(1);
}

console.log(`i18n 检查通过：zh-CN ${zh.leaves.size} 个 key，en-US ${en.leaves.size} 个；无死 key、无缺失、两边一致。`);
