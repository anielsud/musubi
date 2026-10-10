// Draws Musubi's app icon: three threads tied into a knot on an ink tile.
// Usage: swift icon.swift out.png   (1024×1024)
import AppKit

let size: CGFloat = 1024
let img = NSImage(size: NSSize(width: size, height: size))
img.lockFocus()
let ctx = NSGraphicsContext.current!.cgContext

// Tile with the macOS icon margin and corner radius.
let inset: CGFloat = 100
let tile = CGRect(x: inset, y: inset, width: size - 2 * inset, height: size - 2 * inset)
let path = CGPath(roundedRect: tile, cornerWidth: 185, cornerHeight: 185, transform: nil)
ctx.saveGState()
ctx.setShadow(offset: CGSize(width: 0, height: -14), blur: 30, color: NSColor.black.withAlphaComponent(0.45).cgColor)
ctx.addPath(path)
ctx.setFillColor(NSColor(red: 0.075, green: 0.09, blue: 0.12, alpha: 1).cgColor)
ctx.fillPath()
ctx.restoreGState()

ctx.saveGState()
ctx.addPath(path)
ctx.clip()
let grad = CGGradient(colorsSpace: CGColorSpaceCreateDeviceRGB(), colors: [
    NSColor(red: 0.12, green: 0.15, blue: 0.2, alpha: 1).cgColor,
    NSColor(red: 0.055, green: 0.067, blue: 0.086, alpha: 1).cgColor,
] as CFArray, locations: [0, 1])!
ctx.drawLinearGradient(grad, start: CGPoint(x: 0, y: size - inset), end: CGPoint(x: 0, y: inset), options: [])

// Three loops at 120° that overlap in the centre: a musubi knot.
let colors = [
    NSColor(red: 0.37, green: 0.92, blue: 0.83, alpha: 1),
    NSColor(red: 0.65, green: 0.55, blue: 0.98, alpha: 1),
    NSColor(red: 0.96, green: 0.79, blue: 0.48, alpha: 1),
]
let c = CGPoint(x: size / 2, y: size / 2 - 6)
ctx.setLineCap(.round)
for pass in 0..<2 {
    for (i, col) in colors.enumerated() {
        let a = CGFloat(i) * 2 * .pi / 3 + .pi / 2
        let center = CGPoint(x: c.x + cos(a) * 92, y: c.y + sin(a) * 92)
        let loop = CGPath(ellipseIn: CGRect(x: center.x - 165, y: center.y - 165, width: 330, height: 330), transform: nil)
        ctx.addPath(loop)
        if pass == 0 {
            // A dark halo under each thread makes the crossings read as woven.
            ctx.setStrokeColor(NSColor(red: 0.055, green: 0.067, blue: 0.086, alpha: 1).cgColor)
            ctx.setLineWidth(74)
        } else {
            ctx.setStrokeColor(col.cgColor)
            ctx.setLineWidth(46)
        }
        ctx.strokePath()
    }
}
ctx.restoreGState()
img.unlockFocus()

let rep = NSBitmapImageRep(data: img.tiffRepresentation!)!
let png = rep.representation(using: .png, properties: [:])!
try! png.write(to: URL(fileURLWithPath: CommandLine.arguments[1]))
