package clients

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cyberark/conjur-api-go/conjurapi"
	"github.com/cyberark/conjur-api-go/conjurapi/response"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validDiscoveryJSON is a minimal /api/public/tenant-discovery response that satisfies
// all checks in identityURL: non-empty tenant_id, identity_administration service with
// a main endpoint whose ui field is set.
const validDiscoveryJSON = `{
	"tenant_id": "tenant-123",
	"services": [{
		"service_name": "identity_administration",
		"region": "us-east-1",
		"endpoints": [
			{"type": "main", "is_active": true, "ui": "https://mytenant.id.cyberark.cloud"},
			{"type": "crdr", "is_active": false, "ui": "https://mytenant-dr.id.cyberark.cloud"}
		]
	}]
}`

func TestParseCloudURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    *CloudURL
		wantErr assert.ErrorAssertionFunc
	}{{
		"valid URL",
		"https://mytenant.secretsmgr.cyberark.cloud/api",
		&CloudURL{Tenant: "mytenant", Suffix: ".cyberark.cloud"},
		assert.NoError,
	}, {
		"invalid subdomain",
		"https://mytenant.secretsmgr.invalid.domain/api",
		&CloudURL{Tenant: "mytenant", Suffix: ".invalid.domain"},
		func(t assert.TestingT, err error, i ...interface{}) bool {
			return assert.Error(t, err) && assert.Contains(t, err.Error(), "unrecognized domain suffix")
		},
	}, {
		"missing secretsmgr",
		"https://mytenant.cyberark.cloud/api",
		&CloudURL{Tenant: "mytenant", Suffix: ".cyberark.cloud"},
		func(t assert.TestingT, err error, i ...interface{}) bool {
			return assert.Error(t, err) && assert.Contains(t, err.Error(), "https://<tenant>.secretsmgr.cyberark.cloud/api")
		},
	}, {
		"missing api",
		"https://mytenant.secretsmgr.cyberark.cloud",
		nil,
		assert.Error,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCloudURL(tt.url)
			if !tt.wantErr(t, err, fmt.Sprintf("ParseCloudURL(%v)", tt.url)) {
				return
			}
			assert.Equalf(t, tt.want, got, "ParseCloudURL(%v)", tt.url)
		})
	}
}

func Test_extractHTTPStatusCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{{
		"401",
		&response.ConjurError{Code: 401},
		401,
	}, {
		"no status code",
		fmt.Errorf("some other error"),
		0,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, extractHTTPStatusCode(tt.err), "extractHTTPStatusCode(%v)", tt.err)
		})
	}
}

func Test_tenantDiscoveryEndpointURL(t *testing.T) {
	tests := []struct {
		name  string
		ccURL *CloudURL
		want  string
	}{
		{
			"standard prod suffix",
			&CloudURL{Tenant: "mytenant", Suffix: ".cyberark.cloud"},
			"https://platform-discovery.cyberark.cloud/api/public/tenant-discovery?bySubdomain=mytenant&allEndpoints=true",
		},
		{
			"regional pd suffix",
			&CloudURL{Tenant: "pdtenant", Suffix: ".us-east-1.pd.cyberark.cloud"},
			"https://platform-discovery.us-east-1.pd.cyberark.cloud/api/public/tenant-discovery?bySubdomain=pdtenant&allEndpoints=true",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tenantDiscoveryEndpointURL(tt.ccURL))
		})
	}
}

func Test_fetchTenantDiscovery_success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/public/tenant-discovery", r.URL.Path)
		assert.Equal(t, "mytenant", r.URL.Query().Get("bySubdomain"))
		assert.Equal(t, "true", r.URL.Query().Get("allEndpoints"))
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, validDiscoveryJSON)
	}))
	defer srv.Close()

	result, err := fetchTenantDiscovery(newTestClient(srv), tenantDiscoveryEndpointURL(&CloudURL{Tenant: "mytenant", Suffix: ".cyberark.cloud"}))

	require.NoError(t, err)
	assert.Equal(t, "tenant-123", result.TenantID)
	require.Len(t, result.Services, 1)
	assert.Equal(t, "identity_administration", result.Services[0].ServiceName)
}

func Test_fetchTenantDiscovery_non200(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"404", http.StatusNotFound},
		{"500", http.StatusInternalServerError},
		{"401", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, "<html>error page</html>")
			}))
			defer srv.Close()

			_, err := fetchTenantDiscovery(newTestClient(srv), srv.URL+"/api/public/tenant-discovery")

			require.Error(t, err)
			assert.Contains(t, err.Error(), fmt.Sprintf("HTTP %d", tt.status))
			assert.NotContains(t, err.Error(), "html") // body must not leak into error message
		})
	}
}

