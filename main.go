package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func main() {
	// ===== Config =====
	minioEndpoint := "37.32.8.181:32446"
	accessKey := "stage"
	secretKey := "Lj5UlGa6v6uUDMUk"
	bucketName := "test.test123"
	objectName := "nginx-1.28.0.tar.gz"
	localFile := "/tmp/app/nginx-1.28.0.tar.gz"
	extractDir := "/tmp/app/extracted"

	ctx := context.Background()

	// Ensure temp directories exist
	if err := os.MkdirAll(filepath.Dir(localFile), 0755); err != nil {
		log.Fatalf("Failed to create temp directory: %v", err)
	}

	// ===== 1️⃣ Download from MinIO =====
	fmt.Println("Downloading from MinIO...")
	minioClient, err := minio.New(minioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: false, // true if https
	})
	if err != nil {
		log.Fatalf("Failed to init MinIO client: %v", err)
	}

	err = minioClient.FGetObject(ctx, bucketName, objectName, localFile, minio.GetObjectOptions{})
	if err != nil {
		log.Fatalf("Failed to download object: %v", err)
	}
	fmt.Println("Downloaded:", localFile)

	// ===== 2️⃣ Extract .tgz =====
	fmt.Println("Extracting archive...")
	if err := untarGz(localFile, extractDir); err != nil {
		log.Fatalf("Failed to extract: %v", err)
	}
	fmt.Println("Extracted to:", extractDir)
}

// untarGz extracts a .tgz file to targetDir
func untarGz(src, targetDir string) error {
	// Ensure target directory exists
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("failed to create target directory: %v", err)
	}

	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file: %v", err)
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %v", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar header: %v", err)
		}

		// Normalize the path to prevent directory traversal
		cleanPath := filepath.Clean(header.Name)
		if strings.Contains(cleanPath, "..") {
			return fmt.Errorf("invalid file path: %s", header.Name)
		}

		targetPath := filepath.Join(targetDir, cleanPath)

		// Security check: ensure path is within target directory
		if !strings.HasPrefix(targetPath, filepath.Clean(targetDir)+string(os.PathSeparator)) {
			return fmt.Errorf("invalid file path: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %v", targetPath, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return fmt.Errorf("failed to create parent directory for %s: %v", targetPath, err)
			}
			outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("failed to create file %s: %v", targetPath, err)
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return fmt.Errorf("failed to copy file content: %v", err)
			}
			outFile.Close()
		}
	}
	return nil
}

// createTar creates a tar stream from dir for Docker build context
func createTar(srcDir string) (io.Reader, error) {
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)

	err := filepath.Walk(srcDir, func(file string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(srcDir, file)
		if err != nil {
			return err
		}

		// Skip directories in tar, they'll be created as needed
		if fi.IsDir() {
			return nil
		}

		// Skip hidden files and directories (optional)
		if strings.HasPrefix(filepath.Base(file), ".") && filepath.Base(file) != ".dockerignore" {
			return nil
		}

		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(relPath) // Ensure forward slashes for Docker

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		f, err := os.Open(file)
		if err != nil {
			return err
		}

		_, err = io.Copy(tw, f)
		f.Close() // Close immediately, not deferred in loop
		if err != nil {
			return err
		}

		return nil
	})

	// Close the tar writer and check for errors
	closeErr := tw.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to walk directory: %v", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("failed to close tar writer: %v", closeErr)
	}

	return buf, nil
}
