package room

import (
	"crypto/rand"
	"fmt"
	"math/bits"

	kolocodec "github.com/ciranda-dev/kolo-codec"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	keyLabel   = "kolo-group/history/v0"
	topicLabel = "kolo-group/topic/v0"
	aadLabel   = "kolo-group/message/v0"
	keySize    = chacha20poly1305.KeySize
	nonceSize  = chacha20poly1305.NonceSizeX
)

// epochKey is K_e: the key every member of an epoch derives from the MLS exporter.
func epochKey(export func(label string, context []byte, length int) ([]byte, error), gid []byte, epoch uint64) ([keySize]byte, error) {
	var e kolocodec.Encoder
	e.PutBytes(gid)
	e.PutUvarint(epoch)
	b, err := export(keyLabel, e.Bytes(), keySize)
	if err != nil {
		return [keySize]byte{}, err
	}
	defer clear(b)
	return [keySize]byte(b), nil
}

// aad binds a sealed message to its room, epoch, device and sequence number.
func aad(gid []byte, epoch uint64, device []byte, seq uint64) []byte {
	var e kolocodec.Encoder
	e.PutString(aadLabel)
	e.PutBytes(gid)
	e.PutUvarint(epoch)
	e.PutBytes(device)
	e.PutUvarint(seq)
	return e.Bytes()
}

// seal encrypts plain under key with a random nonce; the nonce is returned.
func seal(key *[keySize]byte, ad, plain []byte) (nonce [nonceSize]byte, ct []byte, err error) {
	c, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nonce, nil, err
	}
	if _, err := rand.Read(nonce[:]); err != nil {
		return nonce, nil, err
	}
	return nonce, c.Seal(nil, nonce[:], plain, ad), nil
}

// open decrypts ct; it returns ErrOpen when the key, nonce or additional data differ.
func open(key *[keySize]byte, nonce [nonceSize]byte, ad, ct []byte) ([]byte, error) {
	c, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	pt, err := c.Open(nil, nonce[:], ct, ad)
	if err != nil {
		return nil, fmt.Errorf("%w", ErrOpen)
	}
	return pt, nil
}

// padme rounds n up to a length that leaks little about it (Padme, Nikitin et al., PETS 2019).
func padme(n int) int {
	if n < 2 {
		return n
	}
	e := bits.Len(uint(n)) - 1
	s := bits.Len(uint(e))
	mask := 1<<(e-s) - 1
	return (n + mask) &^ mask
}
