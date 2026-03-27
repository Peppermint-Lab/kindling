package volumebackup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	cfg "github.com/kindlingvm/kindling/internal/config"
)

type Store interface {
	Configured() bool
	UploadFile(ctx context.Context, objectKey, srcPath string) (storageURL string, sizeBytes int64, err error)
	DownloadFile(ctx context.Context, objectKey, dstPath string) error
	DeleteObject(ctx context.Context, objectKey string) error
}

type S3Store struct {
	client *s3.Client
	bucket string
	prefix string
}

func NewStoreFromSnapshot(ctx context.Context, snap *cfg.Snapshot) (Store, error) {
	if snap == nil {
		return nil, fmt.Errorf("backup store snapshot is required")
	}
	if strings.TrimSpace(snap.VolumeBackupS3Bucket) == "" {
		return nil, fmt.Errorf("volume backup bucket is not configured")
	}
	if strings.TrimSpace(snap.VolumeBackupS3Region) == "" {
		return nil, fmt.Errorf("volume backup region is not configured")
	}
	if strings.TrimSpace(snap.VolumeBackupS3AccessKeyID) == "" || strings.TrimSpace(snap.VolumeBackupS3SecretAccessKey) == "" {
		return nil, fmt.Errorf("volume backup access key is not configured")
	}

	loadOpts := []func(*config.LoadOptions) error{
		config.WithRegion(strings.TrimSpace(snap.VolumeBackupS3Region)),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			snap.VolumeBackupS3AccessKeyID,
			snap.VolumeBackupS3SecretAccessKey,
			"",
		)),
	}
	if endpoint := strings.TrimSpace(snap.VolumeBackupS3Endpoint); endpoint != "" {
		loadOpts = append(loadOpts, config.WithBaseEndpoint(endpoint))
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("load s3 config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if strings.TrimSpace(snap.VolumeBackupS3Endpoint) != "" {
			o.UsePathStyle = true
		}
	})
	return &S3Store{
		client: client,
		bucket: strings.TrimSpace(snap.VolumeBackupS3Bucket),
		prefix: strings.Trim(strings.TrimSpace(snap.VolumeBackupS3Prefix), "/"),
	}, nil
}

func (s *S3Store) Configured() bool {
	return s != nil && s.client != nil && s.bucket != ""
}

func (s *S3Store) UploadFile(ctx context.Context, objectKey, srcPath string) (string, int64, error) {
	if !s.Configured() {
		return "", 0, fmt.Errorf("backup store is not configured")
	}
	file, err := os.Open(srcPath)
	if err != nil {
		return "", 0, fmt.Errorf("open source file: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", 0, fmt.Errorf("stat source file: %w", err)
	}
	key := s.objectKey(objectKey)
	uploader := manager.NewUploader(s.client)
	if _, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   file,
	}); err != nil {
		return "", 0, fmt.Errorf("upload to s3: %w", err)
	}
	return fmt.Sprintf("s3://%s/%s", s.bucket, key), info.Size(), nil
}

func (s *S3Store) DownloadFile(ctx context.Context, objectKey, dstPath string) error {
	if !s.Configured() {
		return fmt.Errorf("backup store is not configured")
	}
	resp, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(objectKey)),
	})
	if err != nil {
		return fmt.Errorf("download from s3: %w", err)
	}
	defer resp.Body.Close()

	file, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("create destination file: %w", err)
	}
	defer file.Close()
	if _, err := io.Copy(file, resp.Body); err != nil {
		return fmt.Errorf("write downloaded backup: %w", err)
	}
	return nil
}

func (s *S3Store) DeleteObject(ctx context.Context, objectKey string) error {
	if !s.Configured() {
		return fmt.Errorf("backup store is not configured")
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(objectKey)),
	})
	if err != nil {
		return fmt.Errorf("delete s3 object: %w", err)
	}
	return nil
}

func (s *S3Store) objectKey(objectKey string) string {
	key := strings.Trim(strings.TrimSpace(objectKey), "/")
	if s.prefix == "" {
		return key
	}
	return path.Join(s.prefix, key)
}

func BackupObjectKey(volumeID, backupID uuid.UUID) string {
	return path.Join("project-volumes", volumeID.String(), "backups", backupID.String()+".qcow2")
}

func MoveObjectKey(operationID uuid.UUID) string {
	return path.Join("project-volumes", "moves", operationID.String()+".qcow2")
}
