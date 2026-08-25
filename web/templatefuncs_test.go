package web

import (
	"bytes"
	"html/template"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHumanBytes(t *testing.T) {
	assert.Equal(t, "512B", humanBytes(512))
	assert.Equal(t, "1.0KiB", humanBytes(1024))
	assert.Equal(t, "1.5MiB", humanBytes(1024*1024*3/2))
	assert.Equal(t, "8.0GiB", humanBytes(8*1024*1024*1024))
	assert.Equal(t, "0B", humanBytes(0))
}

func TestHumanMillicores(t *testing.T) {
	assert.Equal(t, "250m", humanMillicores(250))
	assert.Equal(t, "1.00", humanMillicores(1000))
	assert.Equal(t, "3.50", humanMillicores(3500))
}

func TestHumanDuration(t *testing.T) {
	assert.Equal(t, "45s", humanDuration(45))
	assert.Equal(t, "5m", humanDuration(300))
	assert.Equal(t, "2h30m", humanDuration(9000))
	assert.Equal(t, "3d", humanDuration(3*86400))
}

func TestPercentGuardsDivisionByZero(t *testing.T) {
	assert.Equal(t, 0.0, percent(5, 0))
	assert.Equal(t, 0.0, percent(5, -1))
	assert.Equal(t, 50.0, percent(1, 2))
	assert.Equal(t, 33.3, percent(1, 3))
}

func TestTemplateFuncsIncludeSprigAndOurs(t *testing.T) {
	f := templateFuncs()
	for _, name := range []string{"upper", "default", "date", "trim", "toJson", "emojify", "statusEmoji", "humanBytes"} {
		_, ok := f[name]
		assert.True(t, ok, "expected %q in the FuncMap", name)
	}
	// This process holds API keys and the VAPID private key in its environment;
	// no template may reach them.
	for _, leaky := range []string{"env", "expandenv"} {
		_, ok := f[leaky]
		assert.False(t, ok, "%q must be removed from the FuncMap", leaky)
	}
}

func TestTemplateFuncsRenderInATemplate(t *testing.T) {
	tmpl, err := template.New("t").Funcs(templateFuncs()).Parse(
		`{{ .Name | upper }}|{{ humanBytes .Bytes }}|{{ emojify .Note }}|{{ .Missing | default "n/a" }}`)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, tmpl.Execute(&buf, map[string]any{
		"Name":  "prod",
		"Bytes": int64(2048),
		"Note":  ":fire: hot",
	}))
	assert.Equal(t, "PROD|2.0KiB|🔥 hot|n/a", buf.String())
}

func TestValidPushEndpointRejectsNonHTTPS(t *testing.T) {
	assert.True(t, validPushEndpoint("https://fcm.googleapis.com/fcm/send/x"))
	assert.False(t, validPushEndpoint("http://evil.internal/x"), "plain http is an SSRF vector")
	assert.False(t, validPushEndpoint("file:///etc/passwd"))
	assert.False(t, validPushEndpoint(""))
	assert.False(t, validPushEndpoint("https://"+string(make([]byte, 3000))), "absurd length rejected")
}
