package store

import (
	"math"
	"testing"
)

func TestVecToBlobRoundTrip(t *testing.T) {
	vec := []float32{0, 1, -1, 3.14159, -2.71828, math.MaxFloat32, math.SmallestNonzeroFloat32}
	b := vecToBlob(vec)
	if got, want := len(b), len(vec)*4; got != want {
		t.Fatalf("blob length = %d, want %d", got, want)
	}
	got, err := blobToVec(b, len(vec))
	if err != nil {
		t.Fatalf("blobToVec: %v", err)
	}
	if len(got) != len(vec) {
		t.Fatalf("decoded len = %d, want %d", len(got), len(vec))
	}
	for i := range vec {
		if math.Float32bits(got[i]) != math.Float32bits(vec[i]) {
			t.Errorf("[%d] = %v, want %v", i, got[i], vec[i])
		}
	}
}

func TestVecToBlobRoundTripNaN(t *testing.T) {
	nan := float32(math.NaN())
	got, err := blobToVec(vecToBlob([]float32{nan}), 1)
	if err != nil {
		t.Fatalf("blobToVec: %v", err)
	}
	if !math.IsNaN(float64(got[0])) {
		t.Fatalf("expected NaN, got %v", got[0])
	}
}

func TestBlobToVecRejectsBadLength(t *testing.T) {
	_, err := blobToVec([]byte{0, 0, 0}, 1) // 3 bytes, dim=1 → expects 4
	if err == nil {
		t.Fatalf("expected error for short blob")
	}
}

func TestBlobToVecAcceptsZeroDim(t *testing.T) {
	got, err := blobToVec(nil, 0)
	if err != nil {
		t.Fatalf("blobToVec(nil, 0): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}
