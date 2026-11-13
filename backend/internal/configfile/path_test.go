package configfile

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidatePath(t *testing.T) {
	for _, ok := range []string{"/etc/nginx/conf.d/default.conf", "/app/config.yml", "/a/./b.conf"} {
		assert.NoError(t, ValidatePath(ok), ok)
	}
	for _, bad := range []string{"", "/", "relative/x.conf", "/etc/../../x", "../x", "/a/..", "/a\nb", `\etc\x`} {
		err := ValidatePath(bad)
		assert.True(t, errors.Is(err, ErrInvalidPath), "%q: %v", bad, err)
	}
}

func TestHostPathFor(t *testing.T) {
	p, err := hostPathFor(hostBasePath, "svc-1", "/etc/nginx/nginx.conf")
	assert.NoError(t, err)
	assert.Equal(t, "/var/paas/config/svc-1/etc/nginx/nginx.conf", p)

	_, err = hostPathFor(hostBasePath, "svc-1", "/../../../etc/cron.d/x")
	assert.Error(t, err)
}
