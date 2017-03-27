package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"

	"cloud.google.com/go/pubsub"
	"golang.org/x/net/context"
	"golang.org/x/oauth2"
)

type PubSubOutput struct {
	ctx    context.Context
	client *pubsub.Client
	topic  *pubsub.Topic

	//connectTime                 time.Time
	//reconnectTime               time.Time
	//connected                   bool
	droppedEventCount int64
	//droppedEventSinceConnection int64

	//sync.RWMutex
}

type PubSubStatistics struct {
	Topic             string `json:"topic"`
	DroppedEventCount int64  `json:"dropped_event_count"`
}

// Initialize() expects a config string in the following format:
// project-id:topic-name:path-to-credentials
// for example: carbonblack-test:cb-test-topic:/root/google/credentials.json
// maps to PubSub topic 'projects/carbonblack-test/topics/cb-test-topic'
// using Google application credentials at /root/google/credentials.json
func (o *PubSubOutput) Initialize(configStr string) error {
	// Validate and set config fields
	parts := strings.SplitN(configStr, ":", 3)
	if len(parts) != 2 {
		return errors.New(fmt.Sprintf("Expecting 2 colon-deparated values in pubsubout, got: %v", configStr))
	}
	projectID := parts[0]
	topicName := parts[1]

	regex := `[a-z][a-z0-9]*(-[a-z0-9]+)*`
	if !regexp.MustCompile(regex).MatchString(projectID) {
		return errors.New(fmt.Sprintf("Invalid PubSub project ID %s, must match: %s", projectID, regex))
	}
	regex = `[A-Za-z][A-Za-z0-9\-\._~%\+]{2,254}`
	if !regexp.MustCompile(regex).MatchString(topicName) {
		return errors.New(fmt.Sprintf("Invalid PubSub topic name %s, must match: %s", topicName, regex))
	}

	var credsPath string
	if config.PubSubGoogleAppCreds != nil {
		credsPath = *config.PubSubGoogleAppCreds
		os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credsPath)
	} else {
		credsPath = os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	}
	if _, err := os.Stat(credsPath); err != nil {
		return errors.New(fmt.Sprintf("Error reading Google credentials file '%s': %v", credsPath, err))
	}

	// Create the PubSub client
	o.ctx = oauth2.NoContext
	client, err := pubsub.NewClient(o.ctx, projectID)
	if err != nil {
		return err
	}
	o.client = client

	// Validate topic exists on server
	o.topic = client.Topic(topicName)
	ok, err := o.topic.Exists(o.ctx)
	if err != nil {
		return errors.New(fmt.Sprintf("Failed to check if PubSub topic %s exists: %v", topicName, err))
	}
	if !ok {
		if config.PubSubCreateTopicIfMissing {
			topic, err := client.CreateTopic(context.Background(), topicName)
			if err != nil {
				return errors.New(fmt.Sprintf("Failed to create PubSub topic %s: %v", topicName,
					err))
			}
			o.topic = topic
		} else {
			return errors.New(fmt.Sprintf("PubSub topic %s does not exist", topicName))
		}
	}
	o.topic.PublishSettings = *config.PubSubPublishSettings

	return nil
}

func (o *PubSubOutput) Key() string {
	return o.topic.String()
}

func (o *PubSubOutput) String() string {
	return "Google Cloud PubSub Topic " + o.Key()
}

func (o *PubSubOutput) Statistics() interface{} {
	return PubSubStatistics{
		Topic:             o.topic.String(),
		DroppedEventCount: o.droppedEventCount,
	}
}

func (o *PubSubOutput) output(m string) error {
	b, err := json.Marshal(m)
	ctx := context.Background()
	if err != nil {
		// drop this event on the floor...
		atomic.AddInt64(&o.droppedEventCount, 1)
		return nil
	}
	_, err = o.topic.Publish(ctx, &pubsub.Message{Data: b}).Get(ctx)
	return err
}

func (o *PubSubOutput) Go(messages <-chan string, errorChan chan<- error) error {
	go func() {
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		defer signal.Stop(hup)

		for message := range messages {
			if err := o.output(message); err != nil {
				errorChan <- err
			}
		}
	}()
	return nil
}
