package wecom

import (
	"crypto/aes"
	"crypto/cipher"
	crand "crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

type Crypto struct {
	token     string
	aesKey    []byte // 32 bytes AES-256 key
	receiveID string
}

func NewCrypto(token, encodingAESKey, receiveID string) (*Crypto, error) {
	if token == "" || encodingAESKey == "" {
		return nil, errors.New("missing token or encodingAESKey")
	}
	// AES Key: base64(EncodingAESKey + "=")
	key, err := base64.StdEncoding.DecodeString(encodingAESKey + "=")
	if err != nil {
		return nil, fmt.Errorf("invalid EncodingAESKey: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("decoded aes key len=%d, expect 32", len(key))
	}
	return &Crypto{token: token, aesKey: key, receiveID: receiveID}, nil
}

func (c *Crypto) VerifySignature(signature, timestamp, nonce, encrypted string) bool {
	parts := []string{c.token, timestamp, nonce, encrypted}
	sort.Strings(parts)
	raw := strings.Join(parts, "")
	sum := sha1.Sum([]byte(raw))
	calc := hex.EncodeToString(sum[:])
	return calc == signature
}

// Decrypt 解密得到原始XML消息内容，并校验 receiveID
func (c *Crypto) Decrypt(encrypted string) ([]byte, error) {
	cipherData, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	block, err := aes.NewCipher(c.aesKey)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	if len(cipherData)%block.BlockSize() != 0 {
		return nil, errors.New("ciphertext is not a multiple of block size")
	}
	iv := c.aesKey[:16]
	mode := cipher.NewCBCDecrypter(block, iv)
	plain := make([]byte, len(cipherData))
	mode.CryptBlocks(plain, cipherData)

	unpadded, err := pkcs7Unpad(plain, block.BlockSize())
	if err != nil {
		return nil, fmt.Errorf("pkcs7 unpad: %w", err)
	}
	if len(unpadded) < 20 {
		return nil, errors.New("plaintext too short")
	}
	// 16B random | 4B BE msgLen | msg | receiveID
	msgLen := binary.BigEndian.Uint32(unpadded[16:20])
	end := 20 + int(msgLen)
	if end > len(unpadded) {
		return nil, errors.New("invalid message length in plaintext")
	}
	msg := unpadded[20:end]
	recv := string(unpadded[end:])
	if c.receiveID != "" && recv != c.receiveID {
		return nil, fmt.Errorf("receive id mismatch: got %q, want %q", recv, c.receiveID)
	}
	return msg, nil
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("invalid padding size")
	}
	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > blockSize || padLen > len(data) {
		return nil, errors.New("invalid padding")
	}
	for i := 0; i < padLen; i++ {
		if data[len(data)-1-i] != byte(padLen) {
			return nil, errors.New("invalid padding content")
		}
	}
	return data[:len(data)-padLen], nil
}

// Encrypt 按企业微信协议加密明文（xml），返回 base64 的密文字符串。
func (c *Crypto) Encrypt(plainXML []byte) (string, error) {
	if len(c.aesKey) != 32 {
		return "", errors.New("invalid aes key")
	}
	if c.receiveID == "" {
		return "", errors.New("receiveID required for encryption")
	}
	// 16B 随机
	rand16 := make([]byte, 16)
	if _, err := io.ReadFull(crand.Reader, rand16); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	// 4B 长度
	msgLen := make([]byte, 4)
	binary.BigEndian.PutUint32(msgLen, uint32(len(plainXML)))
	// 拼接: random | len | xml | receiveID
	raw := make([]byte, 0, 16+4+len(plainXML)+len(c.receiveID))
	raw = append(raw, rand16...)
	raw = append(raw, msgLen...)
	raw = append(raw, plainXML...)
	raw = append(raw, []byte(c.receiveID)...)

	block, err := aes.NewCipher(c.aesKey)
	if err != nil {
		return "", fmt.Errorf("new cipher: %w", err)
	}
	padded := pkcs7Pad(raw, block.BlockSize())
	cipherData := make([]byte, len(padded))
	iv := c.aesKey[:16]
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(cipherData, padded)
	return base64.StdEncoding.EncodeToString(cipherData), nil
}

// Sign 计算回包签名
func (c *Crypto) Sign(timestamp, nonce, encrypted string) string {
	parts := []string{c.token, timestamp, nonce, encrypted}
	sort.Strings(parts)
	raw := strings.Join(parts, "")
	sum := sha1.Sum([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - (len(data) % blockSize)
	if pad == 0 {
		pad = blockSize
	}
	p := byte(pad)
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = p
	}
	return out
}
