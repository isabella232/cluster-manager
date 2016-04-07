package config

import "testing"

func TestDecrypt(t *testing.T) {
	key := "+CYp6SnbG6v14/g136kdnx5oEOt34+aIOJrVpxSkMrA="
	encrypted := "c6adb7742a44cac7d52dbea2a7403522:a40af19d80e55cdd4d9ff8fb6199416e"
	decrypted, err := Decrypt(encrypted, key)
	if err != nil {
		t.Fatal(err)
	}
	if decrypted != "cattle" {
		t.Fatal("Failed to decrypt")
	}
}
