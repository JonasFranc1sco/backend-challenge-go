package sqs

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
)

// EventPublisher defines the contract for dispatching domain events to a broker.
type EventPublisher interface {
	Publish(ctx context.Context, eventID string, messageGroupID string, payload []byte) error
}

type SQSEventPublisher struct {
	client   *awssqs.Client
	queueURL string
}

func NewSQSEventPublisher(client *awssqs.Client, eventsQueueURL string) *SQSEventPublisher {
	return &SQSEventPublisher{
		client:   client,
		queueURL: eventsQueueURL,
	}
}

// Publish dispatches a domain event to the FIFO SQS queue.
// Preserves eventID across republishing as MessageDeduplicationId.
func (p *SQSEventPublisher) Publish(ctx context.Context, eventID, messageGroupID string, payload []byte) error {
	if p.queueURL == "" {
		return fmt.Errorf("events queue URL is not configured")
	}

	body := string(payload)
	input := &awssqs.SendMessageInput{
		QueueUrl:               aws.String(p.queueURL),
		MessageBody:            aws.String(body),
		MessageGroupId:         aws.String(messageGroupID),
		MessageDeduplicationId: aws.String(eventID),
	}

	_, err := p.client.SendMessage(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to send event %s to SQS FIFO queue: %w", eventID, err)
	}

	return nil
}
