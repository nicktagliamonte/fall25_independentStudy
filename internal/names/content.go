package names

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

const (
	InlineObjectLimit = 4 << 20
	ObjectChunkSize   = 1 << 20
)

type ChunkRef struct {
	Index          uint64 `json:"index" cbor:"index"`
	PlaintextHash  []byte `json:"plaintext_hash" cbor:"plaintext_hash"`
	CiphertextKey  []byte `json:"ciphertext_key" cbor:"ciphertext_key"`
	Nonce          []byte `json:"nonce,omitempty" cbor:"nonce,omitempty"`
	PlaintextBytes uint64 `json:"plaintext_bytes" cbor:"plaintext_bytes"`
	StoredBytes    uint64 `json:"stored_bytes" cbor:"stored_bytes"`
}

type KeyEnvelope struct {
	ReaderPublic    []byte `json:"reader_public" cbor:"reader_public"`
	EphemeralPublic []byte `json:"ephemeral_public" cbor:"ephemeral_public"`
	Nonce           []byte `json:"nonce" cbor:"nonce"`
	WrappedKey      []byte `json:"wrapped_key" cbor:"wrapped_key"`
}

type ObjectManifest struct {
	Version         uint64        `json:"version" cbor:"version"`
	ObjectBytes     uint64        `json:"object_bytes" cbor:"object_bytes"`
	PlaintextDigest []byte        `json:"plaintext_digest" cbor:"plaintext_digest"`
	Encryption      string        `json:"encryption" cbor:"encryption"`
	KeyEpoch        uint64        `json:"key_epoch" cbor:"key_epoch"`
	ChunkSize       uint64        `json:"chunk_size" cbor:"chunk_size"`
	Chunks          []ChunkRef    `json:"chunks" cbor:"chunks"`
	Envelopes       []KeyEnvelope `json:"envelopes,omitempty" cbor:"envelopes,omitempty"`
	Signer          []byte        `json:"signer" cbor:"signer"`
	Signature       []byte        `json:"signature,omitempty" cbor:"signature,omitempty"`
}

func (m *ObjectManifest) unsigned() ObjectManifest { out := *m; out.Signature = nil; return out }

func (m *ObjectManifest) Sign(privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("invalid manifest signing key")
	}
	m.Signer = append(m.Signer[:0], privateKey.Public().(ed25519.PublicKey)...)
	payload, err := signingBytes(ManifestDomain, m.unsigned())
	if err != nil {
		return err
	}
	m.Signature = ed25519.Sign(privateKey, payload)
	return nil
}

func (m *ObjectManifest) Validate() error {
	if m == nil || m.Version != FormatVersion || len(m.PlaintextDigest) != 32 || len(m.Signer) != 32 || len(m.Signature) != 64 {
		return errors.New("invalid manifest schema")
	}
	if m.Encryption != "private" && m.Encryption != "public" {
		return errors.New("invalid manifest encryption mode")
	}
	if len(m.Chunks) == 0 {
		return errors.New("manifest has no chunks")
	}
	if m.ObjectBytes <= InlineObjectLimit {
		if len(m.Chunks) != 1 || m.ChunkSize != InlineObjectLimit {
			return errors.New("small object must use one block")
		}
	} else if m.ChunkSize != ObjectChunkSize {
		return errors.New("large object must use 1 MiB chunks")
	}
	var total uint64
	for i, chunk := range m.Chunks {
		if chunk.Index != uint64(i) || len(chunk.PlaintextHash) != 32 || len(chunk.CiphertextKey) != 32 || (chunk.PlaintextBytes == 0 && m.ObjectBytes != 0) {
			return errors.New("invalid manifest chunk")
		}
		if m.Encryption == "private" && len(chunk.Nonce) != chacha20poly1305.NonceSizeX {
			return errors.New("private chunk has invalid nonce")
		}
		if m.Encryption == "public" && len(chunk.Nonce) != 0 {
			return errors.New("public chunk may not have a nonce")
		}
		total += chunk.PlaintextBytes
	}
	if total != m.ObjectBytes {
		return errors.New("manifest object size does not match chunks")
	}
	if m.Encryption == "private" && len(m.Envelopes) == 0 {
		return errors.New("private manifest requires a reader envelope")
	}
	for _, envelope := range m.Envelopes {
		if len(envelope.ReaderPublic) != 32 || len(envelope.EphemeralPublic) != 32 || len(envelope.Nonce) != chacha20poly1305.NonceSizeX || len(envelope.WrappedKey) != 32+chacha20poly1305.Overhead {
			return errors.New("invalid reader envelope")
		}
	}
	payload, err := signingBytes(ManifestDomain, m.unsigned())
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(m.Signer), payload, m.Signature) {
		return errors.New("invalid manifest signature")
	}
	return nil
}

