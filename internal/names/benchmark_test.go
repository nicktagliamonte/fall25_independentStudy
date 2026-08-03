package names

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
)

func BenchmarkObjectBuild(b *testing.B) {
	_, signer := testKeys(b)
	readerPrivate := make([]byte, 32)
	_, _ = rand.Read(readerPrivate)
	readerPublic, _ := curve25519.X25519(readerPrivate, curve25519.Basepoint)
	for _, size := range []int{64 << 10, 4 << 20, 64 << 20} {
		b.Run(fmt.Sprintf("bytes_%d", size), func(b *testing.B) {
			data := bytes.Repeat([]byte{0x5a}, size)
			sink := func(_ context.Context, raw []byte) (ContentKey, error) { return sha256.Sum256(raw), nil }
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := BuildObject(context.Background(), bytes.NewReader(data), sink, BuildObjectOptions{Encryption: "private", KeyEpoch: uint64(i + 1), ReaderPublicKeys: [][]byte{readerPublic}, Signer: signer}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkNameRecordSignAndValidate(b *testing.B) {
	owner, private := testKeys(b)
	namespace, _ := NewNamespaceID()
	record := testRecord(b, namespace, "/bench/object", owner, private, 0, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		record.Timestamp++
		record.Nonce[0]++
		if err := record.Sign(private); err != nil {
			b.Fatal(err)
		}
		if err := record.Validate(time.Unix(0, record.Timestamp), nil); err != nil {
			b.Fatal(err)
		}
	}
}
