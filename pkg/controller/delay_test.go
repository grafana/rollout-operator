package controller

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreatePrepareDownscaleEndpoints(t *testing.T) {
	testCases := []struct {
		name        string
		namespace   string
		stsName     string
		serviceName string
		from        int
		to          int
		inputURL    string
		expected    []endpoint
	}{
		{
			name:        "URL without port",
			namespace:   "test-namespace",
			stsName:     "test-service",
			serviceName: "test-service-headless",
			from:        0,
			to:          2,
			inputURL:    "http://example.com/api/prepare",
			expected: []endpoint{
				{
					namespace: "test-namespace",
					podName:   "test-service-0",
					url:       mustParseURL(t, "http://test-service-0.test-service-headless.test-namespace.svc.cluster.local./api/prepare"),
					replica:   0,
				},
				{
					namespace: "test-namespace",
					podName:   "test-service-1",
					url:       mustParseURL(t, "http://test-service-1.test-service-headless.test-namespace.svc.cluster.local./api/prepare"),
					replica:   1,
				},
			},
		},
		{
			name:        "URL with port",
			namespace:   "prod-namespace",
			stsName:     "prod-service",
			serviceName: "prod-service-headless",
			from:        1,
			to:          3,
			inputURL:    "http://example.com:8080/api/prepare",
			expected: []endpoint{
				{
					namespace: "prod-namespace",
					podName:   "prod-service-1",
					url:       mustParseURL(t, "http://prod-service-1.prod-service-headless.prod-namespace.svc.cluster.local.:8080/api/prepare"),
					replica:   1,
				},
				{
					namespace: "prod-namespace",
					podName:   "prod-service-2",
					url:       mustParseURL(t, "http://prod-service-2.prod-service-headless.prod-namespace.svc.cluster.local.:8080/api/prepare"),
					replica:   2,
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			inputURL, err := url.Parse(tc.inputURL)
			require.NoError(t, err)

			result := createPrepareDownscaleEndpoints(tc.namespace, tc.stsName, tc.serviceName, tc.from, tc.to, inputURL)

			assert.Equal(t, tc.expected, result)
		})
	}
}

func mustParseURL(t *testing.T, urlString string) url.URL {
	u, err := url.Parse(urlString)
	require.NoError(t, err)
	return *u
}
