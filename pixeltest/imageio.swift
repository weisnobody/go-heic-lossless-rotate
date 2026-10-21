// imageio decodes a HEIF file with Apple ImageIO, applying its orientation
// (irot/imir, as ImageIO reports it), and writes a PNG:
//     imageio <in.heic> <out.png>
// Used by pixel_test.go; compiled with swiftc on the fly.
import Foundation
import ImageIO
import UniformTypeIdentifiers

let args = CommandLine.arguments
guard args.count == 3,
      let src = CGImageSourceCreateWithURL(URL(fileURLWithPath: args[1]) as CFURL, nil) else {
    FileHandle.standardError.write("usage: imageio <in.heic> <out.png>\n".data(using: .utf8)!)
    exit(2)
}
let opts: [CFString: Any] = [
    kCGImageSourceCreateThumbnailFromImageAlways: true,
    kCGImageSourceCreateThumbnailWithTransform: true,
    kCGImageSourceThumbnailMaxPixelSize: 1 << 16,
]
guard let img = CGImageSourceCreateThumbnailAtIndex(src, 0, opts as CFDictionary),
      let dst = CGImageDestinationCreateWithURL(URL(fileURLWithPath: args[2]) as CFURL,
                                                UTType.png.identifier as CFString, 1, nil) else {
    FileHandle.standardError.write("imageio: can't decode \(args[1])\n".data(using: .utf8)!)
    exit(1)
}
CGImageDestinationAddImage(dst, img, nil)
exit(CGImageDestinationFinalize(dst) ? 0 : 1)
