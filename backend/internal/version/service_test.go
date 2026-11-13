package version

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Nickpap21/toft/backend/pkg/scm"
)

func TestSCMErrorTextSaysWhyAndWhatToDo(t *testing.T) {
	limited := &scm.APIError{Provider: "github", Status: 403, Body: `{"message":"API rate limit exceeded for 203.0.113.7."}`}
	wrapped := fmt.Errorf("list branches: %w", limited)

	assert.Equal(t, "GitHub's limit for requests without a token is used up. Add a Git token in Settings → Credentials, or try again within the hour.", scmErrorText(wrapped, false))
	assert.Equal(t, "GitHub's request limit for the Git token is used up. Try again within the hour.", scmErrorText(limited, true))
	assert.Equal(t, "GitLab refused the Git token. Check it in Settings → Credentials.", scmErrorText(&scm.APIError{Provider: "gitlab", Status: 401}, true))
	assert.Contains(t, scmErrorText(&scm.APIError{Provider: "github", Status: 404}, false), "it is private, or the URL is wrong")
	assert.Equal(t, "GitHub answered HTTP 502. Try again in a moment.", scmErrorText(&scm.APIError{Provider: "github", Status: 502}, false))
	assert.Equal(t, "The repository host didn't answer. Try again in a moment.", scmErrorText(errors.New("dial tcp: i/o timeout"), false))
	// The provider's own message (here an IP address) never reaches the panel.
	assert.NotContains(t, scmErrorText(limited, false), "77.49")
}
