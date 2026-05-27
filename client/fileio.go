package client

import "os"

func init() {
	readFile = os.ReadFile
	writeFile = func(path string, data []byte) error {
		return os.WriteFile(path, data, 0o600)
	}
}
