package sqs

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
)

type Config struct {
	Region          string
	Endpoint        string // LocalStack endpoint e.g. http://localhost:4566
	AccessKeyID     string
	SecretAccessKey string
	QueueURL        string
	DLQURL          string
	EventsQueueURL  string
}

// NewClient initializes an AWS SQS client configured for AWS or LocalStack.
func NewClient(ctx context.Context, cfg Config) (*awssqs.Client, error) {
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}

	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
	}

	// Use static credentials if provided (required for LocalStack)
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}

	// Custom endpoint for LocalStack
	if cfg.Endpoint != "" {
		opts = append(opts, awsconfig.WithEndpointResolverWithOptions(
			aws.EndpointResolverWithOptionsFunc(func(service, reg string, options ...any) (aws.Endpoint, error) {
				return aws.Endpoint{
					URL:               cfg.Endpoint,
					SigningRegion:     region,
					HostnameImmutable: true,
				}, nil
			}),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS configuration: %w", err)
	}

	client := awssqs.NewFromConfig(awsCfg)
	return client, nil
}
