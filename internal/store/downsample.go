// Package store — LTTB (Largest-Triangle-Three-Buckets) downsampler.
//
// Implements Steinarsson's algorithm: split N points into `target` buckets,
// pick one representative point per bucket using the largest triangle area
// formed with the previous selected point and the average of the next bucket.
//
// Reference: Steinarsson, S. (2013). "Downsampling Time Series for Visual
// Representation." MSc thesis, University of Iceland.
// https://skemman.is/handle/1946/15343
package store

import (
	"math"

	"github.com/litemlflow/litemlflow/internal/model"
)

// DownsampleLTTB reduces points to at most target representatives using the
// Largest-Triangle-Three-Buckets algorithm.
//
// Edge cases:
//   - target < 3: return all points unchanged (not enough to form any triangle).
//   - len(points) <= target: return all points unchanged.
//   - The first and last points are always included.
func DownsampleLTTB(points []model.Metric, target int) []model.Metric {
	n := len(points)
	if target < 3 || n <= target {
		return points
	}

	out := make([]model.Metric, 0, target)

	// Always include the first point.
	out = append(out, points[0])

	// The middle points are divided into (target - 2) buckets.
	// Bucket boundaries are computed over the range [1, n-1).
	bucketCount := target - 2
	// Effective range of points to bucket: indices 1..n-2 (n-2 points total).
	rangeLen := float64(n - 2)

	prevPoint := points[0]

	for i := 0; i < bucketCount; i++ {
		// Current bucket bounds (indices into points, exclusive of first/last).
		bucketStart := int(math.Floor(float64(i)*rangeLen/float64(bucketCount))) + 1
		bucketEnd := int(math.Floor(float64(i+1)*rangeLen/float64(bucketCount))) + 1
		if bucketEnd >= n-1 {
			bucketEnd = n - 2
		}

		// Next bucket average (the "C" point in the triangle).
		nextBucketStart := bucketEnd + 1
		nextBucketEnd := int(math.Floor(float64(i+2)*rangeLen/float64(bucketCount))) + 1
		if nextBucketEnd >= n-1 {
			nextBucketEnd = n - 2
		}

		// Compute average of next bucket as a virtual point.
		var avgX, avgY float64
		count := 0
		for j := nextBucketStart; j <= nextBucketEnd; j++ {
			avgX += float64(points[j].Timestamp)
			avgY += points[j].Value
			count++
		}
		// If next bucket is empty (pathological slice), fall back to the last point.
		if count == 0 {
			avgX = float64(points[n-1].Timestamp)
			avgY = points[n-1].Value
		} else {
			avgX /= float64(count)
			avgY /= float64(count)
		}

		// Pick the point in the current bucket that forms the largest triangle
		// with prevPoint (A) and avgNext (C).
		aX := float64(prevPoint.Timestamp)
		aY := prevPoint.Value

		maxArea := -1.0
		bestIdx := bucketStart
		for j := bucketStart; j <= bucketEnd; j++ {
			bX := float64(points[j].Timestamp)
			bY := points[j].Value
			// Triangle area = 0.5 * |det([B-A, C-A])|.
			area := math.Abs((bX-aX)*(avgY-aY) - (avgX-aX)*(bY-aY))
			if area > maxArea {
				maxArea = area
				bestIdx = j
			}
		}

		out = append(out, points[bestIdx])
		prevPoint = points[bestIdx]
	}

	// Always include the last point.
	out = append(out, points[n-1])
	return out
}
