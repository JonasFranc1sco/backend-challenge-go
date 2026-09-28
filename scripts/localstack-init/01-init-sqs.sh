#!/bin/bash
set -euo pipefail

echo "Initializing LocalStack AWS SQS FIFO Queues..."

AWS_REGION="${AWS_DEFAULT_REGION:-us-east-1}"
ENDPOINT="http://localhost:4566"

# Create DLQ FIFO Queue
echo "Creating wager-transactions-dlq.fifo..."
awslocal sqs create-queue \
  --queue-name wager-transactions-dlq.fifo \
  --attributes '{"FifoQueue":"true","ContentBasedDeduplication":"false","MessageRetentionPeriod":"1209600"}' \
  --region "${AWS_REGION}"

# Get DLQ ARN
DLQ_ARN=$(awslocal sqs get-queue-attributes \
  --queue-url "${ENDPOINT}/000000000000/wager-transactions-dlq.fifo" \
  --attribute-names QueueArn \
  --query 'Attributes.QueueArn' \
  --output text \
  --region "${AWS_REGION}")

echo "DLQ ARN: ${DLQ_ARN}"

# Create Main FIFO Queue with RedrivePolicy (maxReceiveCount: 5)
echo "Creating wager-transactions.fifo with redrive policy..."
awslocal sqs create-queue \
  --queue-name wager-transactions.fifo \
  --attributes "{\"FifoQueue\":\"true\",\"ContentBasedDeduplication\":\"false\",\"VisibilityTimeout\":\"30\",\"RedrivePolicy\":\"{\\\"deadLetterTargetArn\\\":\\\"${DLQ_ARN}\\\",\\\"maxReceiveCount\\\":\\\"5\\\"}\"}" \
  --region "${AWS_REGION}"

# Create Outbox Events FIFO Queue
echo "Creating wager-events.fifo..."
awslocal sqs create-queue \
  --queue-name wager-events.fifo \
  --attributes '{"FifoQueue":"true","ContentBasedDeduplication":"false","VisibilityTimeout":"30"}' \
  --region "${AWS_REGION}"

echo "LocalStack SQS FIFO queues successfully initialized!"
