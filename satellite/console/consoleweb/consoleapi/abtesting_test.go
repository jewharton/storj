// Copyright (C) 2021 Storj Labs, Inc.
// See LICENSE for copying information.

package consoleapi_test

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"storj.io/common/testcontext"
	"storj.io/storj/satellite/console/consoleweb/consoleapi"
)

func Test_GetVariationObject(t *testing.T) {
	var portStr string
	log := zap.L()
	ctx := testcontext.New(t)

	// Mock Flagship AB API
	{
		listener, err := net.Listen("tcp", "localhost:0")
		require.NoError(t, err)

		router := mux.NewRouter()
		router.HandleFunc("/{environmentID}/{campaignID}", func(w http.ResponseWriter, r *http.Request) {
			environmentID := mux.Vars(r)["environmentID"]
			campaignID := mux.Vars(r)["campaignID"]
			apiKey := r.Header.Get("x-api-key")

			if apiKey != "myApiKey" {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			var visitor struct {
				ID string `json:"visitor_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&visitor); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, err := w.Write([]byte(`{"message": "Syntax error in body JSON request"}`))
				require.NoError(t, err)
				return
			}

			w.Header().Set("Content-Type", "application/json")

			if environmentID != "myEnvironmentID" {
				_, err := w.Write([]byte(`{"error": "Environment ID not authorized"}`))
				require.NoError(t, err)
				return
			}

			if campaignID != "myCampaignID" {
				w.WriteHeader(http.StatusBadRequest)
				_, err := w.Write([]byte(`{"message": "The campaign ` + campaignID + ` does not exist"}`))
				require.NoError(t, err)
				return
			}

			_, err := w.Write([]byte(`{
				"id": "myCampaignID",
				"variationGroupId": "foo",
				"variation": {
					"id": "bar",
						"modifications": {
							"type": "JSON",
							"value": {
								"key": "` + visitor.ID + `"
							}
						},
					"reference": false
				}
			}`))
			require.NoError(t, err)
		})
		server := http.Server{
			Handler: router,
			Addr:    listener.Addr().String(),
		}
		go func() {
			require.NoError(t, server.Serve(listener))
		}()
		defer server.Close()

		addrParts := strings.Split(listener.Addr().String(), ":")
		portStr = addrParts[len(addrParts)-1]
		require.NoError(t, err)
	}

	// Test satellite AB service
	{
		urlPrefix := "http://localhost:" + portStr
		config := consoleapi.ABTestingConfig{
			Enabled:          true,
			ApiKey:           "myApiKey",
			BaseVariationURL: urlPrefix + "/myEnvironmentID/",
		}

		ab := consoleapi.NewABTesting(log, config)
		defaultValue := map[string]interface{}{"key": "unknown"}
		result, err := ab.GetVariationObject(ctx, "myCampaignID", "myVisitorID", defaultValue)
		require.NoError(t, err)
		require.Contains(t, result, "key")
		require.Equal(t, "myVisitorID", result["key"])

		config.ApiKey = "wrongApiKey"
		ab = consoleapi.NewABTesting(log, config)
		result, err = ab.GetVariationObject(ctx, "myCampaignID", "myVisitorID", defaultValue)
		require.Error(t, err, consoleapi.ErrABAPI)
		require.Contains(t, result, "key")
		require.Equal(t, "unknown", result["key"])

		config.ApiKey = "myApiKey"
		config.BaseVariationURL = urlPrefix + "/wrongEnvID"
		ab = consoleapi.NewABTesting(log, config)
		result, err = ab.GetVariationObject(ctx, "myCampaignID", "myVisitorID", defaultValue)
		require.Error(t, err, consoleapi.ErrABAPI)
		require.Contains(t, result, "key")
		require.Equal(t, "unknown", result["key"])

		config.BaseVariationURL = urlPrefix + "/myEnvironmentID"
		ab = consoleapi.NewABTesting(log, config)
		result, err = ab.GetVariationObject(ctx, "wrongCampaignID", "myVisitorID", defaultValue)
		require.Error(t, err, consoleapi.ErrABAPI)
		require.Contains(t, result, "key")
		require.Equal(t, "unknown", result["key"])
	}
}
