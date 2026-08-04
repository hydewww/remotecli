package remotecli

import (
	"os"
	"path/filepath"
)

func configPath() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "remotecli", "config.json"), nil
}

func defaultRunRoot() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "remotecli", "runs"), nil
}
