// Copyright 2019 The Morning Consult, LLC or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//         https://www.apache.org/licenses/LICENSE-2.0
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"text/template"
	"time"

	cleanhttp "github.com/hashicorp/go-cleanhttp"
	"github.com/morningconsult/go-elasticsearch-alerts/command/alert"
	"golang.org/x/xerrors"
)

const defaultTextLimit = 6000

// Ensure AlertMethod adheres to the alert.Method interface.
var _ alert.Method = (*AlertMethod)(nil)

// AlertMethodConfig configures where Slack alerts should be
// created and what they should look like.
type AlertMethodConfig struct {
	WebhookURL     string `mapstructure:"webhook"`
	Channel        string `mapstructure:"channel"`
	Username       string `mapstructure:"username"`
	Text           string `mapstructure:"text"`
	Emoji          string `mapstructure:"emoji"`
	TextLimit      int    `mapstructure:"text_limit"`
	IncludeData    bool   `mapstructure:"include_data"`
	Client         *http.Client
	BodyTemplate   string `mapstructure:"body_template"`
	FilterTemplate string `mapstructure:"filter_template"`
}

// AlertMethod implements the alert.AlertMethod interface
// for writing new alerts to Slack.
type AlertMethod struct {
	webhookURL     string
	client         *http.Client
	channel        string
	username       string
	text           string
	emoji          string
	textLimit      int
	bodyTemplate   *template.Template
	filterTemplate *template.Template
}

// payload represents the JSON data needed to create a
// new Slack message.
type payload struct {
	Channel     string       `json:"channel,omitempty"`
	Username    string       `json:"username,omitempty"`
	Text        string       `json:"text,omitempty"`
	Emoji       string       `json:"icon_emoji,omitempty"`
	Attachments []attachment `json:"attachments,omitempty"`
}

func toJSON(obj interface{}) string {
	b, _ := json.MarshalIndent(obj, "", "  ")
	return string(b)
}

// NewAlertMethod creates a new *AlertMethod or a
// non-nil error if there was an error.
func NewAlertMethod(config *AlertMethodConfig) (alert.Method, error) {
	if config == nil {
		return nil, xerrors.New("no config provided")
	}

	if config.WebhookURL == "" {
		return nil, xerrors.New("field 'output.config.webhook' must not be empty when using the Slack output method")
	}

	if config.Client == nil {
		config.Client = cleanhttp.DefaultClient()
	}

	if config.TextLimit == 0 {
		config.TextLimit = defaultTextLimit
	}

	funcMap := template.FuncMap{
		"toJSON": toJSON,
	}

	return &AlertMethod{
		channel:        config.Channel,
		webhookURL:     config.WebhookURL,
		client:         config.Client,
		text:           config.Text,
		emoji:          config.Emoji,
		textLimit:      config.TextLimit,
		bodyTemplate:   template.Must(template.New("body").Funcs(funcMap).Parse(config.BodyTemplate)),
		filterTemplate: template.Must(template.New("filter").Parse(config.FilterTemplate)),
	}, nil
}

// Write creates a properly-formatted Slack message from the
// records and posts it to the webhook defined at the creation
// of the AlertMethod. If there was an error making the
// HTTP request, it returns a non-nil error.
func (s *AlertMethod) Write(ctx context.Context, rule string, records []*alert.Record) error {
	if records == nil || len(records) < 1 {
		return nil
	}
	pl, err := s.buildPayload(rule, records)
	if err != nil {
		return err
	}
	return s.post(ctx, pl)
}

func (s *AlertMethod) formatBody(jsonText string) (string, error) {
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(jsonText), &obj); err != nil {
		return "```\n" + jsonText + "\n```", xerrors.Errorf("failed to parse body as JSON: %s", err)
	}
	str := new(strings.Builder)
	err := s.bodyTemplate.Execute(str, obj)
	if err != nil {
		return "```\n" + jsonText + "\n```", xerrors.Errorf("failed to execute body template: %s", err)
	}
	return str.String(), nil
}

func (s *AlertMethod) formatFilter(text string) (string, error) {
	str := new(strings.Builder)
	err := s.filterTemplate.Execute(str, text)
	if err != nil {
		return text, xerrors.Errorf("failed to execute filter template: %s", err)
	}
	return str.String(), nil
}

// buildPayload creates a *Payload instance from the provided
// records. After being JSON-encoded it can be included in a
// POST request to a Slack webhook in order to create a new
// Slack message.
func (s *AlertMethod) buildPayload(rule string, records []*alert.Record) (payload, error) {
	pl := payload{
		Channel:  s.channel,
		Username: s.username,
		Text:     s.text,
		Emoji:    s.emoji,
	}

	records = s.preprocess(records)

	for _, record := range records {
		filterText, err := s.formatFilter(record.Filter)
		if err != nil {
			return pl, err
		}

		att := attachment{
			Title:      rule,
			Text:       filterText,
			MarkdownIn: []string{"text"},
			Color:      defaultAttachmentColor,
			Footer:     defaultAttachmentFooter,
			Timestamp:  time.Now().Unix(),
		}

		if record.BodyField && record.Text != "" {
			bodyText, err := s.formatBody(record.Text)
			if err != nil {
				return pl, err
			}
			att.Text = att.Text + "\n" + bodyText
			att.Color = defaultBodyColor
		}

		for _, f := range record.Fields {
			short := false
			if len(f.Key) <= 35 {
				short = true
			}

			att.Fields = append(att.Fields, field{
				Title: f.Key,
				Value: fmt.Sprintf("%d", f.Count),
				Short: short,
			})
		}

		pl.Attachments = append(pl.Attachments, att)
	}

	return pl, nil
}

func (s *AlertMethod) post(ctx context.Context, pl payload) error {
	buf := bytes.Buffer{}
	if err := json.NewEncoder(&buf).Encode(pl); err != nil {
		return err
	}
	req, err := http.NewRequest("POST", s.webhookURL, &buf)
	if err != nil {
		return err
	}
	req.Header.Add("Content-Type", "application/json")
	req = req.WithContext(ctx)

	resp, err := s.client.Do(req)
	if err != nil {
		return xerrors.Errorf("error making HTTP request: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != 200 {
		return xerrors.Errorf("received non-200 status code: %s", resp.Status)
	}

	return err
}

// preprocess breaks attachments with text greater than s.textLimit
// into multiple attachments in order to prevent trucation.
func (s *AlertMethod) preprocess(records []*alert.Record) []*alert.Record {
	output := make([]*alert.Record, 0)
	for _, rawRecord := range records {
		n := len(rawRecord.Text) / s.textLimit
		if n < 1 {
			output = append(output, rawRecord)
			continue
		}
		var i int
		for i = 0; i < n; i++ {
			chopped := fmt.Sprintf(
				"(part %d of %d)\n\n%s\n\n(continued)",
				i+1, n+1, rawRecord.Text[s.textLimit*i:s.textLimit*(i+1)],
			)
			record := &alert.Record{
				Filter:    fmt.Sprintf("%s (%d of %d)", rawRecord.Filter, i+1, n+1),
				Text:      chopped,
				BodyField: rawRecord.BodyField,
			}
			output = append(output, record)
		}
		chopped := fmt.Sprintf(
			"(part %d of %d)\n\n%s", i+1, n+1,
			rawRecord.Text[s.textLimit*i:],
		)
		record := &alert.Record{
			Filter:    fmt.Sprintf("%s (%d of %d)", rawRecord.Filter, i+1, n+1),
			Text:      chopped,
			BodyField: rawRecord.BodyField,
		}
		output = append(output, record)
	}
	return output
}
