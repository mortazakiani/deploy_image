package main

import (
	"archive/tar"
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

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func main() {
	// ===== Config =====
	minioEndpoint := "minio.example.com:9000"
	accessKey := "minioadmin"
	secretKey := "minioadmin"
	bucketName := "builds"
	objectName := "app.tgz"
	localFile := "/tmp/app.tgz"
	extractDir := "/tmp/app"
	imageName := "myrepo/myapp:latest" // your registry + repo + tag

	// Docker registry credentials
	registryUser := "myuser"
	registryPass := "mypass"
	registryServer := "https://index.docker.io/v1/" // Docker Hub; change for private registry

	ctx := context.Background()

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
}

// untarGz extracts a .tgz file to targetDir
func untarGz(src, targetDir string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		targetPath := filepath.Join(targetDir, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return err
			}
			outFile, err := os.Create(targetPath)
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				return err
			}
			outFile.Close()
		}
	}
	return nil
}

// buildDockerImage builds a Docker image from dir using Docker SDK
func buildDockerImage(ctx context.Context, srcDir, imageName string) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}

	buildCtx, err := createTar(srcDir)
	if err != nil {
		return err
	}

	buildResp, err := cli.ImageBuild(
		ctx,
		buildCtx,
		types.ImageBuildOptions{
			Tags:       []string{imageName},
			Dockerfile: "Dockerfile",
			Remove:     true,
		},
	)
	if err != nil {
		return err
	}
	defer buildResp.Body.Close()

	_, err = io.Copy(os.Stdout, buildResp.Body)
	return err
}

// createTar creates a tar stream from dir for Docker build context
func createTar(srcDir string) (io.Reader, error) {
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)
	defer tw.Close()

	err := filepath.Walk(srcDir, func(file string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(srcDir, file)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}

		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		hdr.Name = relPath

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return nil, err
	}

	return buf, nil
}

// encodeDockerAuth creates base64 auth string for Docker push
func encodeDockerAuth(username, password, server string) (string, error) {
	authConfig := types.AuthConfig{
		Username:      username,
		Password:      password,
		ServerAddress: server,
	}
	encodedJSON, err := json.Marshal(authConfig)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(encodedJSON), nil
}

// pushDockerImage pushes the built image with auth
func pushDockerImage(ctx context.Context, imageName, authStr string) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}

	pushResp, err := cli.ImagePush(ctx, imageName, types.ImagePushOptions{
		RegistryAuth: authStr,
	})
	if err != nil {
		return err
	}
	defer pushResp.Close()

	_, err = io.Copy(os.Stdout, pushResp)
	return err
}