func (m *ObjectManifest) Marshal() ([]byte, error) { return MarshalCanonical(m) }

func DecodeObjectManifest(raw []byte) (*ObjectManifest, error) {
	var manifest ObjectManifest
	if err := UnmarshalCanonical(raw, &manifest); err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

type BlockSink func(context.Context, []byte) (ContentKey, error)
type BlockSource func(context.Context, ContentKey) ([]byte, error)

type BuildObjectOptions struct {
	Encryption       string
	KeyEpoch         uint64
	DataKey          []byte
	ReaderPublicKeys [][]byte
	Previous         *ObjectManifest
	Signer           ed25519.PrivateKey
}

type BuiltObject struct {
	Manifest     *ObjectManifest
	ManifestKey  ContentKey
	DataKey      []byte
	NewBlocks    int
	ReusedBlocks int
}

func BuildObject(ctx context.Context, reader io.Reader, sink BlockSink, options BuildObjectOptions) (*BuiltObject, error) {
	if sink == nil {
		return nil, errors.New("block sink is required")
	}
	if options.Encryption == "" {
		options.Encryption = "private"
	}
	if options.Encryption != "private" && options.Encryption != "public" {
		return nil, errors.New("encryption must be private or public")
	}
	if options.Encryption == "private" && len(options.ReaderPublicKeys) == 0 {
		return nil, errors.New("private object requires at least one reader")
	}
	if options.Previous != nil && options.Previous.Encryption == "private" && options.Encryption == "private" && options.Previous.KeyEpoch == options.KeyEpoch {
		if len(options.DataKey) == 0 {
			return nil, errors.New("same-epoch update requires the existing data key")
		}
		if !sameReaderSet(options.Previous.Envelopes, options.ReaderPublicKeys) {
			return nil, errors.New("reader membership change requires a new key epoch")
		}
	}
	dataKey := append([]byte(nil), options.DataKey...)
	if options.Encryption == "private" && len(dataKey) == 0 {
		dataKey = make([]byte, chacha20poly1305.KeySize)
		if _, err := rand.Read(dataKey); err != nil {
			return nil, err
		}
	}
	if options.Encryption == "private" && len(dataKey) != chacha20poly1305.KeySize {
		return nil, errors.New("data key must be 32 bytes")
	}
	if len(options.Signer) != ed25519.PrivateKeySize {
		return nil, errors.New("manifest signer is required")
	}

	prefix := make([]byte, InlineObjectLimit+1)
	n, readErr := io.ReadFull(reader, prefix)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return nil, readErr
	}
	prefix = prefix[:n]
	chunkSize := InlineObjectLimit
	input := io.Reader(bytes.NewReader(prefix))
	if n > InlineObjectLimit {
		chunkSize = ObjectChunkSize
		input = io.MultiReader(bytes.NewReader(prefix), reader)
	}
	if n == 0 {
		input = bytes.NewReader([]byte{})
	}

	manifest := &ObjectManifest{Version: FormatVersion, Encryption: options.Encryption, KeyEpoch: options.KeyEpoch, ChunkSize: uint64(chunkSize), Chunks: []ChunkRef{}}
	wholeHash := sha256.New()
	reuse := reusableChunks(options.Previous, options.KeyEpoch, options.Encryption)
	result := &BuiltObject{Manifest: manifest, DataKey: dataKey}
	buffer := make([]byte, chunkSize)
	for index := uint64(0); ; index++ {
		count, err := io.ReadFull(input, buffer)
		if err == io.EOF && index > 0 {
			break
		}
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return nil, err
		}
		plain := append([]byte(nil), buffer[:count]...)
		if count == 0 && index == 0 {
			plain = []byte{}
		}
		_, _ = wholeHash.Write(plain)
		plainHash := sha256.Sum256(plain)
		if previous, ok := reuse[string(plainHash[:])]; ok {
			previous.Index = index
			manifest.Chunks = append(manifest.Chunks, previous)
			manifest.ObjectBytes += uint64(count)
			result.ReusedBlocks++
		} else {
			stored, nonce, err := sealChunk(plain, dataKey, options.Encryption, options.KeyEpoch, plainHash)
			if err != nil {
				return nil, err
			}
			key, err := sink(ctx, stored)
			if err != nil {
				return nil, err
			}
			if key != sha256.Sum256(stored) {
				return nil, errors.New("block sink returned incorrect content key")
			}
			manifest.Chunks = append(manifest.Chunks, ChunkRef{Index: index, PlaintextHash: plainHash[:], CiphertextKey: key[:], Nonce: nonce, PlaintextBytes: uint64(count), StoredBytes: uint64(len(stored))})
			manifest.ObjectBytes += uint64(count)
			result.NewBlocks++
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
	}
	manifest.PlaintextDigest = wholeHash.Sum(nil)
	if options.Encryption == "private" {
		for _, readerPublic := range options.ReaderPublicKeys {
			envelope, err := WrapDataKey(dataKey, readerPublic, options.KeyEpoch)
			if err != nil {
				return nil, err
			}
			manifest.Envelopes = append(manifest.Envelopes, envelope)
		}
	}
	if err := manifest.Sign(options.Signer); err != nil {
		return nil, err
	}
	manifestRaw, err := manifest.Marshal()
	if err != nil {
		return nil, err
	}
	manifestKey, err := sink(ctx, manifestRaw)
	if err != nil {
		return nil, err
	}
	if manifestKey != sha256.Sum256(manifestRaw) {
		return nil, errors.New("block sink returned incorrect manifest key")
	}
	result.ManifestKey = manifestKey
	result.NewBlocks++
	return result, nil
}

