# 依赖许可证与发布声明证据盘点

状态：研究与发布门禁记录，不是法律意见，也不保证不存在其他许可、专利、商标、出口或合同义务。

查阅日期：2026-09-22

范围：当前 `go.mod`、`go.sum`、`web/package.json`、`web/bun.lock`，本机 Go module cache、`web/node_modules` 中随包提供的许可证/NOTICE，以及当前 `web/dist` 的内容特征。未读取归档 CPA、CLIProxyAPI 或 Sub2API 源码。

## 1. 基线与方法

本次盘点使用下列文件散列固定证据基线：

| 文件 | SHA-256 |
| --- | --- |
| `go.mod` | `6CD910953F0A317873DF6D47CCF977A6B17515475051F3238014E9A150BDFA24` |
| `go.sum` | `19578D11912868836E86DECEE66332EEF697F5DFF7EE0C7C54CCA24999BE89C5` |
| `web/package.json` | `1FAD2FCD3EC4D9985BE2DBD415622E131D22169573A394542588D96D6376162F` |
| `web/bun.lock` | `A4EB191AE92B5545E6F8DEB8AD504EBECB44C929A5D4146ACF225988A27C2ADE` |

核对方法：

1. 从 `go.mod` 读取 2 个直接模块和 10 个显式 indirect 模块；逐一核对本机 module cache 根目录及其嵌入目录的许可证文件。
2. `go.sum` 有 25 个不同模块名，但它是校验和历史集合，不等同于当前选定模块图。没有仅因某项出现在 `go.sum` 就把它写成已链接依赖。
3. 从 `web/bun.lock` 读取 151 个锁定条目；将其与本机 Windows x64 的 `web/node_modules/**/package.json`、`LICENSE`/`LICENCE`/`COPYING`/`NOTICE` 文件交叉核对。
4. 当前安装树物化了 107 个锁定包版本；TypeScript 包内还带有未作为独立锁条目出现的 `vscode-jsonrpc@9.0.0`。因此本地许可证分类共覆盖 108 个已落盘组件版本。
5. 当前压缩 JavaScript 输出中能识别 React、React DOM/Scheduler 运行时代码，但未发现 `@license` 或 copyright 标记。该观察只适用于盘点时的构建产物，发布时必须重新生成并核对最终产物。

## 2. Go 运行时依赖

项目源码直接导入 `golang.org/x/crypto/bcrypt` 和 `modernc.org/sqlite`。其余 10 项由 `go.mod` 显式标为 indirect；它们可能随目标平台和构建标签进入静态 Go 二进制。由于盘点环境没有可执行的 `go` 命令，未运行 `go list -m all`、`go list -deps` 或二进制符号/构建信息检查，因此下表是“清单与本地源码许可证已核对”，不是“每个目标均确认已链接”。

| 模块 | 版本 | `go.mod` 类型 | 本地许可证结论 | 发布时收集的原始文件 |
| --- | --- | --- | --- | --- |
| `golang.org/x/crypto` | `v0.42.0` | direct | BSD-3-Clause | `LICENSE` |
| `modernc.org/sqlite` | `v1.38.2` | direct | BSD-3-Clause；SQLite 可交付代码公共领域声明 | `LICENSE`, `SQLITE-LICENSE` |
| `github.com/dustin/go-humanize` | `v1.0.1` | indirect | MIT | `LICENSE` |
| `github.com/google/uuid` | `v1.6.0` | indirect | BSD-3-Clause | `LICENSE` |
| `github.com/mattn/go-isatty` | `v0.0.20` | indirect | MIT | `LICENSE` |
| `github.com/ncruces/go-strftime` | `v0.1.9` | indirect | MIT | `LICENSE` |
| `github.com/remyoudompheng/bigfft` | `v0.0.0-20230129092748-24d4a6f8daec` | indirect | BSD-3-Clause | `LICENSE` |
| `golang.org/x/exp` | `v0.0.0-20250620022241-b7579e27df2b` | indirect | BSD-3-Clause | `LICENSE` |
| `golang.org/x/sys` | `v0.36.0` | indirect | BSD-3-Clause | `LICENSE` |
| `modernc.org/libc` | `v1.66.3` | indirect | BSD-3-Clause；内含 Go Authors BSD-3-Clause 与 Dominik Honnef MIT 材料 | `LICENSE`, `LICENSE-GO`, `honnef.co/go/netdb/LICENSE` |
| `modernc.org/mathutil` | `v1.7.1` | indirect | BSD-3-Clause；内含 mersenne BSD-3-Clause 材料 | `LICENSE`, `mersenne/LICENSE` |
| `modernc.org/memory` | `v1.11.0` | indirect | BSD-3-Clause；内含 Go Authors 和 mmap-go BSD-3-Clause 材料 | `LICENSE`, `LICENSE-GO`, `LICENSE-MMAP-GO` |

