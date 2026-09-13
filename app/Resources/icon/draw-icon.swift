// Draws the Onyx app icon: a black diamond with white facet edges on a
// soft gray rounded square. Emits an .iconset directory; the Makefile turns
// it into AppIcon.icns with iconutil.
//   swift draw-icon.swift <out.iconset>
import AppKit

let out = CommandLine.arguments[1]
try? FileManager.default.createDirectory(atPath: out, withIntermediateDirectories: true)

func draw(_ px: Int, name: String) {
    let s = CGFloat(px)
    let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: px, pixelsHigh: px, bitsPerSample: 8,
                               samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                               colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
    NSGraphicsContext.saveGraphicsState()
    let gc = NSGraphicsContext(bitmapImageRep: rep)!
    NSGraphicsContext.current = gc
    let c = gc.cgContext
    // Coordinates below are in a 0…1 unit square, y down (flipped).
    c.translateBy(x: 0, y: s); c.scaleBy(x: s, y: -s)

    // Soft gray squircle on the macOS icon grid (824/1024 of the canvas).
    let inset: CGFloat = 100.0 / 1024
    let tile = CGRect(x: inset, y: inset, width: 1 - 2 * inset, height: 1 - 2 * inset)
    let bg = CGPath(roundedRect: tile, cornerWidth: tile.width * 0.2237, cornerHeight: tile.width * 0.2237, transform: nil)
    c.saveGState()
    c.addPath(bg); c.clip()
    let grad = CGGradient(colorsSpace: CGColorSpaceCreateDeviceRGB(),
                          colors: [CGColor(red: 0.90, green: 0.90, blue: 0.91, alpha: 1),
                                   CGColor(red: 0.78, green: 0.78, blue: 0.80, alpha: 1)] as CFArray,
                          locations: [0, 1])!
    c.drawLinearGradient(grad, start: CGPoint(x: 0.5, y: tile.minY), end: CGPoint(x: 0.5, y: tile.maxY), options: [])
    c.restoreGState()

    // Diamond: table, girdle, culet.
    let tY: CGFloat = 0.30, gY: CGFloat = 0.47, cY = CGPoint(x: 0.5, y: 0.80)
    let T = [0.36, 0.50, 0.64].map { CGPoint(x: $0, y: tY) }
    let G = [0.20, 0.35, 0.50, 0.65, 0.80].map { CGPoint(x: $0, y: gY) }
    let outline = CGMutablePath()
    outline.move(to: T[0]); outline.addLine(to: T[2]); outline.addLine(to: G[4])
    outline.addLine(to: cY); outline.addLine(to: G[0]); outline.closeSubpath()

    c.setFillColor(CGColor(red: 0.06, green: 0.06, blue: 0.07, alpha: 1))
    c.addPath(outline); c.fillPath()

    // Facet edges: crown fans from the table corners, pavilion from the culet.
    let edges: [(CGPoint, CGPoint)] = [
        (G[0], G[4]),                       // girdle
        (T[0], G[1]), (T[2], G[3]),         // crown
        (T[1], G[1]), (T[1], G[3]),
        (G[1], cY), (G[3], cY),             // pavilion
    ]
    c.setStrokeColor(.white)
    c.setLineWidth(0.022); c.setLineCap(.round); c.setLineJoin(.round)
    for (a, b) in edges { c.move(to: a); c.addLine(to: b) }
    c.strokePath()
    c.addPath(outline); c.strokePath()

    NSGraphicsContext.restoreGraphicsState()
    try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: "\(out)/\(name).png"))
}

for (pt, scales) in [(16, [1, 2]), (32, [1, 2]), (128, [1, 2]), (256, [1, 2]), (512, [1, 2])] {
    for sc in scales { draw(pt * sc, name: sc == 1 ? "icon_\(pt)x\(pt)" : "icon_\(pt)x\(pt)@2x") }
}
