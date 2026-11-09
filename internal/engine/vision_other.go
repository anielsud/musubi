//go:build !darwin

package engine

type visionFace struct{ x, y, w, h, quality float64 }

const haveVision = false

func detectFaces([]byte) []visionFace { return nil }
func featurePrint([]byte) []float32   { return nil }
