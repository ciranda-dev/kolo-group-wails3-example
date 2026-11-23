package room

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/bits"

	koloid "github.com/ciranda-dev/kolo-id"
	kololog "github.com/ciranda-dev/kolo-log"

	kolocodec "github.com/ciranda-dev/kolo-codec"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	keyLabel    = "kolo-group/epoch-key/v0"
	secretLabel = "kolo-group/room-secret/v0"
	topicLabel  = "kolo-group/topic/v0"
	aadLabel    = "kolo-group/message/v0"
	authorCtx   = "kolo-group/author/v0"
	idLabel     = "kolo-group/id/v0"
	logLabel    = "kolo-group/log/v0"
	keySize     = chacha20poly1305.KeySize
	nonceSize   = chacha20poly1305.NonceSizeX
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

// authorSigned is what a device signs for a sealed message: it binds the ciphertext to its room, epoch, device and sequence.
func authorSigned(ad []byte, nonce [nonceSize]byte, ct []byte) []byte {
	b := make([]byte, 0, len(ad)+nonceSize+len(ct))
	return append(append(append(b, ad...), nonce[:]...), ct...)
}

// signSealed signs a sealed message with the author's device key, so a stored copy proves who wrote it.
func signSealed(id *koloid.Identity, gid []byte, s *sealed, seq uint64) error {
	sig, err := id.Sign(authorCtx, authorSigned(aad(gid, s.Epoch, id.PublicKey(), seq), s.Nonce, s.CT))
	if err != nil {
		return err
	}
	copy(s.Sig[:], sig)
	return nil
}

// verifySealed checks the author's signature on a sealed message; it returns ErrSignature when it does not verify.
func verifySealed(device ed25519.PublicKey, gid []byte, s *sealed, seq uint64) error {
	if !koloid.Verify(device, authorCtx, authorSigned(aad(gid, s.Epoch, device, seq), s.Nonce, s.CT), s.Sig[:]) {
		return ErrSignature
	}
	return nil
}

// messageID names a message by what its author signed, so every holder of a copy computes the same id.
func messageID(gid, device []byte, seq uint64, body []byte) MessageID {
	h := sha256.New()
	h.Write([]byte(idLabel))
	h.Write(gid)
	h.Write(device)
	var e [8]byte
	for i := range e {
		e[i] = byte(seq >> (8 * i))
	}
	h.Write(e[:])
	h.Write(body)
	return MessageID(h.Sum(nil))
}

// logSecret is the entry secret of the room's logs: any member derives the keys of any entry, so a pin or a
// peer that holds no key serves ciphertext that every member opens. Messages stay sealed under the epoch keys.
func logSecret(roomSecret *[keySize]byte) (*kololog.EntrySecret, error) {
	b, err := hkdf.Key(sha256.New, roomSecret[:], nil, logLabel, kololog.EntrySecretSize)
	if err != nil {
		return nil, err
	}
	var s kololog.EntrySecret
	copy(s[:], b)
	clear(b)
	return &s, nil
}

func be(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

func unbe(b []byte) uint64 { return binary.BigEndian.Uint64(b) }

func sha256Of(b []byte) [32]byte { return sha256.Sum256(b) }
