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

// 模板拼接的命名空间，逐条声明。两种写法：
//   values: [...]  后缀只可能是这几个（节点模式、位置……），只有它们算"在用"——多出来的 key 照旧算死 key
//   不给 values    后缀来自数据表（无法静态读出），整个命名空间豁免；理由必须写清楚
// 没有声明的动态拼接会让检查失败：这就是"一个模板藏掉一整个命名空间"的补丁不许再开一个口子。
const DYNAMIC_PREFIXES = new Map([
    [
        "canvas.promptPanel",
        {
            values: ["image", "video", "audio", "text"],
            why: "`canvas.promptPanel.${mode}` 的 mode 是节点的生成方式；editImage/editText 由模板自己的字面量分支给出",
        },
    ],
    ["assets.kinds", { why: "后缀是资产自身的 kind 字段，静态读不出" }],
    ["canvas.sidePanel.filter", { why: "后缀是 NODE_FILTER_VALUES 里的节点类型" }],
    ["canvas.composer.resources", { why: "后缀是输入节点的类型（image/video/audio/text/group）" }],
    ["canvas.toolbar", { why: "后缀是当前画布工具（select/pan）" }],
    ["gateway.models", { why: "后缀是 registry 里的模型 id；中文标签由 chinese() 只查 zh 字典，见 known-issues #10" }],
    ["gateway.variants", { why: "同上：registry 里 variant 模式 id → 中文标签" }],
    ["settingsPanels.video", { why: "后缀是数据表里的 labelKey（尺寸、速度档、模式提示）" }],
    ["settingsPanels.video.modes", { why: "后缀是数据表里的模式 labelKey，外加两个提示分支的字面量" }],
    ["settingsPanels.model.capabilities", { why: "后缀是 ModelCapability 联合类型，值在类型里" }],
    ["config.channelEditor.capabilities", { why: "同上，来自 ModelCapability 数组" }],
    ["config.preferences.cardSkins", { why: "后缀是皮肤 id 的数据表，.label/.description 都是表格字段" }],
    ["config.preferences.progressRingSkins", { why: "同上，进度环皮肤的数据表" }],
    ["config.localStorage.stores", { why: "后缀是 localStorage 的 store key 表" }],
    ["canvas.videoFrames", { why: "后缀是 first/last/both 三种位置加 Title 后缀，模板读不出" }],
    ["apiErrors", { why: "apiText(key) 的后缀是各服务自己的错误码常量" }],
]);

const undeclared = [];
const reachable = new Set(HAND_RESOLVED);
const prefixes = new Set();
const unsound = [];
const stripDot = (s) => (s.endsWith(".") ? s.slice(0, -1) : s);

/**
 * 模板字符串里能静态读出的那些候选值。
 *
 * `t(`canvas.promptPanel.${mode === "image" ? "editImage" : mode}`)` 以前被当成"整个命名空间都在用"，
 * 结果 `canvas.promptPanel.*` 下 13 个死 key 藏了一个版本。现在只承认字面量候选（以及条件/拼接里
 * 能读出的分支）；只要有一处是变量、就返回 dynamic，让调用点进"无法静态解析"的清单，由人核对。
 */
function templateCandidates(prefix, expression) {
    const found = new Set();
    let dynamic = false;
    const walk = (node) => {
        if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
            found.add(`${prefix}.${node.text}`);
            return;
        }
        if (ts.isParenthesizedExpression(node)) return walk(node.expression);
        if (ts.isConditionalExpression(node)) {
            walk(node.whenTrue);
            walk(node.whenFalse);
            return;
        }
        if (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.PlusToken) {
            walk(node.left);
            walk(node.right);
            return;
        }
        dynamic = true;
    };
    walk(expression);
    for (const key of found) reachable.add(key);
    if (!dynamic) return true;

    const declared = DYNAMIC_PREFIXES.get(prefix);
    if (!declared) {
        undeclared.push(prefix);
        return false;
    }
    if (declared.values) {
        for (const value of declared.values) reachable.add(`${prefix}.${value}`);
    } else {
        prefixes.add(prefix);
    }
    return true;
}

function classify(node) {
    if (ts.isParenthesizedExpression(node)) return classify(node.expression);
    if (ts.isNoSubstitutionTemplateLiteral(node) || ts.isStringLiteral(node)) {
        reachable.add(node.text);
        return true;
    }
    if (ts.isTemplateExpression(node)) {
        const head = stripDot(node.head.text);
        let sound = true;
        for (const span of node.templateSpans) {
            if (!templateCandidates(head, span.expression)) sound = false;
        }
        return sound;
    }
    if (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.PlusToken && ts.isStringLiteral(node.left)) {
        const head = stripDot(node.left.text);
        return templateCandidates(head, node.right);
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

if (undeclared.length) {
    problems.push(`没有声明的动态 key 拼接（在 DYNAMIC_PREFIXES 里写明值或理由）:\n${[...new Set(undeclared)].map((prefix) => `    ${prefix}`).join("\n")}`);
}

if (problems.length) {
    console.error(problems.join("\n\n"));
    console.error(`\n失败：${deadZh.length + deadEn.length} 个死 key，${missingZh.length + missingEn.length} 个缺失 key。`);
    process.exit(1);
}

console.log(`i18n 检查通过：zh-CN ${zh.leaves.size} 个 key，en-US ${en.leaves.size} 个；无死 key、无缺失、两边一致。`);
