package sysinfo

import "os"

// readFile reads a small file whole; /proc/meminfo is a few kilobytes.
func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