模块公开来源可由精确模块路径和版本在 `https://pkg.go.dev/<module>@<version>` 核对。发布包应使用本次固定版本实际下载内容中的原始许可证文本，不能只放 SPDX 名称或本表摘要。

`modernc.org/memory/LICENSE-LOGO` 只指向项目 logo 的来源；当前没有证据表明该图形进入服务二进制。`modernc.org/libc` 的 crlibm `COPYING` 位于 `testdata`，当前没有证据表明其进入运行时二进制。若发布源码、vendor 树、SDK 或测试资料，必须重新纳入这两项及所有递归文件。

`go.sum` 中有 13 个当前未列在 `go.mod` 的模块：`github.com/google/pprof`、`golang.org/x/mod`、`golang.org/x/sync`、`golang.org/x/tools`、`modernc.org/cc/v4`、`modernc.org/ccgo/v4`、`modernc.org/fileutil`、`modernc.org/gc/v2`、`modernc.org/goabi0`、`modernc.org/opt`、`modernc.org/sortutil`、`modernc.org/strutil`、`modernc.org/token`。它们保留为发布前由 Go 工具解析模块图时的复核候选，而不是在缺少构建证据时宣称已经链接。

## 3. 前端直接依赖的用途与许可证

`package.json` 把 Vite、插件和 TypeScript 放在 `dependencies`，但其代码用途仍是构建工具；字段位置本身不意味着它们进入浏览器输出。

| 包 | 版本 | 实际用途 | 本地许可证结论 | 当前运行时分发判断 |
| --- | --- | --- | --- | --- |
| `react` | `19.3.0` | 浏览器运行时 | MIT，存在 `LICENSE` | 已在压缩 JS 中识别，应随发行物提供原始 MIT 文本 |
| `react-dom` | `19.3.0` | 浏览器运行时 | MIT，存在 `LICENSE` | 已在压缩 JS 中识别，应随发行物提供原始 MIT 文本 |
| `scheduler` | `0.28.0` | React DOM 间接运行时 | MIT，存在 `LICENSE` | 已在压缩 JS 中识别，应随发行物提供原始 MIT 文本 |
| `@vitejs/plugin-react` | `6.1.1` | 构建 | MIT | 未识别为静态输出运行时代码 |
| `vite` | `8.3.0` | 构建 | MIT | 未识别为静态输出运行时代码 |
| `typescript` | `7.0.2` | 编译/类型检查 | Apache-2.0，存在 `LICENSE` 与 `NOTICE.txt` | 未识别为静态输出运行时代码；若分发工具链须保留二者 |
| `@testing-library/jest-dom` | `7.0.1` | 测试 | MIT | 不应进入生产输出；最终产物仍须验证 |
| `@testing-library/react` | `16.3.3` | 测试 | MIT | 不应进入生产输出；最终产物仍须验证 |
| `@testing-library/user-event` | `14.6.7` | 测试 | MIT | 不应进入生产输出；最终产物仍须验证 |
| `@types/react` | `19.3.0` | 编译/类型 | MIT | 不应进入生产输出；最终产物仍须验证 |
| `@types/react-dom` | `19.2.3` | 编译/类型 | MIT | 不应进入生产输出；最终产物仍须验证 |
| `jsdom` | `30.1.1` | 测试 | MIT | 不应进入生产输出；最终产物仍须验证 |
| `vitest` | `5.0.1` | 测试 | MIT | 不应进入生产输出；最终产物仍须验证 |

npm registry 的精确版本元数据可由 `https://registry.npmjs.org/<name>/<version>` 核对（scope 包名需 URL 编码）；许可证交付仍以锁定包实际携带的原始文件为准。

## 4. 已落盘前端组件的许可证分类

以下分类来自 108 个本地组件版本的 `package.json` license 字段，并用包目录中的许可证文件做交叉检查。它便于发现异常，不替代逐包原文。

### Apache-2.0（7）

