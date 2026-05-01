package storage

import vec "github.com/asg017/sqlite-vec-go-bindings/cgo"

// init registers the sqlite-vec extension globally so every connection
// opened after package load — including in-memory test DBs — has the
// `vec0` virtual table available. Calls sqlite3_auto_extension under the
// hood; safe to call multiple times across init cycles, but we only need
// it once per process.
func init() {
	vec.Auto()
}

// EmbedDims is the locked embedding dimensionality for Phase 2.
// Anchored to OpenAI text-embedding-3-small (1536-d). Phase 4 EU-sovereignty
// pass may switch to Voyage-3 (1024-d) — that swap requires migration 004_*
// recreating kb_vec with the new size and re-embedding existing chunks.
const EmbedDims = 1536

// SerializeEmbedding turns a Go []float32 into the little-endian byte BLOB
// vec0 expects. Returns an error if dims doesn't match EmbedDims so we
// catch shape mismatches at the boundary instead of from a SQLite syntax
// error deep inside the query.
func SerializeEmbedding(v []float32) ([]byte, error) {
	if len(v) != EmbedDims {
		return nil, errEmbedDimMismatch{got: len(v)}
	}
	return vec.SerializeFloat32(v)
}

type errEmbedDimMismatch struct{ got int }

func (e errEmbedDimMismatch) Error() string {
	return "storage: embedding has " + itoa(e.got) + " dims, expected " + itoa(EmbedDims)
}

// itoa is the tiniest possible int-to-string for the dim-mismatch error so
// we don't need to pull in fmt for one error variant.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	buf := [20]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