func reusableChunks(previous *ObjectManifest, epoch uint64, encryption string) map[string]ChunkRef {
	out := make(map[string]ChunkRef)
	if previous == nil || previous.KeyEpoch != epoch || previous.Encryption != encryption {
		return out
	}
	for _, chunk := range previous.Chunks {
		out[string(chunk.PlaintextHash)] = chunk
	}
	return out
}

func sameReaderSet(envelopes []KeyEnvelope, readers [][]byte) bool {
	if len(envelopes) != len(readers) {
		return false
	}
	for _, envelope := range envelopes {
		found := false
		for _, reader := range readers {
			if bytes.Equal(envelope.ReaderPublic, reader) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func chunkAAD(epoch uint64, plainHash [32]byte) []byte {
	aad := make([]byte, 8+32)
	binary.BigEndian.PutUint64(aad[:8], epoch)
	copy(aad[8:], plainHash[:])
	return aad
}

func sealChunk(plain, key []byte, encryption string, epoch uint64, plainHash [32]byte) ([]byte, []byte, error) {
	if encryption == "public" {
		return append([]byte(nil), plain...), nil, nil
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return aead.Seal(nil, nonce, plain, chunkAAD(epoch, plainHash)), nonce, nil
}

func WrapDataKey(dataKey, readerPublic []byte, epoch uint64) (KeyEnvelope, error) {
	var out KeyEnvelope
	if len(dataKey) != 32 || len(readerPublic) != 32 {
		return out, errors.New("data key and X25519 reader key must be 32 bytes")
	}
	ephemeralPrivate := make([]byte, 32)
	if _, err := rand.Read(ephemeralPrivate); err != nil {
		return out, err
	}
	ephemeralPublic, err := curve25519.X25519(ephemeralPrivate, curve25519.Basepoint)
	if err != nil {
		return out, err
	}
	shared, err := curve25519.X25519(ephemeralPrivate, readerPublic)
	if err != nil {
		return out, err
	}
	wrapKey, err := deriveWrapKey(shared, readerPublic, ephemeralPublic, epoch)
	if err != nil {
		return out, err
	}
	aead, err := chacha20poly1305.NewX(wrapKey)
	if err != nil {
		return out, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return out, err
	}
	aad := envelopeAAD(readerPublic, ephemeralPublic, epoch)
	out = KeyEnvelope{ReaderPublic: append([]byte(nil), readerPublic...), EphemeralPublic: ephemeralPublic, Nonce: nonce, WrappedKey: aead.Seal(nil, nonce, dataKey, aad)}
	return out, nil
}

func OpenDataKey(envelope KeyEnvelope, readerPrivate []byte, epoch uint64) ([]byte, error) {
	if len(readerPrivate) != 32 {
		return nil, errors.New("X25519 reader private key must be 32 bytes")
	}
	readerPublic, err := curve25519.X25519(readerPrivate, curve25519.Basepoint)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(readerPublic, envelope.ReaderPublic) {
		return nil, errors.New("envelope is for a different reader")
	}
	shared, err := curve25519.X25519(readerPrivate, envelope.EphemeralPublic)
	if err != nil {
		return nil, err
	}
	wrapKey, err := deriveWrapKey(shared, readerPublic, envelope.EphemeralPublic, epoch)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(wrapKey)
	if err != nil {
		return nil, err
	}
	key, err := aead.Open(nil, envelope.Nonce, envelope.WrappedKey, envelopeAAD(readerPublic, envelope.EphemeralPublic, epoch))
	if err != nil {
		return nil, errors.New("key envelope authentication failed")
	}
	return key, nil
}

func deriveWrapKey(shared, readerPublic, ephemeralPublic []byte, epoch uint64) ([]byte, error) {
	info := envelopeAAD(readerPublic, ephemeralPublic, epoch)
	reader := hkdf.New(func() hash.Hash { return sha256.New() }, shared, []byte("tarsus-envelope-salt-v1"), info)
	key := make([]byte, 32)
	_, err := io.ReadFull(reader, key)
	return key, err
}

func envelopeAAD(readerPublic, ephemeralPublic []byte, epoch uint64) []byte {
	out := make([]byte, 0, len(readerPublic)+len(ephemeralPublic)+8+20)
	out = append(out, []byte("tarsus-envelope-v1\x00")...)
	out = append(out, readerPublic...)
	out = append(out, ephemeralPublic...)
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], epoch)
	return append(out, encoded[:]...)
}

func ReconstructObject(ctx context.Context, manifest *ObjectManifest, source BlockSource, dataKey []byte, writer io.Writer) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if source == nil || writer == nil {
		return errors.New("block source and writer are required")
	}
	if manifest.Encryption == "private" && len(dataKey) != 32 {
		return errors.New("private object requires a 32-byte data key")
	}
	wholeHash := sha256.New()
	var total uint64
	for _, ref := range manifest.Chunks {
		var key ContentKey
		copy(key[:], ref.CiphertextKey)
		stored, err := source(ctx, key)
		if err != nil {
			return err
		}
		if sha256.Sum256(stored) != key {
			return fmt.Errorf("chunk %d content-key verification failed", ref.Index)
		}
		plain := stored
		if manifest.Encryption == "private" {
			aead, err := chacha20poly1305.NewX(dataKey)
			if err != nil {
				return err
			}
			var plainHash [32]byte
			copy(plainHash[:], ref.PlaintextHash)
			plain, err = aead.Open(nil, ref.Nonce, stored, chunkAAD(manifest.KeyEpoch, plainHash))
			if err != nil {
				return fmt.Errorf("chunk %d authentication failed", ref.Index)
			}
		}
		if sha256.Sum256(plain) != bytesToArray(ref.PlaintextHash) || uint64(len(plain)) != ref.PlaintextBytes {
			return fmt.Errorf("chunk %d plaintext verification failed", ref.Index)
		}
		if _, err := writer.Write(plain); err != nil {
			return err
		}
		_, _ = wholeHash.Write(plain)
		total += uint64(len(plain))
	}
	if total != manifest.ObjectBytes || !bytes.Equal(wholeHash.Sum(nil), manifest.PlaintextDigest) {
		return errors.New("reconstructed object digest or size mismatch")
	}
	return nil
}

func bytesToArray(value []byte) [32]byte { var out [32]byte; copy(out[:], value); return out }
