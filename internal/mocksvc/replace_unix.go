//go:build !windows

package mocksvc

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
