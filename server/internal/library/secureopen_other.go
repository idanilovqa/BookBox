//go:build !linux

package library

import "os"

func openRegularBeneath(rootPath string, relative string) (*os.File, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(relative)
}
