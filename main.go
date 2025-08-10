package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
)

func main() {
	// ===== Config =====
	minioEndpoint := "minio.example.com:9000"
	accessKey := "minioadmin"
	secretKey := "minioadmin"
	bucketName := "builds"
	objectName := "app.tgz"
	localFile := "/tmp/app/app.tgz"
	extractDir := "/tmp/app/extracted"
	imageName := "myrepo/myapp:latest" // your registry + repo + tag

	// Docker registry credentials
	registryUser := "myuser"
	registryPass := "mypass"
	registryServer := "https://index.docker.io/v1/" // Docker Hub; change for private registry

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

	// ===== 3️⃣ Build Docker Image =====
	fmt.Println("Building Docker image...")
	if err := buildDockerImage(ctx, extractDir, imageName); err != nil {
		log.Fatalf("Docker build failed: %v", err)
	}
	fmt.Println("Docker image built:", imageName)

	// ===== 4️⃣ Push Docker Image with Auth =====
	fmt.Println("Pushing Docker image...")
	authStr, err := encodeDockerAuth(registryUser, registryPass, registryServer)
	if err != nil {
		log.Fatalf("Failed to encode auth: %v", err)
	}
	if err := pushDockerImage(ctx, imageName, authStr); err != nil {
		log.Fatalf("Docker push failed: %v", err)
	}
	fmt.Println("Docker image pushed:", imageName)

	// Cleanup
	fmt.Println("Cleaning up temporary files...")
	if err := os.RemoveAll("/tmp/app"); err != nil {
		log.Printf("Warning: Failed to cleanup temp files: %v", err)
	}
	fmt.Println("Pipeline completed successfully!")
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

// buildDockerImage builds a Docker image from dir using Docker client
func buildDockerImage(ctx context.Context, srcDir, imageName string) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("failed to create Docker client: %v", err)
	}
	defer cli.Close()

	buildCtx, err := createTar(srcDir)
	if err != nil {
		return fmt.Errorf("failed to create build context: %v", err)
	}

	buildOptions := image.BuildOptions{
		Tags:           []string{imageName},
		Dockerfile:     "Dockerfile",
		Remove:         true,
		ForceRemove:    true,
		PullParent:     true,
		SuppressOutput: false,
	}

	buildResp, err := cli.ImageBuild(ctx, buildCtx, buildOptions)
	if err != nil {
		return fmt.Errorf("failed to build image: %v", err)
	}
	defer buildResp.Body.Close()

	// Stream build output
	scanner := bufio.NewScanner(buildResp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		var buildOutput struct {
			Stream string `json:"stream"`
			Error  string `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &buildOutput); err == nil {
			if buildOutput.Error != "" {
				return fmt.Errorf("build error: %s", buildOutput.Error)
			}
			if buildOutput.Stream != "" {
				fmt.Print(buildOutput.Stream)
			}
		} else {
			fmt.Println(line)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading build output: %v", err)
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

// encodeDockerAuth creates base64 auth string for Docker push
func encodeDockerAuth(username, password, server string) (string, error) {
	authConfig := registry.AuthConfig{
		Username:      username,
		Password:      password,
		ServerAddress: server,
	}
	encodedJSON, err := json.Marshal(authConfig)
	if err != nil {
		return "", fmt.Errorf("failed to marshal auth config: %v", err)
	}
	return base64.URLEncoding.EncodeToString(encodedJSON), nil
}

// pushDockerImage pushes the built image with auth
func pushDockerImage(ctx context.Context, imageName, authStr string) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("failed to create Docker client: %v", err)
	}
	defer cli.Close()

	pushOptions := image.PushOptions{
		RegistryAuth: authStr,
	}

	pushResp, err := cli.ImagePush(ctx, imageName, pushOptions)
	if err != nil {
		return fmt.Errorf("failed to push image: %v", err)
	}
	defer pushResp.Close()

	// Stream push output
	scanner := bufio.NewScanner(pushResp)
	for scanner.Scan() {
		line := scanner.Text()
		var pushOutput struct {
			Status   string `json:"status"`
			Progress string `json:"progress"`
			Error    string `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &pushOutput); err == nil {
			if pushOutput.Error != "" {
				return fmt.Errorf("push error: %s", pushOutput.Error)
			}
			if pushOutput.Status != "" {
				if pushOutput.Progress != "" {
					fmt.Printf("%s: %s\n", pushOutput.Status, pushOutput.Progress)
				} else {
					fmt.Println(pushOutput.Status)
				}
			}
		} else {
			fmt.Println(line)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading push output: %v", err)
	}

	return nil
}
