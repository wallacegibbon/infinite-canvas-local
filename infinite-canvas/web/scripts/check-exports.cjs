#!/usr/bin/env node
// 导出即承诺：每个 `export` 都意味着"外面有人依赖这个名字"。
//
// 这个检查用 TypeScript 的 language service（`findReferences`），不是 grep —— 后者分不清
// "导出后本文件自己在用"和"外面在 import"，也跟不上 barrel 重导出。
//
// 三类问题：
//   A. 完全没人引用      → 应该删掉
//   B. 只有本文件在用    → 应该去掉 `export`
//   C. 只有测试在用      → 要么去掉 export（测试改用公开入口），要么在 TEST_ONLY 里写明理由
//
// C 是刻意算作问题的：测试不是产品代码，一个只有测试 import 的导出说明这个函数/类型
// 没有真正的调用者，`agent.Run` 就是这么活了一个版本的（Go 那边，同样的盲点）。
//
// 豁免：`src/types/canvas-plugin.ts` 是**发布出去的插件契约**，由
// `../plugins/canvas/sdk/src/types.ts` 镜像。插件作者会 import 这些类型，
// "在本仓库没人用"恰恰不是死代码。
//
// 用法：npm run check:exports   （有问题时以非零码退出）

const fs = require("fs");
const path = require("path");
const ts = require("typescript");

const WEB = path.resolve(__dirname, "..");
const PLUGINS = path.resolve(WEB, "../plugins");

const EXEMPT = new Set([path.join(WEB, "src/types/canvas-plugin.ts"), path.join(WEB, "src/vite-env.d.ts")]);

// 只被测试引用的导出，逐条写明理由。这里每多一行，就说明产品代码少一个调用者。
const TEST_ONLY = new Map([
    ["src/lib/gateway-models.ts:modelsOf", "一个 workflow → 若干候选条目的映射，本身就是被测单元；gatewayModels 只是它的 fold"],
    ["src/services/api/http.ts:ApiError", "http.ts 抛出的错误类型，测试用它断言 status/body 的形状"],
    ["src/services/api/http.ts:readApiErrorMessage", "解析器本身是被测单元：表格用例覆盖 msg/message/error/detail/序列化 JSON/HTML 六种形状；从 apiErrorMessage 走会混进状态码兜底"],
]);

function walk(dir, out = []) {
    if (!fs.existsSync(dir)) return out;
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
        const full = path.join(dir, entry.name);
        if (entry.isDirectory()) {
            if (entry.name === "node_modules" || entry.name === "dist") continue;
            walk(full, out);
        } else if (/\.tsx?$/.test(entry.name) && !/\.d\.ts$/.test(entry.name)) {
            out.push(full);
        }
    }
    return out;
}

const srcFiles = walk(path.join(WEB, "src"));
const allFiles = [...srcFiles, ...walk(path.join(WEB, "tests")), ...walk(PLUGINS)];

const configPath = path.join(WEB, "tsconfig.json");
const parsed = ts.parseJsonConfigFileContent(ts.readConfigFile(configPath, ts.sys.readFile).config, ts.sys, WEB);
const options = {
    ...parsed.options,
    noEmit: true,
    incremental: false,
    composite: false,
    allowJs: true,
    jsx: ts.JsxEmit.ReactJSX,
    baseUrl: WEB,
    paths: { "@/*": ["./src/*"] },
    types: [],
};

const host = {
    getScriptFileNames: () => allFiles,
    getScriptVersion: () => "0",
    getScriptSnapshot: (file) => {
        const text = ts.sys.readFile(file);
        return text === undefined ? undefined : ts.ScriptSnapshot.fromString(text);
    },
    getCurrentDirectory: () => WEB,
    getCompilationSettings: () => options,
    getDefaultLibFileName: (o) => ts.getDefaultLibFilePath(o),
    fileExists: ts.sys.fileExists,
    readFile: ts.sys.readFile,
    readDirectory: ts.sys.readDirectory,
    directoryExists: ts.sys.directoryExists,
    getDirectories: ts.sys.getDirectories,
};

const service = ts.createLanguageService(host, ts.createDocumentRegistry());
const program = service.getProgram();
const checker = program.getTypeChecker();

const unused = [];
const localOnly = [];
const testOnly = [];

for (const file of srcFiles) {
    if (EXEMPT.has(file)) continue;
    const sf = program.getSourceFile(file);
    if (!sf) continue;
    const moduleSymbol = checker.getSymbolAtLocation(sf);
    if (!moduleSymbol) continue;

    let exports;
    try {
        exports = checker.getExportsOfModule(moduleSymbol);
    } catch {
        continue;
    }

    for (const exported of exports) {
        let symbol = exported;
        if (symbol.flags & ts.SymbolFlags.Alias) {
            try {
                symbol = checker.getAliasedSymbol(symbol);
            } catch {
                /* keep the alias */
            }
        }
        const declaration = (symbol.declarations || [])[0];
        const nameNode = declaration?.name;
        if (!nameNode || !ts.isIdentifier(nameNode)) continue;

        const declaringFile = declaration.getSourceFile().fileName;
        // 只报告 src/ 下自己的东西：重导出的第三方类型不归这里管。
        if (!declaringFile.startsWith(path.join(WEB, "src"))) continue;
        if (EXEMPT.has(declaringFile)) continue;

        const groups = service.findReferences(declaringFile, nameNode.getStart()) || [];
        let external = 0;
        let own = 0;
        let tests = 0;
        const seen = new Set();
        for (const group of groups) {
            for (const ref of group.references) {
                if (ref.isDefinition) continue;
                const key = `${ref.fileName}:${ref.textSpan.start}`;
                if (seen.has(key)) continue;
                seen.add(key);
                if (ref.fileName === declaringFile) own += 1;
                else if (ref.fileName.includes(`${path.sep}tests${path.sep}`)) tests += 1;
                else external += 1;
            }
        }

        const relative = path.relative(WEB, declaringFile);
        const entry = {
            name: exported.getName(),
            where: `${relative}:${declaration.getSourceFile().getLineAndCharacterOfPosition(declaration.getStart()).line + 1}`,
        };
        if (external === 0 && own === 0 && tests === 0) unused.push(entry);
        else if (external === 0 && tests === 0) localOnly.push(entry);
        else if (external === 0 && tests > 0 && !TEST_ONLY.has(`${relative}:${entry.name}`)) testOnly.push(entry);
    }
}

if (unused.length || localOnly.length || testOnly.length) {
    if (unused.length) {
        console.error(`没人引用的导出（应当删除）共 ${unused.length} 个:`);
        for (const item of unused) console.error(`    ${item.where}  ${item.name}`);
    }
    if (localOnly.length) {
        console.error(`只在声明文件内部使用的导出（应当去掉 export）共 ${localOnly.length} 个:`);
        for (const item of localOnly) console.error(`    ${item.where}  ${item.name}`);
    }
    if (testOnly.length) {
        console.error(`只被测试引用的导出（去掉 export，或在 TEST_ONLY 里写明理由）共 ${testOnly.length} 个:`);
        for (const item of testOnly) console.error(`    ${item.where}  ${item.name}`);
    }
    process.exit(1);
}

console.log(`导出检查通过：src 下 ${srcFiles.length} 个模块，没有无效导出。`);
