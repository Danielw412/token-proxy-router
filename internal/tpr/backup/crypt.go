// Package backup writes and restores encrypted archives of the data folder.
//
// An archive is a small header followed by AES-256-GCM chunks of a gzipped tar.
// The key comes from the passphrase through scrypt. Every chunk is authenticated
// with its position and whether it is the last one, so any bit flip, reordering
// or truncation fails decryption.
package backup

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/scrypt"
)

const (
	magic          = "TPRBAK01"
	chunkSize      = 1 << 20
	saltSize       = 16
	noncePrefixLen = 7
	// MinPassphrase is the shortest passphrase accepted.
	MinPassphrase = 12

	// scrypt cost. Restore rejects anything outside these bounds, so a crafted
	// header cannot make the server burn memory.
	scryptN    = 1 << 15
	scryptR    = 8
	scryptP    = 1
	maxScryptN = 1 << 20
	maxScryptR = 16
	maxScryptP = 4
)

// ErrBadPassphrase means decryption failed at the first chunk: the passphrase is
// wrong, or the very start of the file is damaged.
var ErrBadPassphrase = errors.New("wrong passphrase, or the archive is damaged")

// ErrCorrupt means the archive was damaged or changed after it was written.
var ErrCorrupt = errors.New("the archive is damaged or was changed after it was written")

func deriveKey(passphrase string, salt []byte, n, r, p int) (cipher.AEAD, error) {
	key, err := scrypt.Key([]byte(passphrase), salt, n, r, p, 32)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// CheckPassphrase enforces the minimum length.
func CheckPassphrase(passphrase string) error {
	if len(passphrase) < MinPassphrase {
		return fmt.Errorf("the passphrase must be at least %d characters", MinPassphrase)
	}
	return nil
}

func chunkNonce(prefix []byte, index uint32, last bool) []byte {
	nonce := make([]byte, 12)
	copy(nonce, prefix)
	if last {
		nonce[noncePrefixLen] = 1
	}
	binary.BigEndian.PutUint32(nonce[8:], index)
	return nonce
}

type encryptWriter struct {
	w      io.Writer
	aead   cipher.AEAD
	prefix []byte
	buf    []byte
	index  uint32
	closed bool
}

// newEncryptWriter writes the header and returns a writer for the plaintext.
func newEncryptWriter(w io.Writer, passphrase string) (*encryptWriter, error) {
	if err := CheckPassphrase(passphrase); err != nil {
		return nil, err
	}
	salt := make([]byte, saltSize)
	prefix := make([]byte, noncePrefixLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(prefix); err != nil {
		return nil, err
	}
	aead, err := deriveKey(passphrase, salt, scryptN, scryptR, scryptP)
	if err != nil {
		return nil, err
	}
	var header bytes.Buffer
	header.WriteString(magic)
	header.Write(salt)
	for _, value := range []uint32{scryptN, scryptR, scryptP} {
		_ = binary.Write(&header, binary.BigEndian, value)
	}
	header.Write(prefix)
	if _, err := w.Write(header.Bytes()); err != nil {
		return nil, err
	}
	return &encryptWriter{w: w, aead: aead, prefix: prefix, buf: make([]byte, 0, chunkSize)}, nil
}

func (e *encryptWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		room := chunkSize - len(e.buf)
		take := min(room, len(p))
		e.buf = append(e.buf, p[:take]...)
		p = p[take:]
		written += take
		if len(e.buf) == chunkSize && len(p) > 0 {
			if err := e.flush(false); err != nil {
				return written, err
			}
		}
	}
	return written, nil
}

func (e *encryptWriter) flush(last bool) error {
	sealed := e.aead.Seal(nil, chunkNonce(e.prefix, e.index, last), e.buf, nil)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(sealed)))
	if _, err := e.w.Write(size[:]); err != nil {
		return err
	}
	if _, err := e.w.Write(sealed); err != nil {
		return err
	}
	e.index++
	e.buf = e.buf[:0]
	return nil
}

// Close seals the final chunk. Without it the archive reads as truncated.
func (e *encryptWriter) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	return e.flush(true)
}

type decryptReader struct {
	stream io.Reader
	aead   cipher.AEAD
	prefix []byte
	index  uint32
	plain  []byte
	done   bool
}

// newDecryptReader reads the header and prepares the key. Decryption errors show
// up on Read.
func newDecryptReader(r io.Reader, passphrase string) (*decryptReader, error) {
	head := make([]byte, len(magic)+saltSize+12+noncePrefixLen)
	if _, err := io.ReadFull(r, head); err != nil {
		return nil, errors.New("this is not a Token Proxy Router backup: the file is too short")
	}
	if string(head[:len(magic)]) != magic {
		return nil, errors.New("this is not a Token Proxy Router backup")
	}
	offset := len(magic)
	salt := head[offset : offset+saltSize]
	offset += saltSize
	n := binary.BigEndian.Uint32(head[offset:])
	cost := binary.BigEndian.Uint32(head[offset+4:])
	parallel := binary.BigEndian.Uint32(head[offset+8:])
	offset += 12
	if n < 1<<10 || n > maxScryptN || n&(n-1) != 0 || cost == 0 || cost > maxScryptR || parallel == 0 || parallel > maxScryptP {
		return nil, ErrCorrupt
	}
	aead, err := deriveKey(passphrase, salt, int(n), int(cost), int(parallel))
	if err != nil {
		return nil, err
	}
	return &decryptReader{stream: r, aead: aead, prefix: head[offset:]}, nil
}

func (d *decryptReader) Read(p []byte) (int, error) {
	for len(d.plain) == 0 {
		if d.done {
			return 0, io.EOF
		}
		if err := d.nextChunk(); err != nil {
			return 0, err
		}
	}
	n := copy(p, d.plain)
	d.plain = d.plain[n:]
	return n, nil
}

func (d *decryptReader) nextChunk() error {
	var size [4]byte
	if _, err := io.ReadFull(d.stream, size[:]); err != nil {
		return ErrCorrupt
	}
	length := binary.BigEndian.Uint32(size[:])
	if length > chunkSize+uint32(d.aead.Overhead()) || length < uint32(d.aead.Overhead()) {
		return ErrCorrupt
	}
	sealed := make([]byte, length)
	if _, err := io.ReadFull(d.stream, sealed); err != nil {
		return ErrCorrupt
	}
	// A chunk says whether it is the last one by which nonce opens it.
	if plain, err := d.aead.Open(nil, chunkNonce(d.prefix, d.index, false), sealed, nil); err == nil {
		d.plain = plain
		d.index++
		return nil
	}
	plain, err := d.aead.Open(nil, chunkNonce(d.prefix, d.index, true), sealed, nil)
	if err != nil {
		if d.index == 0 {
			return ErrBadPassphrase
		}
		return ErrCorrupt
	}
	var extra [1]byte
	if n, _ := d.stream.Read(extra[:]); n != 0 {
		return ErrCorrupt
	}
	d.plain = plain
	d.index++
	d.done = true
	return nil
}
