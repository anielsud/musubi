package engine

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework Vision -framework CoreGraphics -framework ImageIO
#import <Foundation/Foundation.h>
#import <Vision/Vision.h>
#import <ImageIO/ImageIO.h>
#include <string.h>

typedef struct { float x, y, w, h, quality; } musubi_face;

static CGImageRef musubi_decode(const void *buf, int n) {
	CFDataRef d = CFDataCreate(NULL, buf, n);
	CGImageSourceRef src = CGImageSourceCreateWithData(d, NULL);
	CFRelease(d);
	if (!src) return NULL;
	CGImageRef img = CGImageSourceCreateImageAtIndex(src, 0, NULL);
	CFRelease(src);
	return img;
}

// Faces with their capture quality, top-left origin, as shares of the image.
static int musubi_faces(const void *buf, int n, musubi_face *out, int max) {
	@autoreleasepool {
		CGImageRef img = musubi_decode(buf, n);
		if (!img) return -1;
		VNDetectFaceCaptureQualityRequest *q = [VNDetectFaceCaptureQualityRequest new];
		VNImageRequestHandler *h = [[VNImageRequestHandler alloc] initWithCGImage:img options:@{}];
		NSError *err = nil;
		BOOL ok = [h performRequests:@[q] error:&err];
		CGImageRelease(img);
		if (!ok) return -1;
		int i = 0;
		for (VNFaceObservation *f in q.results) {
			if (i >= max) break;
			CGRect b = f.boundingBox;
			out[i].x = b.origin.x;
			out[i].y = 1 - b.origin.y - b.size.height;
			out[i].w = b.size.width;
			out[i].h = b.size.height;
			out[i].quality = f.faceCaptureQuality ? f.faceCaptureQuality.floatValue : 0;
			i++;
		}
		return i;
	}
}

// An image feature print (Vision's embedding), for comparing faces.
static int musubi_print(const void *buf, int n, float *out, int max) {
	@autoreleasepool {
		CGImageRef img = musubi_decode(buf, n);
		if (!img) return -1;
		VNGenerateImageFeaturePrintRequest *r = [VNGenerateImageFeaturePrintRequest new];
		VNImageRequestHandler *h = [[VNImageRequestHandler alloc] initWithCGImage:img options:@{}];
		NSError *err = nil;
		BOOL ok = [h performRequests:@[r] error:&err];
		CGImageRelease(img);
		if (!ok || r.results.count == 0) return -1;
		VNFeaturePrintObservation *o = r.results.firstObject;
		if (o.elementType != VNElementTypeFloat) return -2;
		int c = (int)o.elementCount;
		if (c > max) c = max;
		memcpy(out, o.data.bytes, c * sizeof(float));
		return c;
	}
}
*/
import "C"

import "unsafe"

// visionFace is a face Vision found: its box as shares of the image
// (top-left origin) and its capture quality (0–1, higher is a clearer,
// better-lit, more frontal face).
type visionFace struct{ x, y, w, h, quality float64 }

const haveVision = true

// detectFaces finds faces in a JPEG with macOS Vision.
func detectFaces(jpg []byte) []visionFace {
	if len(jpg) == 0 {
		return nil
	}
	var out [16]C.musubi_face
	n := C.musubi_faces(unsafe.Pointer(&jpg[0]), C.int(len(jpg)), &out[0], 16)
	var fs []visionFace
	for i := 0; i < int(n); i++ {
		f := out[i]
		fs = append(fs, visionFace{float64(f.x), float64(f.y), float64(f.w), float64(f.h), float64(f.quality)})
	}
	return fs
}

// featurePrint is Vision's image embedding of a JPEG, or nil.
func featurePrint(jpg []byte) []float32 {
	if len(jpg) == 0 {
		return nil
	}
	buf := make([]float32, 4096)
	n := C.musubi_print(unsafe.Pointer(&jpg[0]), C.int(len(jpg)), (*C.float)(unsafe.Pointer(&buf[0])), C.int(len(buf)))
	if n <= 0 {
		return nil
	}
	return buf[:n]
}