`@typescript/typescript-win32-x64@7.0.2`, `aria-query@5.3.0`, `aria-query@5.3.2`, `detect-libc@2.1.2`, `expect-type@1.4.0`, `typescript@7.0.2`, `xml-name-validator@5.0.0`

### BlueOak-1.0.0（1）

`lru-cache@11.5.3`

### BSD-2-Clause（2）

`entities@8.1.0`, `webidl-conversions@8.0.1`

### BSD-3-Clause（2）

`source-map-js@1.2.1`, `tough-cookie@6.0.2`

### CC0-1.0（1）

`mdn-data@2.27.1`

### ISC（3）

`picocolors@1.1.1`, `saxes@6.0.0`, `siginfo@2.0.0`

### MIT-0（2）

`@csstools/color-helpers@6.1.1`, `@csstools/css-syntax-patches-for-csstree@1.1.14`

### MPL-2.0（2）

`lightningcss@1.33.0`, `lightningcss-win32-x64-msvc@1.33.0`

这两项在当前安装中属于构建工具链。当前静态输出中未识别其运行时代码；这不等于对任何未来构建产物作保证。若分发 `node_modules`、构建容器或修改后的 MPL covered files，应单独验证 MPL-2.0 的通知、源码提供和文件级义务。

### MIT（88）

`@adobe/css-tools@4.5.0`, `@asamuzakjp/css-color@7.0.1`, `@asamuzakjp/dom-selector@9.2.1`, `@babel/code-frame@7.29.7`, `@babel/helper-validator-identifier@7.29.7`, `@babel/runtime@7.29.7`, `@bramus/specificity@2.4.2`, `@csstools/css-calc@3.4.0`, `@csstools/css-color-parser@4.2.3`, `@csstools/css-parser-algorithms@4.0.0`, `@csstools/css-tokenizer@4.0.1`, `@exodus/bytes@1.15.2`, `@jridgewell/resolve-uri@3.1.2`, `@jridgewell/sourcemap-codec@1.6.0`, `@jridgewell/trace-mapping@0.3.31`, `@oxc-project/types@0.150.0`, `@rolldown/binding-win32-x64-msvc@1.2.9`, `@rolldown/pluginutils@1.0.1`, `@testing-library/dom@10.4.2`, `@testing-library/jest-dom@7.0.1`, `@testing-library/react@16.3.3`, `@testing-library/user-event@14.6.7`, `@types/aria-query@5.0.4`, `@types/chai@5.2.3`, `@types/deep-eql@4.0.2`, `@types/estree@1.0.9`, `@types/react@19.3.0`, `@types/react-dom@19.2.3`, `@vitejs/plugin-react@6.1.1`, `@vitest/mocker@5.0.1`, `@vitest/spy@5.0.1`, `ansi-regex@5.0.1`, `ansi-styles@5.2.0`, `assertion-error@2.0.1`, `bidi-js@1.1.0`, `chai@6.2.2`, `css-tree@3.2.1`, `css.escape@1.5.1`, `csstype@3.2.3`, `data-urls@7.0.0`, `decimal.js@10.6.0`, `dequal@2.0.3`, `dom-accessibility-api@0.5.16`, `dom-accessibility-api@0.6.3`, `es-module-lexer@2.3.2`, `estree-walker@3.0.3`, `fdir@6.5.0`, `html-encoding-sniffer@7.0.0`, `indent-string@4.0.0`, `is-potential-custom-element-name@1.0.1`, `js-tokens@4.0.0`, `jsdom@30.1.1`, `lz-string@1.5.0`, `magic-string@1.4.1`, `min-indent@1.0.1`, `nanoid@3.3.19`, `obug@2.2.1`, `parse5@8.0.1`, `picomatch@4.0.7`, `postcss@8.5.28`, `pretty-format@27.5.1`, `punycode@2.3.1`, `react@19.3.0`, `react-dom@19.3.0`, `react-is@17.0.2`, `redent@3.0.0`, `require-from-string@2.0.2`, `rolldown@1.2.9`, `scheduler@0.28.0`, `stackback@0.0.2`, `std-env@4.2.0`, `strip-indent@3.0.0`, `tinybench@6.1.4`, `tinyexec@1.3.0`, `tinyglobby@0.2.17`, `tldts@7.4.14`, `tldts-core@7.4.14`, `tr46@6.0.0`, `undici@8.11.0`, `vite@8.3.0`, `vitest@5.0.1`, `vscode-jsonrpc@9.0.0`, `w3c-xmlserializer@6.0.0`, `whatwg-mimetype@5.0.0`, `whatwg-url@16.0.1`, `whatwg-url@17.1.2`, `why-is-node-running@2.3.0`, `xmlchars@2.2.0`

