package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// deliveryTimeout bounds one attempt. Notifications are not retried, so a dead
// channel costs one timeout per alert and is reported in the events log.
const deliveryTimeout = 10 * time.Second

// Sender delivers one message to one channel.
type Sender func(ctx context.Context, channel Channel, title, message string) error

var deliveryClient = &http.Client{Timeout: deliveryTimeout}

func deliver(ctx context.Context, channel Channel, title, message string) error {
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	body, contentType, headers, err := buildPayload(channel.Type, title, message)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, channel.URL, bytes.NewReader(body))
	if err != nil {
		return errors.New("the channel URL is not valid")
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "token-proxy-router")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := deliveryClient.Do(req)
	if err != nil {
		return safeError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("the channel answered HTTP %d", resp.StatusCode)
	}
	return nil
}

// safeError drops the URL that net/http puts in its errors: it is a secret.
func safeError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return errors.New("the channel did not answer in time")
		}
		return errors.New("could not reach the channel")
	}
	return errors.New("could not reach the channel")
}

// buildPayload shapes a message for each service.
func buildPayload(channelType, title, message string) (body []byte, contentType string, headers map[string]string, err error) {
	switch channelType {
	case "ntfy":
		return []byte(message), "text/plain; charset=utf-8", map[string]string{"Title": title, "Tags": "warning"}, nil
	case "discord":
		body, err = json.Marshal(map[string]string{"content": title + ": " + message})
	case "slack":
		body, err = json.Marshal(map[string]string{"text": title + ": " + message})
	case "webhook":
		body, err = json.Marshal(map[string]string{"title": title, "message": message})
	default:
		return nil, "", nil, fmt.Errorf("unknown channel type %q", channelType)
	}
	return body, "application/json", nil, err
}
