// Package notify delivers bounded incident reports without logging credentials.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/ibrahimmuh26/monitoring-container/internal/incidents"
)

type Sender interface {
	Send(context.Context, incidents.Event, incidents.Incident) (time.Duration, error)
}

type Telegram struct {
	client   *http.Client
	token    string
	sendLogs bool
}

var errDelivery = errors.New("Telegram delivery failed; notification remains queued")

func NewTelegram(tokenFile string, timeout time.Duration, sendLogs bool) (*Telegram, error) {
	info, err := os.Stat(tokenFile)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("Telegram token must be a readable regular file")
	}
	f, err := os.Open(tokenFile)
	if err != nil {
		return nil, errors.New("cannot read Telegram token file")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return nil, errors.New("invalid Telegram token file")
	}
	token := strings.TrimSpace(string(data))
	if !regexp.MustCompile(`^[0-9]{5,20}:[A-Za-z0-9_-]{20,128}$`).MatchString(token) {
		return nil, errors.New("invalid Telegram token format")
	}
	return &Telegram{token: token, sendLogs: sendLogs, client: &http.Client{Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (t *Telegram) Send(ctx context.Context, e incidents.Event, i incidents.Incident) (time.Duration, error) {
	title := "INCIDENT OPENED"
	if e.Kind == "condition_cleared" {
		title = "CONDITION CLEARED"
	}
	action := "none (monitoring only)"
	if i.Recovery == "requested" {
		action = "restart requested; post-restart health not yet verified"
	} else if i.Recovery != "" && i.Recovery != "not_requested" {
		action = "restart not completed: " + i.Recovery
	}
	text := title + " — " + i.ID + "\nServer: " + i.Observation.Server + "\nTarget: " + i.Observation.Target + "\nDetected: " + i.Observation.Time.Format(time.RFC3339) + "\nInitial condition: " + i.Reason + "\nEvidence: " + i.LogStatus + "\nAction: " + action + "\nApplication health: not fully verified. Root cause not established."
	if e.Kind == "condition_cleared" {
		text += "\nConfigured clear threshold reached; this does not close a root-cause ticket."
		if i.Clearance != nil {
			text += "\nCleared: " + i.Clearance.Time.Format(time.RFC3339) + "\nCurrent container: " + i.Clearance.Container.ID + "\nDocker state/health: " + i.Clearance.Container.State + "/" + i.Clearance.Container.DockerHealth
		}
	}
	var body bytes.Buffer
	contentType, method := "application/json", "sendMessage"
	if t.sendLogs && e.Kind == "opened" && i.Logs != "" {
		method = "sendDocument"
		writer := multipart.NewWriter(&body)
		writer.WriteField("chat_id", e.ChatID)
		writer.WriteField("caption", truncate(text, 900))
		part, err := writer.CreateFormFile("document", i.ID+"-diagnostics.json")
		if err != nil {
			return 0, errDelivery
		}
		if err = json.NewEncoder(part).Encode(i); err != nil {
			return 0, errDelivery
		}
		if err = writer.Close(); err != nil {
			return 0, errDelivery
		}
		contentType = writer.FormDataContentType()
	} else {
		if err := json.NewEncoder(&body).Encode(map[string]any{"chat_id": e.ChatID, "text": truncate(text, 3500), "link_preview_options": map[string]bool{"is_disabled": true}}); err != nil {
			return 0, errDelivery
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+t.token+"/"+method, &body)
	if err != nil {
		return 0, errDelivery
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := t.client.Do(req)
	if err != nil {
		return 0, errDelivery
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(data) > 65536 {
		return 0, errDelivery
	}
	var result struct {
		OK         bool `json:"ok"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if json.Unmarshal(data, &result) != nil {
		return 0, errDelivery
	}
	if resp.StatusCode != http.StatusOK || !result.OK {
		return time.Duration(min(max(result.Parameters.RetryAfter, 0), 86400)) * time.Second, errDelivery
	}
	return 0, nil
}
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
