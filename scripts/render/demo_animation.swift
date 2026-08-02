import AppKit

// Render the context-menu demo animation for the README.
//
// Two panes side by side - Finder on the left, File Explorer on the right -
// running the same beats in parallel: select a .prproj, open the context menu,
// step into the second-level menu, pick "Downgrade for older Premiere", and
// watch the downgraded copy appear alongside the untouched original.
//
// Developer tool, not part of the build: the output is committed, so this only
// needs re-running when the artwork or the menu wording changes.
//
// The Premiere document icon is read from a locally installed Premiere Pro at
// render time and is deliberately NOT committed to this repository; without it
// the script falls back to a generic document glyph, so a contributor with no
// Premiere install can still run a preview.
//
// Usage: swift demo_animation.swift   (requires ffmpeg: brew install ffmpeg)

// MARK: - Config

let projectRoot =
    URL(fileURLWithPath: #filePath)
    .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
let outputGif = projectRoot.appendingPathComponent("docs/assets/prem-down_demo.gif").path
let quickActionIconPath = projectRoot.appendingPathComponent(
    "internal/integrate/workflowCustomImageTemplate.tiff"
).path
let windowsIconPath = projectRoot.appendingPathComponent("packaging/windows/winres/icon32.png").path

// Premiere's own document icon, from wherever it happens to be installed. The
// newest release wins, so the demo matches what the user is most likely to see.
let premiereIconPath: String? = {
    let apps = (try? FileManager.default.contentsOfDirectory(atPath: "/Applications")) ?? []
    let versions = apps.filter { $0.hasPrefix("Adobe Premiere Pro ") }.sorted().reversed()
    for app in versions {
        let icns = "/Applications/\(app)/\(app).app/Contents/Resources/pr_proj_primary.icns"
        if FileManager.default.fileExists(atPath: icns) { return icns }
    }
    return nil
}()

// Scratch dir for the intermediate PNG frames + ffmpeg concat manifest.
let outputDir = NSTemporaryDirectory() + "prem-down-demo-\(ProcessInfo.processInfo.processIdentifier)"
do {
    try FileManager.default.createDirectory(atPath: outputDir, withIntermediateDirectories: true)
} catch {
    fatalError("failed to create scratch dir \(outputDir): \(error)")
}

// The layout is authored in these logical points. renderScale draws each frame
// at 3x, and ffmpeg downsamples to the committed asset.
let canvasWidth: CGFloat = 1248
let canvasHeight: CGFloat = 450
let renderScale: CGFloat = 3

// MARK: - Colour + image helpers

func color(hex: UInt32, alpha: CGFloat = 1) -> NSColor {
    NSColor(
        srgbRed: CGFloat((hex >> 16) & 0xff) / 255,
        green: CGFloat((hex >> 8) & 0xff) / 255,
        blue: CGFloat(hex & 0xff) / 255,
        alpha: alpha)
}

func loadImage(_ path: String) -> NSImage? { NSImage(contentsOfFile: path) }

/// Recolours an image's opaque pixels, for template artwork that the OS would
/// tint itself (the Finder Quick Action icon).
func tinted(_ image: NSImage, _ tint: NSColor, size: NSSize) -> NSImage {
    let result = NSImage(size: size)
    result.lockFocus()
    tint.set()
    NSRect(origin: .zero, size: size).fill()
    image.draw(in: NSRect(origin: .zero, size: size), from: .zero, operation: .destinationIn, fraction: 1)
    result.unlockFocus()
    return result
}

let premiereIcon = premiereIconPath.flatMap(loadImage)
let quickActionIcon = loadImage(quickActionIconPath)
let windowsIcon = loadImage(windowsIconPath)

if premiereIcon == nil {
    print("note: no Premiere Pro install found - falling back to a generic document glyph")
}

// MARK: - Fonts
//
// The Explorer pane is set in Segoe UI. Without it the render still completes
// on the system font, but the result is a usable preview rather than a
// publishable asset.

func windowsFont(_ size: CGFloat, semibold: Bool = false, bold: Bool = false) -> NSFont {
    let name = bold ? "SegoeUI-Bold" : (semibold ? "SegoeUI-Semibold" : "SegoeUI")
    if let font = NSFont(name: name, size: size) { return font }
    return NSFont.systemFont(ofSize: size, weight: bold ? .bold : (semibold ? .semibold : .regular))
}

func macFont(_ size: CGFloat, semibold: Bool = false) -> NSFont {
    NSFont.systemFont(ofSize: size, weight: semibold ? .semibold : .regular)
}

func warn(_ message: String) {
    FileHandle.standardError.write(Data(message.utf8))
}

// Checked up front, so the warning is the first thing on screen rather than
// something scrolled off above the render output.
let missingWeights = ["SegoeUI", "SegoeUI-Semibold", "SegoeUI-Bold"]
    .filter { NSFont(name: $0, size: 12) == nil }
if !missingWeights.isEmpty {
    warn(
        """

        ############################################################
        WARNING: Segoe UI is not installed (missing \(missingWeights.joined(separator: ", "))).
        The Explorer pane will fall back to the system font.
        Install segoeui.ttf, seguisb.ttf and segoeuib.ttf into
        ~/Library/Fonts and re-run before committing the result.
        ############################################################


        """)
}

// MARK: - Drawing primitives (top-left logical coords)

func topRect(_ leftX: CGFloat, _ topY: CGFloat, _ width: CGFloat, _ height: CGFloat) -> NSRect {
    NSRect(x: leftX, y: canvasHeight - topY - height, width: width, height: height)
}

func roundedRect(_ rect: NSRect, radius: CGFloat) -> NSBezierPath {
    NSBezierPath(roundedRect: rect, xRadius: radius, yRadius: radius)
}

func textWidth(_ text: String, font: NSFont) -> CGFloat {
    NSAttributedString(string: text, attributes: [.font: font]).size().width
}

/// Draws `text` with its vertical centre at `centreY` (top-left coords).
func drawText(_ text: String, font: NSFont, color textColor: NSColor, leftX: CGFloat, centreY: CGFloat) {
    let attributed = NSAttributedString(
        string: text, attributes: [.font: font, .foregroundColor: textColor])
    let size = attributed.size()
    attributed.draw(at: NSPoint(x: leftX, y: canvasHeight - centreY - size.height / 2))
}

func fill(_ rect: NSRect, _ fillColor: NSColor) {
    fillColor.setFill()
    rect.fill()
}

/// A hairline. Rects are pixel-snapped by the 3x supersample, so a 1pt line is
/// safe to draw as a filled rect.
func hairline(x: CGFloat, y: CGFloat, width: CGFloat, _ lineColor: NSColor) {
    fill(topRect(x, y, width, 1), lineColor)
}

func symbol(_ name: String, pointSize: CGFloat, weight: NSFont.Weight = .regular) -> NSImage? {
    guard let base = NSImage(systemSymbolName: name, accessibilityDescription: nil) else { return nil }
    return base.withSymbolConfiguration(.init(pointSize: pointSize, weight: weight))
}

func drawSymbol(
    _ name: String, centreX: CGFloat, centreY: CGFloat, pointSize: CGFloat,
    _ tint: NSColor, weight: NSFont.Weight = .regular, alpha: CGFloat = 1
) {
    guard let image = symbol(name, pointSize: pointSize, weight: weight) else { return }
    let coloured = tinted(image, tint, size: image.size)
    let rect = NSRect(
        x: centreX - image.size.width / 2, y: canvasHeight - centreY - image.size.height / 2,
        width: image.size.width, height: image.size.height)
    coloured.draw(in: rect, from: .zero, operation: .sourceOver, fraction: alpha)
}

func drawImage(_ image: NSImage, x: CGFloat, centreY: CGFloat, size: CGFloat, alpha: CGFloat = 1) {
    image.draw(
        in: NSRect(x: x, y: canvasHeight - centreY - size / 2, width: size, height: size),
        from: .zero, operation: .sourceOver, fraction: alpha)
}

func withShadow(_ blur: CGFloat, _ opacity: CGFloat, _ offsetY: CGFloat, _ body: () -> Void) {
    NSGraphicsContext.saveGraphicsState()
    let shadow = NSShadow()
    shadow.shadowColor = color(hex: 0, alpha: opacity)
    shadow.shadowBlurRadius = blur
    shadow.shadowOffset = NSSize(width: 0, height: -offsetY)
    shadow.set()
    body()
    NSGraphicsContext.restoreGraphicsState()
}

// MARK: - Palette

enum Platform { case mac, windows }

// macOS light
let macWindowBg = color(hex: 0xffffff)
let macSidebarBg = color(hex: 0xf1f0ef)
let macToolbarBg = color(hex: 0xf6f5f4)
let macBorder = color(hex: 0xd6d4d2)
let macHairline = color(hex: 0xe4e2e0)
let macText = color(hex: 0x1d1d1f)
let macDimText = color(hex: 0x8a8a8e)
let macAccent = color(hex: 0x0a63ff)
let macMenuBg = color(hex: 0xf8f7f7, alpha: 0.985)

// Windows 11 light
let winWindowBg = color(hex: 0xffffff)
let winChromeBg = color(hex: 0xf3f3f3)
let winNavBg = color(hex: 0xfbfbfb)
let winBorder = color(hex: 0xe2e0de)
let winHairline = color(hex: 0xebe9e7)
let winText = color(hex: 0x1a1a1a)
let winDimText = color(hex: 0x616161)
let winSelectBg = color(hex: 0xe9f1fb)
let winAccent = color(hex: 0x0067c0)
let winMenuBg = color(hex: 0xf7f6f5, alpha: 0.99)
let winMenuHover = color(hex: 0xe6e5e4)

// Page background - a soft neutral so both window chromes read as windows.
let pageTop = color(hex: 0xdfe3ea)
let pageBottom = color(hex: 0xc9cfd9)

// MARK: - Layout

let windowTopY: CGFloat = 38
let windowHeight: CGFloat = 390
let windowWidth: CGFloat = 576
let sidebarWidth: CGFloat = 132
let rowHeight: CGFloat = 27

struct Pane {
    let x: CGFloat
    let platform: Platform
    let caption: String

    var contentX: CGFloat { x + sidebarWidth }
    var right: CGFloat { x + windowWidth }
    /// Top of the file list, below the chrome (unified toolbar on macOS, title
    /// bar plus address bar on Windows) and the column header.
    var listTopY: CGFloat {
        windowTopY + (platform == .mac ? 38 : 34 + 34) + 24
    }
}

let macPane = Pane(x: 24, platform: .mac, caption: "macOS")
let winPane = Pane(x: 648, platform: .windows, caption: "Windows")

enum RowKind { case project, folder }
struct FileRow {
    let name: String
    let kind: RowKind
    let modified: String
    /// The downgraded copy, which only exists after the action has run.
    var isResult = false
}

let targetName = "MyProject.prproj"
let resultName = "MyProject_downgraded.prproj"

// Folders sort above files, as both shells do by default.
let baseRows: [FileRow] = [
    FileRow(name: "MyProduction", kind: .folder, modified: "28 Jul 2026"),
    FileRow(name: targetName, kind: .project, modified: "31 Jul 2026"),
    FileRow(name: "Titles_v3.prproj", kind: .project, modified: "2 Jun 2026"),
]
let targetIndex = 1
let resultRow = FileRow(name: resultName, kind: .project, modified: "now", isResult: true)

// MARK: - Menu model

struct MenuItem {
    var title = ""
    var separator = false
    var submenu = false
    var symbol: String?
    /// Right-aligned accelerator hint, as the Windows 11 menu shows.
    var shortcut: String?
    /// The default verb, which the legacy Windows menu sets in bold.
    var bold = false
    /// Our own action - drawn with the prem-down icon, as both shells do.
    var ours = false

    static let sep = MenuItem(separator: true)
}

// Kept deliberately short: the widest item sets the menu width, which is what
// pushes the Quick Actions submenu rightwards - and it has to stay clear of the
// Explorer pane.
// Finder gives the middle groups icons but leaves Open, Open With and Quick
// Actions bare; the labels still share one left edge, so the bare rows keep the
// empty gutter. Compress and Make Alias are dropped - Compress carries the
// filename, which would make it the widest row and shove the submenu into the
// Explorer pane.
let macMenuItems: [MenuItem] = [
    MenuItem(title: "Open"),
    MenuItem(title: "Open With", submenu: true),
    .sep,
    MenuItem(title: "Move to Bin", symbol: "trash"),
    .sep,
    MenuItem(title: "Get Info", symbol: "info.circle"),
    MenuItem(title: "Rename", symbol: "pencil"),
    MenuItem(title: "Quick Look", symbol: "eye"),
    .sep,
    MenuItem(title: "Copy", symbol: "doc.on.doc"),
    .sep,
    MenuItem(title: "Quick Actions", submenu: true),
]
let macQuickActionsIndex = 11

// For a .prproj this is the whole submenu: the rotate/markup actions Finder
// offers are image-only, so ours and Customise… are all that is left.
let macSubmenuItems: [MenuItem] = [
    MenuItem(title: "Downgrade for older Premiere", ours: true),
    MenuItem(title: "Customise…"),
]
let macOurItemIndex = 0

// Windows 11's modern menu gives every row an accent-coloured icon, and hangs
// accelerator hints off the right edge.
let winMenuItems: [MenuItem] = [
    MenuItem(title: "Open", symbol: "doc.text.fill", shortcut: "Enter"),
    MenuItem(title: "Open with", submenu: true, symbol: "square.grid.2x2.fill"),
    MenuItem(title: "Properties", symbol: "wrench.and.screwdriver.fill", shortcut: "Alt+Enter"),
    .sep,
    MenuItem(title: "Show more options", symbol: "list.bullet.rectangle.portrait.fill"),
]
let winShowMoreIndex = 4

// The legacy menu leaves its own verbs bare and only shows icons for shell
// extensions - which is exactly what our entry is, so it sits in the opening
// group with Open rather than in one of its own.
let winLegacyItems: [MenuItem] = [
    MenuItem(title: "Open", bold: true),
    MenuItem(title: "Downgrade for older Premiere", ours: true),
    .sep,
    MenuItem(title: "Cut"),
    MenuItem(title: "Copy"),
    MenuItem(title: "Paste"),
    .sep,
    MenuItem(title: "Delete"),
    MenuItem(title: "Rename"),
    .sep,
    MenuItem(title: "Properties"),
]
let winOurItemIndex = 1

let separatorHeight: CGFloat = 9
let macMenuRowHeight: CGFloat = 23
/// The modern Windows 11 menu is noticeably airier than the legacy one it
/// hands off to.
let winModernRowHeight: CGFloat = 28
let winLegacyRowHeight: CGFloat = 26
/// Height of the labelled command strip Windows 11 puts at the top of its
/// modern menu.
let winCommandBarHeight: CGFloat = 56

func menuHeight(_ items: [MenuItem], rowHeight: CGFloat, extraTop: CGFloat = 0) -> CGFloat {
    var total = 10 + extraTop
    for item in items { total += item.separator ? separatorHeight : rowHeight }
    return total
}

func menuWidth(
    _ items: [MenuItem], font: NSFont, gutter: CGFloat, trailing: CGFloat, shortcutFont: NSFont? = nil
) -> CGFloat {
    var widest: CGFloat = 0
    var widestShortcut: CGFloat = 0
    for item in items where !item.separator {
        widest = max(widest, textWidth(item.title, font: font))
        if let shortcut = item.shortcut, let shortcutFont {
            widestShortcut = max(widestShortcut, textWidth(shortcut, font: shortcutFont))
        }
    }
    return widest + gutter + trailing + (widestShortcut > 0 ? widestShortcut + 26 : 0)
}

/// Top offset of item `index` within a menu whose box starts at `topY`.
func menuItemTop(_ items: [MenuItem], index: Int, topY: CGFloat, rowHeight: CGFloat, extraTop: CGFloat = 0)
    -> CGFloat
{
    var offset = topY + 5 + extraTop
    for item in items.prefix(index) { offset += item.separator ? separatorHeight : rowHeight }
    return offset
}

// MARK: - Icons

/// The Premiere project document icon, or a hand-drawn stand-in when Premiere
/// is not installed locally.
func drawProjectIcon(x: CGFloat, centreY: CGFloat, size: CGFloat, alpha: CGFloat = 1) {
    if let icon = premiereIcon {
        drawImage(icon, x: x, centreY: centreY, size: size, alpha: alpha)
        return
    }
    let rect = NSRect(x: x, y: canvasHeight - centreY - size / 2, width: size * 0.82, height: size)
    color(hex: 0x2f0d4e, alpha: alpha).setFill()
    roundedRect(rect, radius: size * 0.14).fill()
    drawSymbol(
        "play.fill", centreX: rect.midX, centreY: centreY, pointSize: size * 0.42,
        color(hex: 0xc4a5ff, alpha: alpha))
}

func drawFolderIcon(x: CGFloat, centreY: CGFloat, size: CGFloat, platform: Platform, alpha: CGFloat = 1) {
    let tint = platform == .mac ? color(hex: 0x6da8dd, alpha: alpha) : color(hex: 0xe8b765, alpha: alpha)
    drawSymbol("folder.fill", centreX: x + size / 2, centreY: centreY, pointSize: size * 0.86, tint)
}

/// The prem-down icon as each shell shows it: tinted template artwork in the
/// Finder Quick Actions submenu, full colour in the Explorer legacy menu.
func drawOurIcon(x: CGFloat, centreY: CGFloat, size: CGFloat, platform: Platform, tint: NSColor) {
    if platform == .mac, let icon = quickActionIcon {
        // Template artwork follows the row's text colour, so it inverts on the
        // blue highlight exactly as Finder draws it.
        drawImage(tinted(icon, tint, size: NSSize(width: size, height: size)), x: x, centreY: centreY, size: size)
    } else if let icon = windowsIcon {
        drawImage(icon, x: x, centreY: centreY, size: size)
    }
}

// MARK: - Cursor

func drawCursor(at origin: NSPoint) {
    let points: [(CGFloat, CGFloat)] = [
        (0, 0), (0, 17), (4.6, 12.6), (7.8, 19.5), (10.4, 18.3), (7.2, 11.6), (12.4, 11.6),
    ]
    let unit: CGFloat = 0.92
    let path = NSBezierPath()
    for (index, point) in points.enumerated() {
        let vertex = NSPoint(x: origin.x + point.0 * unit, y: canvasHeight - (origin.y + point.1 * unit))
        if index == 0 { path.move(to: vertex) } else { path.line(to: vertex) }
    }
    path.close()
    path.lineJoinStyle = .round
    color(hex: 0, alpha: 0.3).setStroke()
    path.lineWidth = 4
    path.stroke()
    NSColor.white.setFill()
    path.fill()
    color(hex: 0x1a1a1a).setStroke()
    path.lineWidth = 1
    path.stroke()
}

// MARK: - Window chrome

func drawMacChrome(_ pane: Pane) {
    // Sidebar runs the full height behind the unified toolbar; the toolbar
    // strip only covers the content side.
    fill(topRect(pane.x, windowTopY, sidebarWidth, windowHeight), macSidebarBg)
    fill(topRect(pane.contentX, windowTopY, windowWidth - sidebarWidth, 38), macToolbarBg)
    hairline(x: pane.x, y: windowTopY + 38, width: windowWidth, macHairline)
    fill(topRect(pane.contentX, windowTopY, 1, windowHeight), macHairline)

    let lightY = windowTopY + 19
    let lights: [UInt32] = [0xff5f57, 0xfebc2e, 0x28c840]
    for (index, hex) in lights.enumerated() {
        color(hex: hex).setFill()
        NSBezierPath(
            ovalIn: NSRect(
                x: pane.x + 16 + CGFloat(index) * 19, y: canvasHeight - lightY - 5.5, width: 11, height: 11)
        ).fill()
    }

    let title = "Projects"
    drawText(
        title, font: macFont(13, semibold: true), color: macText,
        leftX: pane.contentX + (windowWidth - sidebarWidth - textWidth(title, font: macFont(13, semibold: true))) / 2,
        centreY: windowTopY + 19)

    // Sidebar: one section header and its items, enough to read as Finder.
    drawText("Favourites", font: macFont(10.5, semibold: true), color: macDimText, leftX: pane.x + 14, centreY: windowTopY + 54)
    let sidebarItems = [
        ("Documents", "doc.fill"), ("Pictures", "photo.fill"), ("Movies", "film.fill"),
        ("Projects", "folder.fill"),
    ]
    for (index, item) in sidebarItems.enumerated() {
        let centreY = windowTopY + 76 + CGFloat(index) * 24
        let selected = item.0 == "Projects"
        if selected {
            fill(topRect(pane.x + 8, centreY - 11, sidebarWidth - 16, 22), color(hex: 0x000000, alpha: 0.07))
        }
        drawSymbol(item.1, centreX: pane.x + 24, centreY: centreY, pointSize: 12, color(hex: 0x4a90d9))
        drawText(item.0, font: macFont(12), color: macText, leftX: pane.x + 38, centreY: centreY)
    }

    // Column header
    let headerY = windowTopY + 38 + 12
    drawText("Name", font: macFont(11), color: macDimText, leftX: pane.contentX + 14, centreY: headerY)
    drawText("Date Modified", font: macFont(11), color: macDimText, leftX: pane.right - 118, centreY: headerY)
    hairline(x: pane.contentX, y: windowTopY + 38 + 24, width: windowWidth - sidebarWidth, macHairline)
}

func drawWindowsChrome(_ pane: Pane) {
    fill(topRect(pane.x, windowTopY, windowWidth, 34), winChromeBg)

    // Title bar: a single tab plus the window controls.
    let tabWidth: CGFloat = 168
    fill(topRect(pane.x + 8, windowTopY + 5, tabWidth, 29), winWindowBg)
    drawSymbol("folder.fill", centreX: pane.x + 26, centreY: windowTopY + 19, pointSize: 12, color(hex: 0xe8b765))
    drawText("Projects", font: windowsFont(12), color: winText, leftX: pane.x + 40, centreY: windowTopY + 19)
    drawSymbol("xmark", centreX: pane.x + tabWidth - 8, centreY: windowTopY + 19, pointSize: 8, winDimText)
    for (index, name) in ["minus", "square", "xmark"].enumerated() {
        drawSymbol(
            name, centreX: pane.right - 76 + CGFloat(index) * 26, centreY: windowTopY + 17,
            pointSize: name == "square" ? 8.5 : 9, winText, alpha: 0.85)
    }

    // Address bar
    let addressY = windowTopY + 34
    fill(topRect(pane.x, addressY, windowWidth, 34), winWindowBg)
    for (index, name) in ["arrow.left", "arrow.right", "arrow.up"].enumerated() {
        drawSymbol(name, centreX: pane.x + 20 + CGFloat(index) * 25, centreY: addressY + 17, pointSize: 11, winDimText)
    }
    let field = topRect(pane.x + 96, addressY + 6, windowWidth - 200, 23)
    fill(field, color(hex: 0xf8f8f8))
    color(hex: 0xe3e1df).setStroke()
    let fieldBorder = roundedRect(field, radius: 4)
    fieldBorder.lineWidth = 1
    fieldBorder.stroke()
    drawSymbol("folder.fill", centreX: pane.x + 110, centreY: addressY + 17, pointSize: 10, color(hex: 0xe8b765))
    drawText(
        "Documents  ›  Projects", font: windowsFont(11.5), color: winText,
        leftX: pane.x + 122, centreY: addressY + 17)
    hairline(x: pane.x, y: addressY + 34, width: windowWidth, winHairline)

    // Navigation pane
    let navTop = addressY + 34
    fill(topRect(pane.x, navTop, sidebarWidth, windowTopY + windowHeight - navTop), winNavBg)
    fill(topRect(pane.contentX, navTop, 1, windowTopY + windowHeight - navTop), winHairline)
    let navItems = [
        ("Home", "house.fill"), ("Documents", "doc.fill"), ("Gallery", "photo.fill"),
        ("Projects", "folder.fill"),
    ]
    for (index, item) in navItems.enumerated() {
        let centreY = navTop + 20 + CGFloat(index) * 24
        if item.0 == "Projects" {
            fill(topRect(pane.x + 6, centreY - 11, sidebarWidth - 14, 22), color(hex: 0x000000, alpha: 0.045))
        }
        let navTint = item.0 == "Projects" ? color(hex: 0xe8b765) : color(hex: 0x8b8b8b)
        drawSymbol(item.1, centreX: pane.x + 22, centreY: centreY, pointSize: 11.5, navTint)
        drawText(item.0, font: windowsFont(11.5), color: winText, leftX: pane.x + 36, centreY: centreY)
    }

    // Column header
    let headerY = navTop + 12
    drawText("Name", font: windowsFont(11), color: winDimText, leftX: pane.contentX + 14, centreY: headerY)
    drawText("Date modified", font: windowsFont(11), color: winDimText, leftX: pane.right - 122, centreY: headerY)
    hairline(x: pane.contentX, y: navTop + 24, width: windowWidth - sidebarWidth, winHairline)
}

// MARK: - File list

func drawFileRows(_ pane: Pane, rows: [FileRow], selected: Int?, resultAlpha: CGFloat) {
    let mac = pane.platform == .mac
    let listWidth = windowWidth - sidebarWidth
    for (index, row) in rows.enumerated() {
        let top = pane.listTopY + CGFloat(index) * rowHeight
        let centreY = top + rowHeight / 2
        let alpha: CGFloat = row.isResult ? resultAlpha : 1
        if alpha <= 0 { continue }

        if selected == index {
            if mac {
                fill(topRect(pane.contentX + 4, top + 1, listWidth - 8, rowHeight - 2), macAccent.withAlphaComponent(0.92))
            } else {
                let rect = topRect(pane.contentX + 5, top + 1, listWidth - 10, rowHeight - 2)
                winSelectBg.setFill()
                roundedRect(rect, radius: 4).fill()
                fill(topRect(pane.contentX + 5, top + 6, 3, rowHeight - 12), winAccent)
            }
        } else if mac && index % 2 == 1 {
            // Finder's alternating list-view banding
            fill(topRect(pane.contentX, top, listWidth, rowHeight), color(hex: 0xf5f5f7))
        }

        let iconX = pane.contentX + 14
        switch row.kind {
        case .project: drawProjectIcon(x: iconX, centreY: centreY, size: 17, alpha: alpha)
        case .folder: drawFolderIcon(x: iconX, centreY: centreY, size: 17, platform: pane.platform, alpha: alpha)
        }

        let highlighted = selected == index && mac
        let nameColour = highlighted ? NSColor.white : (mac ? macText : winText)
        let dateColour = highlighted ? NSColor.white.withAlphaComponent(0.8) : (mac ? macDimText : winDimText)
        let font = mac ? macFont(12.5) : windowsFont(12)
        drawText(row.name, font: font, color: nameColour.withAlphaComponent(alpha), leftX: iconX + 25, centreY: centreY)
        drawText(
            row.modified, font: mac ? macFont(11.5) : windowsFont(11.5),
            color: dateColour.withAlphaComponent(alpha), leftX: pane.right - 118, centreY: centreY)
    }
}

// MARK: - Menus

func drawMenuBox(_ rect: NSRect, platform: Platform, radius: CGFloat) {
    withShadow(24, 0.22, 7) {
        (platform == .mac ? macMenuBg : winMenuBg).setFill()
        roundedRect(rect, radius: radius).fill()
    }
    color(hex: 0x000000, alpha: platform == .mac ? 0.12 : 0.10).setStroke()
    let border = roundedRect(rect, radius: radius)
    border.lineWidth = 1
    border.stroke()
}

func drawMenu(
    items: [MenuItem], leftX: CGFloat, topY: CGFloat, width: CGFloat, rowHeight: CGFloat,
    platform: Platform, hover: Int?, iconGutter: Bool, commandBar: Bool = false
) {
    let mac = platform == .mac
    let extraTop = commandBar ? winCommandBarHeight : 0
    let height = menuHeight(items, rowHeight: rowHeight, extraTop: extraTop)
    let radius: CGFloat = mac ? 7 : 8
    drawMenuBox(topRect(leftX, topY, width, height), platform: platform, radius: radius)

    if commandBar {
        // The strip Windows 11 puts at the top of the modern menu: an icon over
        // a label for each of the six clipboard/file verbs.
        let commands = [
            ("scissors", "Cut"), ("doc.on.doc", "Copy"), ("doc.on.clipboard", "Paste"),
            ("character.textbox", "Rename"), ("arrow.up.forward.square", "Share"), ("trash", "Delete"),
        ]
        let labelFont = windowsFont(9.5)
        let spacing = (width - 24) / CGFloat(commands.count)
        for (index, command) in commands.enumerated() {
            let centreX = leftX + 12 + spacing * (CGFloat(index) + 0.5)
            drawSymbol(command.0, centreX: centreX, centreY: topY + 20, pointSize: 12.5, winAccent)
            drawText(
                command.1, font: labelFont, color: winText,
                leftX: centreX - textWidth(command.1, font: labelFont) / 2, centreY: topY + 40)
        }
        hairline(x: leftX + 8, y: topY + winCommandBarHeight - 1, width: width - 16, color(hex: 0, alpha: 0.07))
    }

    let font = mac ? macFont(13) : windowsFont(12)
    let gutter: CGFloat = iconGutter ? 30 : 14
    var rowTop = topY + 5 + extraTop

    for (index, item) in items.enumerated() {
        if item.separator {
            hairline(x: leftX + 10, y: rowTop + separatorHeight / 2, width: width - 20, color(hex: 0, alpha: 0.10))
            rowTop += separatorHeight
            continue
        }
        let centreY = rowTop + rowHeight / 2
        let isHover = hover == index
        if isHover {
            let rect = topRect(leftX + 5, rowTop + 1, width - 10, rowHeight - 2)
            if mac {
                macAccent.setFill()
                roundedRect(rect, radius: 5).fill()
            } else {
                winMenuHover.setFill()
                roundedRect(rect, radius: 4).fill()
            }
        }
        let textColour = (isHover && mac) ? NSColor.white : (mac ? macText : winText)

        if item.ours {
            drawOurIcon(x: leftX + 9, centreY: centreY, size: 16, platform: platform, tint: textColour)
        } else if let name = item.symbol {
            // Windows draws its menu glyphs in the accent colour; Finder tints
            // them with the row's text colour.
            let iconTint = mac ? textColour : winAccent
            drawSymbol(name, centreX: leftX + 17, centreY: centreY, pointSize: 12.5, iconTint, alpha: mac ? 0.9 : 1)
        }
        let rowFont = item.bold ? windowsFont(12, bold: true) : font
        drawText(item.title, font: rowFont, color: textColour, leftX: leftX + gutter, centreY: centreY)

        if let shortcut = item.shortcut {
            let shortcutFont = windowsFont(11)
            drawText(
                shortcut, font: shortcutFont, color: winDimText,
                leftX: leftX + width - 14 - textWidth(shortcut, font: shortcutFont), centreY: centreY)
        }

        if item.submenu {
            let tipX = leftX + width - 13
            let path = NSBezierPath()
            let points: [(CGFloat, CGFloat)] = [(-3.4, -3.4), (0, 0), (-3.4, 3.4)]
            for (pointIndex, point) in points.enumerated() {
                let vertex = NSPoint(x: tipX + point.0, y: canvasHeight - (centreY + point.1))
                if pointIndex == 0 { path.move(to: vertex) } else { path.line(to: vertex) }
            }
            path.lineWidth = 1.4
            path.lineCapStyle = .round
            path.lineJoinStyle = .round
            textColour.withAlphaComponent(0.75).setStroke()
            path.stroke()
        }
        rowTop += rowHeight
    }
}

// MARK: - Menu geometry

let macMenuLeft = macPane.contentX + 46
let macMenuTop = macPane.listTopY + CGFloat(targetIndex) * rowHeight + 14
let macMenuW = menuWidth(macMenuItems, font: macFont(13), gutter: 30, trailing: 34)
let macSubmenuTop = menuItemTop(macMenuItems, index: macQuickActionsIndex, topY: macMenuTop, rowHeight: macMenuRowHeight) - 5
let macSubmenuLeft = macMenuLeft + macMenuW - 5
let macSubmenuW = menuWidth(macSubmenuItems, font: macFont(13), gutter: 30, trailing: 22)

let winMenuLeft = winPane.contentX + 46
let winMenuTop = winPane.listTopY + CGFloat(targetIndex) * rowHeight + 14
let winMenuW = menuWidth(
    winMenuItems, font: windowsFont(12), gutter: 30, trailing: 34, shortcutFont: windowsFont(11))
let winLegacyW = menuWidth(winLegacyItems, font: windowsFont(12), gutter: 30, trailing: 26)

// MARK: - Frame model

struct Frame {
    var selected = false
    /// 0 = none, 1 = first menu, 2 = second menu (Quick Actions / legacy)
    var menuLevel = 0
    /// Whether the row that opens the second menu is highlighted. The two
    /// shells put that row at different indices, so it is resolved per pane.
    var hoverStep = false
    /// Whether our own action is highlighted, likewise per pane.
    var hoverAction = false
    var resultAlpha: CGFloat = 0
    var cursorStage = CursorStage.row
    var durationMs: Int
}

enum CursorStage { case row, primary, secondary, result, hidden }

func cursorPoint(_ pane: Pane, _ stage: CursorStage) -> NSPoint? {
    let mac = pane.platform == .mac
    let rowCentre = pane.listTopY + CGFloat(targetIndex) * rowHeight + rowHeight / 2
    switch stage {
    case .hidden:
        return nil
    case .row:
        return NSPoint(x: pane.contentX + 48, y: rowCentre - 3)
    case .result:
        // Resting on the downgraded copy, so the eye lands on the payoff.
        return NSPoint(x: pane.contentX + 48, y: rowCentre + rowHeight - 3)
    case .primary:
        let items = mac ? macMenuItems : winMenuItems
        let index = mac ? macQuickActionsIndex : winShowMoreIndex
        let top = mac ? macMenuTop : winMenuTop
        let rowH = mac ? macMenuRowHeight : winModernRowHeight
        let extra = mac ? 0 : winCommandBarHeight
        let itemTop = menuItemTop(items, index: index, topY: top, rowHeight: rowH, extraTop: extra)
        let left = mac ? macMenuLeft : winMenuLeft
        return NSPoint(x: left + 42, y: itemTop + rowH / 2 - 3)
    case .secondary:
        if mac {
            let itemTop = menuItemTop(
                macSubmenuItems, index: macOurItemIndex, topY: macSubmenuTop, rowHeight: macMenuRowHeight)
            return NSPoint(x: macSubmenuLeft + 60, y: itemTop + macMenuRowHeight / 2 - 3)
        }
        let itemTop = menuItemTop(
            winLegacyItems, index: winOurItemIndex, topY: winMenuTop, rowHeight: winLegacyRowHeight)
        return NSPoint(x: winMenuLeft + 60, y: itemTop + winLegacyRowHeight / 2 - 3)
    }
}

// MARK: - Compose a pane

func drawPane(_ pane: Pane, _ frame: Frame) {
    let mac = pane.platform == .mac

    // Caption above the window, set in the platform's own UI face.
    drawText(
        pane.caption, font: mac ? macFont(12, semibold: true) : windowsFont(12, semibold: true),
        color: color(hex: 0x5b6473), leftX: pane.x + 3, centreY: 20)

    // Window body, clipped to its rounded rect so the chrome corners are right.
    let windowRect = topRect(pane.x, windowTopY, windowWidth, windowHeight)
    let radius: CGFloat = mac ? 10 : 8
    withShadow(26, 0.20, 8) {
        macWindowBg.setFill()
        roundedRect(windowRect, radius: radius).fill()
    }
    NSGraphicsContext.saveGraphicsState()
    roundedRect(windowRect, radius: radius).addClip()
    fill(windowRect, mac ? macWindowBg : winWindowBg)
    if mac { drawMacChrome(pane) } else { drawWindowsChrome(pane) }

    var rows = baseRows
    if frame.resultAlpha > 0 { rows.insert(resultRow, at: targetIndex + 1) }
    drawFileRows(pane, rows: rows, selected: frame.selected ? targetIndex : nil, resultAlpha: frame.resultAlpha)
    NSGraphicsContext.restoreGraphicsState()

    color(hex: 0, alpha: mac ? 0.16 : 0.13).setStroke()
    let windowBorder = roundedRect(windowRect, radius: radius)
    windowBorder.lineWidth = 1
    windowBorder.stroke()

    // Menus float above the window, so they are drawn unclipped.
    let stepIndex = mac ? macQuickActionsIndex : winShowMoreIndex
    let actionIndex = mac ? macOurItemIndex : winOurItemIndex
    if frame.menuLevel >= 1 {
        if mac {
            // Finder keeps the parent row highlighted while its submenu is open.
            let hover = (frame.menuLevel == 2 || frame.hoverStep) ? stepIndex : nil
            drawMenu(
                items: macMenuItems, leftX: macMenuLeft, topY: macMenuTop, width: macMenuW,
                rowHeight: macMenuRowHeight, platform: .mac, hover: hover, iconGutter: true)
        } else if frame.menuLevel == 1 {
            drawMenu(
                items: winMenuItems, leftX: winMenuLeft, topY: winMenuTop, width: winMenuW,
                rowHeight: winModernRowHeight, platform: .windows, hover: frame.hoverStep ? stepIndex : nil,
                iconGutter: true, commandBar: true)
        }
    }
    if frame.menuLevel == 2 {
        let hover = frame.hoverAction ? actionIndex : nil
        if mac {
            drawMenu(
                items: macSubmenuItems, leftX: macSubmenuLeft, topY: macSubmenuTop, width: macSubmenuW,
                rowHeight: macMenuRowHeight, platform: .mac, hover: hover, iconGutter: true)
        } else {
            // Windows replaces the modern menu with the legacy one in place.
            drawMenu(
                items: winLegacyItems, leftX: winMenuLeft, topY: winMenuTop, width: winLegacyW,
                rowHeight: winLegacyRowHeight, platform: .windows, hover: hover, iconGutter: true)
        }
    }

    if let cursor = cursorPoint(pane, frame.cursorStage) { drawCursor(at: cursor) }
}

// MARK: - Render one frame

func drawBackground() {
    guard let gradient = NSGradient(colors: [pageTop, pageBottom], atLocations: [0, 1], colorSpace: .sRGB)
    else { fatalError("gradient init failed") }
    gradient.draw(in: NSRect(x: 0, y: 0, width: canvasWidth, height: canvasHeight), angle: -90)
}

func renderFrame(_ frame: Frame, to path: String) {
    guard
        let rep = NSBitmapImageRep(
            bitmapDataPlanes: nil, pixelsWide: Int(canvasWidth * renderScale),
            pixelsHigh: Int(canvasHeight * renderScale), bitsPerSample: 8, samplesPerPixel: 4,
            hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)
    else { fatalError("bitmap allocation failed") }
    rep.size = NSSize(width: canvasWidth, height: canvasHeight)
    guard let context = NSGraphicsContext(bitmapImageRep: rep) else { fatalError("context init failed") }

    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = context
    drawBackground()
    drawPane(macPane, frame)
    drawPane(winPane, frame)
    NSGraphicsContext.restoreGraphicsState()

    guard let png = rep.representation(using: .png, properties: [:]) else { fatalError("png encoding failed") }
    do {
        try png.write(to: URL(fileURLWithPath: path))
    } catch {
        fatalError("failed to write \(path): \(error)")
    }
}

// MARK: - Timeline

let frames: [Frame] = [
    Frame(durationMs: 1100),                                                                    // idle
    Frame(selected: true, durationMs: 450),                                                     // row selected
    Frame(selected: true, menuLevel: 1, durationMs: 780),                                       // context menu
    Frame(selected: true, menuLevel: 1, cursorStage: .primary, durationMs: 120),
    Frame(selected: true, menuLevel: 1, hoverStep: true, cursorStage: .primary, durationMs: 700),
    Frame(selected: true, menuLevel: 2, cursorStage: .primary, durationMs: 320),                // second level opens
    Frame(selected: true, menuLevel: 2, cursorStage: .secondary, durationMs: 260),
    Frame(selected: true, menuLevel: 2, hoverAction: true, cursorStage: .secondary, durationMs: 900),
    Frame(selected: true, menuLevel: 2, cursorStage: .secondary, durationMs: 110),              // click flash off
    Frame(selected: true, menuLevel: 2, hoverAction: true, cursorStage: .secondary, durationMs: 150),
    Frame(selected: true, cursorStage: .secondary, durationMs: 300),                            // menus close
    Frame(selected: true, resultAlpha: 0.45, cursorStage: .result, durationMs: 140),            // copy appears
    Frame(selected: true, resultAlpha: 1, cursorStage: .result, durationMs: 1900),
]

for (index, frame) in frames.enumerated() {
    renderFrame(frame, to: String(format: "\(outputDir)/f%03d.png", index))
}

// ffmpeg concat manifest with per-frame durations (the last file is repeated,
// as the concat demuxer ignores the final entry's duration).
var concat = ""
for (index, frame) in frames.enumerated() {
    concat += "file 'f\(String(format: "%03d", index)).png'\nduration \(Double(frame.durationMs) / 1000)\n"
}
concat += "file 'f\(String(format: "%03d", frames.count - 1)).png'\n"
do {
    try concat.write(toFile: "\(outputDir)/concat.txt", atomically: true, encoding: .utf8)
} catch {
    fatalError("failed to write concat.txt: \(error)")
}

// MARK: - Stitch frames into the GIF (ffmpeg)

func ffmpeg(_ args: [String]) {
    let process = Process()
    process.executableURL = URL(fileURLWithPath: "/usr/bin/env")
    process.arguments = ["ffmpeg"] + args
    process.standardOutput = FileHandle.nullDevice  // ffmpeg is chatty; stay quiet
    process.standardError = FileHandle.nullDevice
    // Detach stdin: with an inherited TTY, ffmpeg enters interactive mode and
    // blocks on a keypress read, hanging the render.
    process.standardInput = FileHandle.nullDevice
    do {
        try process.run()
    } catch {
        fatalError("could not launch ffmpeg: \(error)")
    }
    process.waitUntilExit()
    guard process.terminationStatus == 0 else {
        fatalError("ffmpeg failed (\(process.terminationStatus)); install it: brew install ffmpeg")
    }
}

let filter = "scale=\(Int(canvasWidth)):\(Int(canvasHeight)):flags=lanczos"  // supersample down to the asset size
let concatFile = "\(outputDir)/concat.txt"
let palette = "\(outputDir)/palette.png"

// Pass 1: optimised palette from the frames
ffmpeg([
    "-y", "-f", "concat", "-safe", "0", "-i", concatFile,
    "-vf", "\(filter),palettegen=stats_mode=diff", palette,
])
// Pass 2: apply the palette and honour the per-frame durations
ffmpeg([
    "-y", "-f", "concat", "-safe", "0", "-i", concatFile, "-i", palette,
    "-lavfi", "\(filter)[x];[x][1:v]paletteuse=dither=sierra2_4a:diff_mode=rectangle",
    "-fps_mode", "vfr", "-loop", "0", outputGif,
])

try? FileManager.default.removeItem(atPath: outputDir)
print("rendered \(outputGif)")
