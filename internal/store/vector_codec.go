package store

import (
	"encoding/binary"
	"fmt"
	"math"
)

// vecToBlob encodes a float32 vector as a binary.LittleEndian byte slice.
// No header is written; dim is stored in its own column on retrieval.
func vecToBlob(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// blobToVec decodes dim float32 values from b. Returns an error if the byte
// length is not exactly dim*4. NaN values round-trip bit-exactly.
func blobToVec(b []byte, dim int) ([]float32, error) {
	if len(b) != dim*4 {
		return nil, fmt.Errorf("vector blob length %d does not match dim %d (expected %d)", len(b), dim, dim*4)
	}
	out := make([]float32, dim)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out, nil
}