func Test_fetchTenantDiscovery_invalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "not json")
	}))
	defer srv.Close()

	_, err := fetchTenantDiscovery(newTestClient(srv), srv.URL+"/api/public/tenant-discovery")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse tenant discovery response")
}

func Test_fetchTenantDiscovery_requestError(t *testing.T) {
	_, err := fetchTenantDiscovery(http.DefaultClient, "http://127.0.0.1:0/unreachable")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant discovery request failed")
}

func Test_findIdentityAdminURL(t *testing.T) {
	mainEndpoint := tenantDiscoveryServiceEndpoint{Type: "main", IsActive: true, UI: "https://mytenant.id.cyberark.cloud"}
	crdrEndpoint := tenantDiscoveryServiceEndpoint{Type: "crdr", IsActive: false, UI: "https://mytenant-dr.id.cyberark.cloud"}
	idSvc := tenantDiscoveryService{
		ServiceName: "identity_administration",
		Endpoints:   []tenantDiscoveryServiceEndpoint{mainEndpoint, crdrEndpoint},
	}
	otherSvc := tenantDiscoveryService{
		ServiceName: "other_service",
		Endpoints:   []tenantDiscoveryServiceEndpoint{mainEndpoint},
	}

	tests := []struct {
		name    string
		result  *tenantDiscoveryResp
		want    string
		wantErr string
	}{
		{
			"happy path",
			&tenantDiscoveryResp{Services: []tenantDiscoveryService{otherSvc, idSvc}},
			"https://mytenant.id.cyberark.cloud",
			"",
		},
		{
			"identity_administration service missing",
			&tenantDiscoveryResp{Services: []tenantDiscoveryService{otherSvc}},
			"",
			"identity_administration service not found",
		},
		{
			"main endpoint missing",
			&tenantDiscoveryResp{Services: []tenantDiscoveryService{
				{ServiceName: "identity_administration", Endpoints: []tenantDiscoveryServiceEndpoint{crdrEndpoint}},
			}},
			"",
			"main endpoint not found",
		},
		{
			"main endpoint ui empty",
			&tenantDiscoveryResp{Services: []tenantDiscoveryService{
				{ServiceName: "identity_administration", Endpoints: []tenantDiscoveryServiceEndpoint{
					{Type: "main", IsActive: true, UI: ""},
				}},
			}},
			"",
			"main endpoint not found",
		},
		{
			"empty services list",
			&tenantDiscoveryResp{Services: []tenantDiscoveryService{}},
			"",
			"identity_administration service not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findIdentityAdminURL(tt.result)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func Test_identityURL_success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, validDiscoveryJSON)
	}))
	defer srv.Close()

	fc := &fakeConjurClient{
		config:     conjurapi.Config{ApplianceURL: "https://mytenant.secretsmgr.cyberark.cloud/api"},
		httpClient: newTestClient(srv),
	}

	identURL, tenantID, err := identityURL(fc)

	require.NoError(t, err)
	assert.Equal(t, "https://mytenant.id.cyberark.cloud", identURL)
	assert.Equal(t, "tenant-123", tenantID)
}

func Test_identityURL_requestURL(t *testing.T) {
	var capturedURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, validDiscoveryJSON)
	}))
	defer srv.Close()

	fc := &fakeConjurClient{
		config:     conjurapi.Config{ApplianceURL: "https://mytenant.secretsmgr.cyberark.cloud/api"},
		httpClient: newTestClient(srv),
	}

	_, _, err := identityURL(fc)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(capturedURL, "/api/public/tenant-discovery"))
	assert.Contains(t, capturedURL, "bySubdomain=mytenant")
	assert.Contains(t, capturedURL, "allEndpoints=true")
}

func Test_identityURL_missingTenantID(t *testing.T) {
	body := `{"tenant_id":"","services":[{"service_name":"identity_administration",
		"endpoints":[{"type":"main","is_active":true,"ui":"https://mytenant.id.cyberark.cloud"}]}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	fc := &fakeConjurClient{
		config:     conjurapi.Config{ApplianceURL: "https://mytenant.secretsmgr.cyberark.cloud/api"},
		httpClient: newTestClient(srv),
	}

	_, _, err := identityURL(fc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant_id not found")
}

func Test_identityURL_non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	fc := &fakeConjurClient{
		config:     conjurapi.Config{ApplianceURL: "https://mytenant.secretsmgr.cyberark.cloud/api"},
		httpClient: newTestClient(srv),
	}

	_, _, err := identityURL(fc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant discovery request returned HTTP 404")
}

func Test_identityURL_invalidApplianceURL(t *testing.T) {
	fc := &fakeConjurClient{
		config:     conjurapi.Config{ApplianceURL: "https://notacloudurl.example.com/api"},
		httpClient: http.DefaultClient,
	}

	_, _, err := identityURL(fc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid Idira Secrets Manager, SaaS URL")
}