其中 `vscode-jsonrpc@9.0.0` 是 TypeScript 包内 vendored material，不是独立的 `bun.lock` 项；其 `License.txt` 仍应在重新分发完整 TypeScript 工具链时保留。

## 5. 未物化或许可证原文不完整的项目

### 5.1 锁定但本机未物化的 44 项

这些条目均为平台可选依赖，不能只依据同系列已安装包推定每个发行包的法律文本：

| 锁文件家族 | 数量 | 未核实范围 |
| --- | ---: | --- |
| `@rolldown/binding-*` | 14 | 除 Windows x64 外的 Android、Darwin、FreeBSD、Linux、OpenHarmony、Windows ARM64 等目标 |
| `@typescript/typescript-*` | 19 | 除 Windows x64 外的 AIX、Darwin、FreeBSD、Linux、NetBSD、OpenBSD、SunOS、Windows ARM64 等目标 |
| `lightningcss-*` | 10 | 除 Windows x64 外的 Android、Darwin、FreeBSD、Linux、Windows ARM64 等目标 |
| `fsevents@2.3.3` | 1 | macOS 可选依赖 |

发布 Linux、macOS、ARM 或其他目标前，必须在相应目标重新按锁文件安装，读取每个实际包的 manifest、许可证和 NOTICE，不把本机缺失当作“不需要声明”。

### 5.2 本机 manifest 有声明、但包目录缺少许可证文件的 6 项

| 包 | manifest 声明 | 证据缺口 |
| --- | --- | --- |
| `@rolldown/binding-win32-x64-msvc@1.2.9` | MIT | 包目录没有许可证文件；需从精确版本上游取得原文 |
| `css.escape@1.5.1` | MIT | 同上 |
| `is-potential-custom-element-name@1.0.1` | MIT | 同上 |
| `punycode@2.3.1` | MIT | 同上 |
| `saxes@6.0.0` | ISC | 同上 |
| `stackback@0.0.2` | MIT | 同上 |

这些 license identifier 足以做风险分类，但不足以生成包含正确版权行的最终通知文件。

## 6. 发布包必须携带的内容

针对当前二进制 + 静态网页形态，最低发布门禁是：

1. 附带本次实际链接的所有 Go 模块原始许可证文本，以及 `modernc.org/sqlite/SQLITE-LICENSE` 和实际编译到目标平台的嵌入材料许可证。
2. 附带 React、React DOM、Scheduler 的原始 MIT 许可证/版权文本；不能依赖当前压缩 JS，因为它没有保留许可证注释。
3. 若交付包含 TypeScript 或其原生包，除 Apache-2.0 `LICENSE` 外还要保留 `NOTICE.txt`；其中 vendored `vscode-jsonrpc` 的 `License.txt` 也应保留。
4. 若交付包含 `lightningcss`、Rolldown、Vite、Vitest、jsdom、Testing Library、`node_modules` 或构建镜像，应从最终安装树收集它们及全部传递依赖的原始许可证/NOTICE。MPL-2.0 组件另做 covered-file/source-offer 复核。
5. 声明文件必须与最终 OS/architecture、Go module graph、前端锁文件和实际产物匹配；清单中存在未知许可证或缺失原文时发布失败。

根目录 `THIRD_PARTY_NOTICES.md` 现在是组件索引和门禁说明，并未嵌入所有完整许可证文本。发布流程仍需生成一个随制品分发的完整 third-party-license bundle；在该制品存在并通过目标平台核对前，不能声称许可证声明已经完整。

## 7. 结论边界

- 可以确认：仓库已经包含真实 Go 与前端依赖；原先“planning documents only / no runtime dependencies”的描述已经过时。
- 可以确认：当前浏览器产物包含 React/React DOM/Scheduler，Go 源码直接使用 x/crypto 与 modernc SQLite。
- 尚不能确认：12 个 Go 清单模块在每个目标上分别是否链接、44 个未物化平台包的精确随包文本、六个缺原始许可证文件的 npm 包的最终版权行、未来安装器/容器会包含哪些构建工具。
- 因此本盘点建立发布门禁和证据清单，但不作“无许可证问题”或“已满足所有义务”的保证。
