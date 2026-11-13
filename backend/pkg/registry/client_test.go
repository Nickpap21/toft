package registry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDockerHubLoginIsJSONWhateverTheCredentials(t *testing.T) {
	var sent []byte
	c := &Client{http: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		var err error
		sent, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"token":"t"}`))}, nil
	})}}

	auth := &Auth{Username: "ada", Password: "p\"w\\\x01d\x7f é"}
	token, err := c.dockerHubToken(context.Background(), auth)
	require.NoError(t, err)
	assert.Equal(t, "t", token)

	var got struct{ Username, Password string }
	require.NoError(t, json.Unmarshal(sent, &got), string(sent))
	assert.Equal(t, auth.Username, got.Username)
	assert.Equal(t, auth.Password, got.Password)
}

func TestDockerHubTagsAskForTheNewestFirst(t *testing.T) {
	var asked string
	c := &Client{http: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		asked = r.URL.String()
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"results":[{"name":"1.30.5","last_updated":"2026-10-08T13:51:01Z","digest":"sha256:ab"}]}`))}, nil
	})}}

	tags, err := c.listDockerHubTags(context.Background(), "library/nginx", "1.27", nil)
	require.NoError(t, err)
	// Docker Hub's ordering is reversed: last_updated is newest first,
	// -last_updated oldest first.
	assert.Contains(t, asked, "ordering=last_updated")
	assert.NotContains(t, asked, "ordering=-last_updated")
	require.Len(t, tags, 1)
	assert.Equal(t, "1.30.5", tags[0].Name)
}
