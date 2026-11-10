package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

const fileMagic = "FSF1"

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

func randomID(bytes int) (string, error) {
	b, err := randomBytes(bytes)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomToken() (string, error) { return randomID(32) }

func tokenHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func deriveKey(master []byte, context string) []byte {
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte(context))
	return mac.Sum(nil)
}

func encryptGCM(master []byte, context string, plaintext []byte) ([]byte, []byte, error) {
	block, err := aes.NewCipher(deriveKey(master, context))
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce, err := randomBytes(gcm.NonceSize())
	if err != nil {
		return nil, nil, err
	}
	return gcm.Seal(nil, nonce, plaintext, []byte(context)), nonce, nil
}

func decryptGCM(master []byte, context string, ciphertext, nonce []byte) ([]byte, error) {
	block, err := aes.NewCipher(deriveKey(master, context))
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, errors.New("invalid nonce")
	}
	return gcm.Open(nil, nonce, ciphertext, []byte(context))
}

func (a *App) encryptText(context string, plain []byte) ([]byte, []byte, error) {
	return encryptGCM(a.cfg.MasterKey, context, plain)
}

func (a *App) decryptText(context string, ciphertext, nonce []byte) ([]byte, error) {
	return decryptGCM(a.cfg.MasterKey, context, ciphertext, nonce)
}

func encodeBytes(v []byte) string { return base64.RawStdEncoding.EncodeToString(v) }

func decodeBytes(v string) ([]byte, error) { return base64.RawStdEncoding.DecodeString(v) }

func (a *App) encryptFile(input io.Reader, output string, assetID string) (size int64, digest string, err error) {
	iv, err := randomBytes(aes.BlockSize)
	if err != nil {
		return 0, "", err
	}
	block, err := aes.NewCipher(deriveKey(a.cfg.MasterKey, "file-enc:"+assetID))
	if err != nil {
		return 0, "", err
	}
	stream := cipher.NewCTR(block, iv)
	mac := hmac.New(sha256.New, deriveKey(a.cfg.MasterKey, "file-mac:"+assetID))
	mac.Write(iv)
	f, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, "", err
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if _, err = f.Write([]byte(fileMagic)); err != nil {
		return 0, "", err
	}
	if _, err = f.Write(iv); err != nil {
		return 0, "", err
	}
	buf := make([]byte, 64*1024)
	for {
		var n int
		n, err = input.Read(buf)
		if n > 0 {
			plain := buf[:n]
			encrypted := make([]byte, n)
			stream.XORKeyStream(encrypted, plain)
			mac.Write(encrypted)
			if _, writeErr := f.Write(encrypted); writeErr != nil {
				return 0, "", writeErr
			}
			size += int64(n)
		}
		if errors.Is(err, io.EOF) {
			err = nil
			break
		}
		if err != nil {
			return 0, "", err
		}
	}
	return size, hex.EncodeToString(mac.Sum(nil)), nil
}

func (a *App) verifyFile(path, assetID, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	header := make([]byte, len(fileMagic)+aes.BlockSize)
	if _, err := io.ReadFull(f, header); err != nil {
		return err
	}
	if string(header[:len(fileMagic)]) != fileMagic {
		return errors.New("invalid encrypted file header")
	}
	mac := hmac.New(sha256.New, deriveKey(a.cfg.MasterKey, "file-mac:"+assetID))
	mac.Write(header[len(fileMagic):])
	if _, err := io.Copy(mac, f); err != nil {
		return err
	}
	actual := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(actual), []byte(expected)) {
		return errors.New("encrypted file integrity check failed")
	}
	return nil
}

func (a *App) decryptFileTo(path, assetID string, output io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	header := make([]byte, len(fileMagic)+aes.BlockSize)
	if _, err := io.ReadFull(f, header); err != nil {
		return err
	}
	if string(header[:len(fileMagic)]) != fileMagic {
		return errors.New("invalid encrypted file header")
	}
	block, err := aes.NewCipher(deriveKey(a.cfg.MasterKey, "file-enc:"+assetID))
	if err != nil {
		return err
	}
	stream := cipher.NewCTR(block, header[len(fileMagic):])
	buf := make([]byte, 64*1024)
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			plain := make([]byte, n)
			stream.XORKeyStream(plain, buf[:n])
			if _, err := output.Write(plain); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func createTemp(directory string) (*os.File, error) {
	for i := 0; i < 5; i++ {
		name, err := randomID(16)
		if err != nil {
			return nil, err
		}
		f, err := os.OpenFile(fmt.Sprintf("%s/%s.part", directory, name), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	return nil, errors.New("could not create temporary file")
}
