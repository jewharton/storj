// Copyright (C) 2021 Storj Labs, Inc.
// See LICENSE for copying information.

package consoleapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/zeebo/errs"
	"go.uber.org/zap"

	"storj.io/storj/satellite/console"
)

// ErrABAPI - console auth ab testing error type.
var ErrABAPI = errs.Class("console auth ab testing error")

// ABTesting is an api controller that exposes all ab testing functionality.
type ABTesting struct {
	log    *zap.Logger
	config ABTestingConfig
}

// ABTestingConfig contains configurations for the Flagship AB testing system.
type ABTestingConfig struct {
	Enabled          bool   `help:"whether or not AB testing is enabled" default:"false"`
	ApiKey           string `help:"the Flagship API key"`
	BaseVariationURL string `help:"the prefix of the API URL to receive information about campaign variations; campaign ID will be suffixed when sending requests" default:"https://decision.flagship.io/v2/ENVIRONMENT_ID/campaigns/"`
}

// NewAuth is a constructor for api auth controller.
func NewABTesting(log *zap.Logger, config ABTestingConfig) *ABTesting {
	return &ABTesting{
		log:    log,
		config: config,
	}
}

func (a *ABTesting) SetBaseVariationURL(url string) {
	a.config.BaseVariationURL = url
}

// GetVariationObject contacts returns the campaign variation assigned to a visitor.
func (a *ABTesting) GetVariationObject(ctx context.Context, campaignId string, visitorId string, defaultValue map[string]interface{}) (result map[string]interface{}, err error) {
	defer mon.Task()(&ctx)(&err)

	result = defaultValue

	reqBody, err := json.Marshal(map[string]interface{}{
		"visitor_id": visitorId,
	})
	if err != nil {
		err = ErrABAPI.Wrap(err)
		a.log.Warn("failed to encode variation json request; returning default", zap.Error(err))
		return
	}

	url := strings.TrimRight(a.config.BaseVariationURL, "/") + "/" + campaignId
	req, err := http.NewRequest("POST", url, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", a.config.ApiKey)
	if err != nil {
		err = ErrABAPI.Wrap(err)
		a.log.Warn("failed to generate variation request; returning default", zap.Error(err))
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		err = ErrABAPI.Wrap(err)
		a.log.Warn("failed to receive variation response; returning default", zap.Error(err))
		return
	}
	if resp.StatusCode != http.StatusOK {
		a.log.Warn("variation response status is not OK; returning default", zap.String("Status", resp.Status))
		err = ErrABAPI.New(resp.Status)
		return
	}
	defer func() { err = errs.Combine(err, resp.Body.Close()) }()

	var campaign struct {
		Error     string `json:"error"`
		Message   string `json:"message"`
		Variation struct {
			Modifications struct {
				Value map[string]interface{} `json:"value"`
			} `json:"modifications"`
		} `json:"variation"`
	}

	err = json.NewDecoder(resp.Body).Decode(&campaign)
	if err != nil {
		err = ErrABAPI.Wrap(err)
		a.log.Warn("failed to decode json variation response; returning default", zap.Error(err))
		return
	}

	errMsg := campaign.Error
	if errMsg == "" && campaign.Message != "" {
		errMsg = campaign.Message
	}
	if errMsg != "" {
		err = ErrABAPI.New(errMsg)
		a.log.Warn("variation response contained an error; returning default", zap.Error(err))
		return
	}

	return campaign.Variation.Modifications.Value, nil
}

// GetPassphraseEntryRequired gets whether to require a passphrase entry
// immediately after the passcode's generation.
func (a *ABTesting) GetPassphraseEntryRequired(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var err error
	defer mon.Task()(&ctx)(&err)

	passphraseEntryInfo := map[string]interface{}{
		"required": true,
	}

	auth, err := console.GetAuth(ctx)
	if err != nil {
		a.serveJSONError(w, err)
		return
	}

	passphraseEntryInfo, err = a.GetVariationObject(ctx, "passphrase-entry-required", auth.User.Email, passphraseEntryInfo)
	if err != nil {
		a.log.Warn("failed to receive passphrase entry info", zap.Error(err))
	}

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(&passphraseEntryInfo)
	if err != nil {
		a.log.Error("failed to encode passphrase entry info", zap.Error(err))
	}
}

// serveJSONError writes JSON error to response output stream.
func (a *ABTesting) serveJSONError(w http.ResponseWriter, err error) {
	w.WriteHeader(a.getStatusCode(err))

	var response struct {
		Error string `json:"error"`
	}

	response.Error = err.Error()

	err = json.NewEncoder(w).Encode(response)
	if err != nil {
		a.log.Error("failed to write json error response", zap.Error(ErrABAPI.Wrap(err)))
	}
}

// getStatusCode returns http.StatusCode depends on console error class.
func (a *ABTesting) getStatusCode(err error) int {
	switch {
	case console.ErrValidation.Has(err):
		return http.StatusBadRequest
	case console.ErrUnauthorized.Has(err):
		return http.StatusUnauthorized
	case console.ErrEmailUsed.Has(err):
		return http.StatusConflict
	case errors.Is(err, errNotImplemented):
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}
