package sqs

import (
	"context"

	"github.com/JonasFranc1sco/backend-challenge-go/internal/config"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"
)

func ProvideSQSClient(cfg *config.Config) (*awssqs.Client, error) {
	sqsCfg := Config{
		Region:          cfg.AWSRegion,
		Endpoint:        cfg.AWSEndpoint,
		AccessKeyID:     cfg.AWSAccessKey,
		SecretAccessKey: cfg.AWSSecretKey,
		QueueURL:        cfg.SQSQueueURL,
		DLQURL:          cfg.SQSDLQURL,
		EventsQueueURL:  cfg.SQSEventsURL,
	}
	return NewClient(context.Background(), sqsCfg)
}

func ProvideEventPublisher(client *awssqs.Client, cfg *config.Config) EventPublisher {
	return NewSQSEventPublisher(client, cfg.SQSEventsURL)
}

var Module = fx.Module("sqs",
	fx.Provide(ProvideSQSClient),
	fx.Provide(ProvideEventPublisher),
)
