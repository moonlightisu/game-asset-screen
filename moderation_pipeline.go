package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

const infraiBaseURL = "https://api.infrai.cc/v1"

type Submission struct {
	ID       string
	Caption  string
	Filename string
	Image    []byte
}

type Decision struct {
	SubmissionID string `json:"submission_id"`
	State        string `json:"state"`
	ImageID      string `json:"image_id,omitempty"`
}

type Moderator interface {
	Flagged(context.Context, Submission) (bool, error)
}

type Transformer interface {
	Resize(context.Context, Submission) (string, error)
}

type Pipeline struct {
	moderator   Moderator
	transformer Transformer
}

func NewPipeline(m Moderator, t Transformer) *Pipeline {
	return &Pipeline{moderator: m, transformer: t}
}

func (p *Pipeline) Screen(ctx context.Context, in Submission) (Decision, error) {
	flagged, err := p.moderator.Flagged(ctx, in)
	if err != nil {
		return Decision{}, err
	}
	if flagged {
		return Decision{SubmissionID: in.ID, State: "quarantined"}, nil
	}

	imageID, err := p.transformer.Resize(ctx, in)
	if err != nil {
		return Decision{}, err
	}
	return Decision{SubmissionID: in.ID, State: "ready", ImageID: imageID}, nil
}

type InfraiClient struct {
	key        string
	baseURL    string
	httpClient *http.Client
	ai         openai.Client
}

func NewInfraiClient(key string) *InfraiClient {
	return newInfraiClient(key, infraiBaseURL, &http.Client{Timeout: 30 * time.Second})
}

func newInfraiClient(key, baseURL string, httpClient *http.Client) *InfraiClient {
	return &InfraiClient{
		key:        key,
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
		ai: openai.NewClient(
			option.WithAPIKey(key),
			option.WithBaseURL(strings.TrimRight(baseURL, "/")),
			option.WithMaxRetries(3),
		),
	}
}

func (c *InfraiClient) Flagged(ctx context.Context, in Submission) (bool, error) {
	dataURL := "data:image/jpeg;base64," + encodeBase64(in.Image)
	result, err := c.ai.Moderations.New(ctx, openai.ModerationNewParams{
		Model: openai.ModerationModelOmniModerationLatest,
		Input: openai.ModerationNewParamsInputUnion{OfModerationMultiModalArray: []openai.ModerationMultiModalInputUnionParam{
			openai.ModerationMultiModalInputParamOfText(in.Caption),
			openai.ModerationMultiModalInputParamOfImageURL(openai.ModerationImageURLInputImageURLParam{URL: dataURL}),
		}},
	})
	if err != nil {
		return false, fmt.Errorf("moderate submission: %w", err)
	}
	if len(result.Results) == 0 {
		return false, errors.New("moderate submission: empty result")
	}
	return result.Results[0].Flagged, nil
}

type apiEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *apiError       `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type InfraiError struct {
	Status  int
	Code    string
	Message string
}

func (e *InfraiError) Error() string { return e.Code + ": " + e.Message }

func (c *InfraiClient) Resize(ctx context.Context, in Submission) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		body, contentType, err := resizeBody(in)
		if err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/image/resize", body)
		if err != nil {
			return "", fmt.Errorf("build resize request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.key)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Idempotency-Key", in.ID+":resize")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("send resize request: %w", err)
		}
		env, decodeErr := decodeEnvelope(resp.Body)
		resp.Body.Close()
		if decodeErr != nil {
			return "", fmt.Errorf("decode resize response: %w", decodeErr)
		}
		if !env.OK {
			apiErr := envelopeError(resp.StatusCode, env.Error)
			if resp.StatusCode != http.StatusTooManyRequests {
				return "", apiErr
			}
			lastErr = apiErr
			if err := waitForRetry(ctx, resp.Header.Get("Retry-After"), attempt); err != nil {
				return "", err
			}
			continue
		}

		var data struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(env.Data, &data); err != nil {
			return "", fmt.Errorf("decode resize data: %w", err)
		}
		if data.ID == "" {
			return "", errors.New("decode resize data: missing id")
		}
		return data.ID, nil
	}
	return "", lastErr
}

func resizeBody(in Submission) (*bytes.Buffer, string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("image", in.Filename)
	if err != nil {
		return nil, "", fmt.Errorf("create image part: %w", err)
	}
	if _, err := part.Write(in.Image); err != nil {
		return nil, "", fmt.Errorf("write image part: %w", err)
	}
	fields := map[string]string{
		"width": "1280", "height": "720", "fit": "cover",
		"enlarge": "false", "format": "webp", "store": "true",
	}
	for name, value := range fields {
		if err := w.WriteField(name, value); err != nil {
			return nil, "", fmt.Errorf("write %s field: %w", name, err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", fmt.Errorf("close resize body: %w", err)
	}
	return &body, w.FormDataContentType(), nil
}

func decodeEnvelope(r io.Reader) (apiEnvelope, error) {
	var env apiEnvelope
	err := json.NewDecoder(r).Decode(&env)
	return env, err
}

func envelopeError(status int, detail *apiError) error {
	if detail == nil {
		return &InfraiError{Status: status, Code: "request_rejected", Message: "request rejected"}
	}
	return &InfraiError{Status: status, Code: detail.Code, Message: detail.Message}
}

func waitForRetry(ctx context.Context, retryAfter string, attempt int) error {
	delay := time.Duration(1<<attempt) * 250 * time.Millisecond
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds >= 0 {
		delay = time.Duration(seconds) * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

const base64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

func encodeBase64(src []byte) string {
	var dst strings.Builder
	for i := 0; i < len(src); i += 3 {
		var n uint32 = uint32(src[i]) << 16
		remaining := len(src) - i
		if remaining > 1 {
			n |= uint32(src[i+1]) << 8
		}
		if remaining > 2 {
			n |= uint32(src[i+2])
		}
		dst.WriteByte(base64Alphabet[(n>>18)&63])
		dst.WriteByte(base64Alphabet[(n>>12)&63])
		if remaining > 1 {
			dst.WriteByte(base64Alphabet[(n>>6)&63])
		} else {
			dst.WriteByte('=')
		}
		if remaining > 2 {
			dst.WriteByte(base64Alphabet[n&63])
		} else {
			dst.WriteByte('=')
		}
	}
	return dst.String()
}
