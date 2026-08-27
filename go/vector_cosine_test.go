package main

import (
	"math"
	"testing"
)

// Un-normalized provider vectors must still rank by angle, not magnitude.
// The old dot-product implementation clamped every large vector pair to 1.0,
// collapsing the ranking to insertion order.
func TestVectorCosineNormalizesMagnitude(t *testing.T) {
	query := []float64{3, 0}
	aligned := []float64{10, 0}
	offAxis := []float64{7, 7}
	orthogonal := []float64{0, 5}

	if got := vectorCosine(query, aligned); math.Abs(got-1) > 1e-9 {
		t.Fatalf("aligned cosine = %f, want 1", got)
	}
	if got := vectorCosine(query, offAxis); math.Abs(got-math.Sqrt2/2) > 1e-9 {
		t.Fatalf("45-degree cosine = %f", got)
	}
	if got := vectorCosine(query, orthogonal); got != 0 {
		t.Fatalf("orthogonal cosine = %f, want 0", got)
	}
	if vectorCosine(query, aligned) <= vectorCosine(query, offAxis) {
		t.Fatal("magnitude leaked into ranking")
	}
	if got := vectorCosine(nil, aligned); got != 0 {
		t.Fatalf("empty vector cosine = %f", got)
	}
	if got := vectorCosine([]float64{0, 0}, aligned); got != 0 {
		t.Fatalf("zero vector cosine = %f", got)
	}
}
