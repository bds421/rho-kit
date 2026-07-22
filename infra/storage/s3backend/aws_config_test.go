package s3backend

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/require"
)

func TestBuildAWSConfigPreservesNativeAWSRequestChecksumPolicy(t *testing.T) {
	t.Setenv("AWS_REQUEST_CHECKSUM_CALCULATION", "WHEN_SUPPORTED")
	t.Setenv("AWS_RESPONSE_CHECKSUM_VALIDATION", "WHEN_SUPPORTED")
	config, err := buildAWSConfig(context.Background(), Config{
		Region: "us-east-1", Bucket: "bucket", AccessKeyID: "access", SecretAccessKey: "secret",
	})
	require.NoError(t, err)
	require.Equal(t, aws.RequestChecksumCalculationWhenSupported, config.RequestChecksumCalculation)
	require.Equal(t, aws.ResponseChecksumValidationWhenSupported, config.ResponseChecksumValidation)
}

func TestBuildAWSConfigUsesCompatibleRequestChecksumsForCustomEndpoint(t *testing.T) {
	t.Setenv("AWS_REQUEST_CHECKSUM_CALCULATION", "WHEN_SUPPORTED")
	t.Setenv("AWS_RESPONSE_CHECKSUM_VALIDATION", "WHEN_SUPPORTED")
	config, err := buildAWSConfig(context.Background(), Config{
		Region: "us-east-1", Bucket: "bucket", Endpoint: "https://minio.example.test",
		AccessKeyID: "access", SecretAccessKey: "secret",
	})
	require.NoError(t, err)
	require.Equal(t, aws.RequestChecksumCalculationWhenRequired, config.RequestChecksumCalculation)
	require.Equal(t, aws.ResponseChecksumValidationWhenSupported, config.ResponseChecksumValidation)
}
