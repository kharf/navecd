package tgz

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
)

func Create(sourceDir string, targetArchiveFilePath string) error {
	archive, err := os.Create(targetArchiveFilePath)
	if err != nil {
		return err
	}
	defer archive.Close()

	gzipWriter := gzip.NewWriter(archive)
	defer gzipWriter.Close()

	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	return filepath.Walk(sourceDir, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(sourceDir, filePath)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relPath)

		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		if info.Mode().IsRegular() {
			file, err := os.Open(filePath)
			if err != nil {
				return err
			}
			defer file.Close()

			_, err = io.Copy(tarWriter, file)
			return err
		}

		return nil
	})
}
