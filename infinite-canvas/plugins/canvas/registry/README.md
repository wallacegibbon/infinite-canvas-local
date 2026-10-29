# 官方插件注册表(集中构建 + 远程发布)

本目录**只放构建脚本**,不放构建产物。官方插件构建后随画布一起分发(产物提交到 `infinite-canvas/web/public/plugins/`),不从外部 CDN 拉取。第三方插件不进本流程,由用户自行填 JS URL 安装。

```
registry/
  package.json    # 构建依赖(esbuild + SDK)
  build.mjs       # 一次进程构建所有官方插件 → dist/(gitignore)+ 生成清单
  dist/           # 构建产物(gitignore,不提交;手动复制到 web/public/plugins/)
```

**产物不进 git**:`dist/` 与 `node_modules/` 均被 `.gitignore` 覆盖。`main` 分支只有源码与脚本。

## 发布流程(本地手动)

本仓库不使用上游的 `plugins-dist` 分支与 jsDelivr CDN 分发（对应 CI 已移除）。插件产物随画布一起构建：

1. `npm install && npm run build` → 在 `dist/` 产出各 `<id>.js` 与 `official-plugins.json`；
2. 把 `dist/` 的内容复制到 `infinite-canvas/web/public/plugins/` 并提交。

前端从同源的 `/plugins/index.json` 读取清单（构建时由 `public/plugins/` 原样复制进 `dist/`）。

清单里每条的 `entry`(相对文件名)由前端解析成与清单同目录的绝对 URL,再走既有 URL 安装流程。

## 新增 / 更新官方插件

- 改完某官方插件源码后,在 `build.mjs` 的 `OFFICIAL` 里保持登记(新增插件在此加一条),提交到 `main`;
- 重新执行 `npm run build`,把 `dist/` 复制到 `infinite-canvas/web/public/plugins/` 并提交。

## 本地自测官方面板

```bash
cd plugins/canvas/registry && npm install && npm run build   # 产出 dist/
# 用任意静态服务器伺服 dist/,把 VITE_PLUGIN_REGISTRY_URL 指向其 official-plugins.json
```
