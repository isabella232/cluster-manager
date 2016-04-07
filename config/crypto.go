package config

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"io/ioutil"
	"os"
	"path"
	"strings"
)

func DecryptConfig(c *Config, encrypted string) (string, error) {
	keyBytes, err := ioutil.ReadFile(path.Join(c.ConfigPath, c.EncryptionKeyPath))
	if os.IsNotExist(err) {
		return encrypted, nil
	}

	return Decrypt(encrypted, string(keyBytes))
}

func Decrypt(encrypted string, key string) (string, error) {
	if key == "" {
		return encrypted, nil
	}

	keyBytes, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return "", err
	}

	parts := strings.SplitN(encrypted, ":", 2)
	iv, err := hex.DecodeString(parts[0])
	if err != nil {
		return "", err
	}

	data, err := hex.DecodeString(parts[1])
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return "", err
	}

	blockMode := cipher.NewCBCDecrypter(block, iv)
	if err != nil {
		return "", err
	}

	blockMode.CryptBlocks(data, data)

	return string(PKCS5UnPadding(data)), nil
}

func PKCS5UnPadding(src []byte) []byte {
	length := len(src)
	unpadding := int(src[length-1])
	return src[:(length - unpadding)]
}
