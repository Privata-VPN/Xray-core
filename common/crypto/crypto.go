// Package crypto provides common crypto libraries for Xray.
package crypto // import "github.com/xtls/xray-core/common/crypto"

import (
	"crypto/rand"
	mrand "math/rand/v2"
	"sync"

	"github.com/xtls/xray-core/common"
)

// randPool holds ChaCha8 generators seeded from crypto/rand. ChaCha8 is
// cryptographically strong, and drawing from it saves a big.Int and a
// getrandom call per number.
var randPool = sync.Pool{
	New: func() any {
		var seed [32]byte
		common.Must2(rand.Read(seed[:]))
		return mrand.New(mrand.NewChaCha8(seed))
	},
}

// [,)
func RandBetween(from int64, to int64) int64 {
	if from > to {
		from, to = to, from
	}
	if d := to - from; d == 0 || d == 1 {
		return from
	}
	r := randPool.Get().(*mrand.Rand)
	n := r.Int64N(to - from)
	randPool.Put(r)
	return from + n
}

// [,]
func RandBytesBetween(b []byte, from, to byte) {
	common.Must2(rand.Read(b))

	if from > to {
		from, to = to, from
	}

	if to-from == 255 {
		return
	}

	for i := range b {
		b[i] = from + b[i]%(to-from+1)
	}
}
