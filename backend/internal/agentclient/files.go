package agentclient

import "os"

func osCreate(dest string) (*os.File, error) {
	return os.Create(dest)
}
