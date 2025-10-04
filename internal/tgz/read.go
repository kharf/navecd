package tgz

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
)

func Read(archiveFilePath string, targetDir string) error {
	archiveFile, err := os.Open(archiveFilePath)
	if err != nil {
		return err
	}
	defer archiveFile.Close()

	zipReader, err := gzip.NewReader(archiveFile)
	if err != nil {
		return err
	}
	defer zipReader.Close()
	tarReader := tar.NewReader(zipReader)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return err
		}

		if header.Typeflag == tar.TypeReg {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(targetDir, header.Name)), 0700); err != nil {
				return err
			}

			dst, err := os.Create(filepath.Join(targetDir, header.Name))
			if err != nil {
				return err
			}
			defer dst.Close()

			_, err = io.Copy(dst, tarReader)
			if err != nil {
				return err
			}
		}
	}

	return nil
}
