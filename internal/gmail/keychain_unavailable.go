//go:build !darwin || !cgo

package gmail

func KeychainAvailable() bool             { return false }
func keychainLoad(string) ([]byte, error) { return nil, ErrKeychainUnavailable }
func keychainStore(string, []byte) error  { return ErrKeychainUnavailable }
