package main

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// The device token is stored in the OS credential store (Windows Credential
// Manager, macOS Keychain, Secret Service on Linux), never in config.json.
// Anyone who can read the token can sync this user's files, so it gets the
// same protection as a saved browser password.
const keychainService = "Null Drive"

func keychainAccount(c Config) string { return c.ServerURL + "|" + c.DeviceName }

func saveToken(c Config, token string) error {
	return keyring.Set(keychainService, keychainAccount(c), token)
}

func loadToken(c Config) (string, error) {
	return keyring.Get(keychainService, keychainAccount(c))
}

func deleteToken(c Config) error {
	err := keyring.Delete(keychainService, keychainAccount(c))
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
