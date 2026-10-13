# MCP Shell 品牌素材

使用内置 imagegen 生成，日期：2026-10-13。图标以终端提示符和连接节点为主题，配色为深青、薄荷绿和珠白。

- `app-icon-master.png`：带透明边距的原始图标。
- `app-icon-1024.png` / `app-icon-256.png`：应用图标 PNG 导出。
- `app.icns` / `app.iconset/`：macOS 图标及各分辨率资源；`build-gui-macos.sh` 已引用。
- `wordmark-light.png`：透明背景横向 Logo，深色文字，适用于浅色背景。
- `banner.png`：深色横幅，用于 README、发布说明或启动页素材。

衍生尺寸通过 macOS sips 导出，ICNS 使用 iconutil 封装；保留原始生成文件。上述生成素材为位图。

`tray-terminal.svg` 是菜单栏专用的单色终端标志，可编辑矢量素材。运行时代码在 `gui/src/tray.rs` 以 2 倍分辨率绘制同款图形，并启用 macOS 模板图标，自动适配深浅菜单栏。停止时减淡，运行时实心显示，处理请求时增加第二条光标线；具体状态也显示在提示文字和菜单中。

## 生成提示词

### icon

```text
Use case: logo-brand. Asset type: production macOS desktop application icon for MCP Shell, a local developer terminal and Model Context Protocol connection utility. Create ONE polished square app icon, 1024x1024 or higher, front-facing orthographic, perfectly centered. A substantial softly rounded square tile in deep midnight teal, with restrained turquoise edge illumination, subtle satin glass and anodized metal depth. The unique main symbol is a large bold pearl-white terminal chevron > on the left and a short mint-white underscore on the lower right; the underscore connects to a tiny elegant three-node branching circuit in the upper-right, communicating a shell connecting tools. Keep the whole symbol simple, heavy, geometrically balanced, highly recognizable at 32 pixels. The terminal chevron must dominate. Use luminous seafoam highlights, soft beveled edges, premium native macOS craftsmanship, modest dimensionality rather than cartoon 3D. Icon tile occupies about 84% of the canvas, with generous even transparent margins, very subtle close shadow only. Background outside the rounded square MUST be truly transparent. No letters, no wordmark, no app name, no extra symbols, no Apple logo, no grid, no mockup, no device, no presentation board, no wallpaper. Deliver a single isolated final icon, not multiple options.
```

### wordmark

```text
Use case: logo-brand. Asset type: transparent horizontal brand lockup for MCP Shell desktop developer utility. The input is the approved icon design and visual identity reference. Create a companion logo asset: retain a visually matching compact midnight teal rounded-square icon with its pearl-white terminal chevron and mint three-node branching underscore at the left, and the exact words "MCP Shell" to the right. Use clean refined medium-weight contemporary sans-serif typography, perfectly spelled, capital M C P, capital S then lowercase h e l l. Wordmark in solid deep midnight teal, for use on white/light backgrounds. Horizontal composition approximately 3:1 with balanced optical spacing, icon around the height of two capital letters. Match the original icon silhouette, terminal glyph and circuit identity faithfully. Keep the type flat and crisp, only the icon has the subtle satin glass depth of the reference. Truly transparent background, no white rectangle, no grid, no tagline, no additional text, no mockup or presentation sheet. One final logo asset.
```

### banner

```text
Use case: ads-marketing. Asset type: finished landscape brand banner for MCP Shell, suitable for README header, product introduction and splash artwork. Use the provided icon as the exact visual identity reference: midnight teal rounded-square tile, luminous pearl-white terminal chevron and mint three-node branching underscore. Make a sophisticated 16:9 landscape composition, large matching icon on the right half, clean left-aligned exact title "MCP Shell" on the left half in refined white sans-serif typography, with abundant breathing room. Deep midnight teal background with a very subtle soft turquoise atmospheric glow behind the icon; three fine curving connection paths emerge gently into the background and terminate in small quiet nodes, suggesting connected local tools. Restrained premium desktop developer software aesthetic, crisp contours, legibility, subtle satin glass dimensionality; no neon chaos, no purple, no excessive bloom, no code snippets, no tiny text. ONLY text: "MCP Shell". No subtitle, watermark, device mockup, border or extra logo. A standalone usable full-bleed landscape banner, not a brand guidelines board.
```
