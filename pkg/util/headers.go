package util

import (
	"bytes"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
)

var (
	headerComment = []byte("# Generated by \"kubeapply expand\". DO NOT EDIT.\n")
)

// AddHeaders adds a comment header to all yaml files in the argument path.
func AddHeaders(root string) error {
	return filepath.Walk(
		root,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if info.IsDir() || !strings.HasSuffix(path, ".yaml") {
				return nil
			}

			fileBytes, err := ioutil.ReadFile(path)
			if err != nil {
				return err
			}

			if bytes.HasPrefix(fileBytes, headerComment) {
				return nil
			}

			fileBytes = append(headerComment, fileBytes...)

			return ioutil.WriteFile(path, fileBytes, info.Mode().Perm())
		},
	)
}
