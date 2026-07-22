package identity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderCompatibilityFixtures_ProjectWithoutProviderSDKs(t *testing.T) {
	types := []string{"generic", "auth0", "keycloak-ory", "cognito", "firebase"}
	for _, name := range types {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "testing", "fixtures", "oidc", name+".json")
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			var fixture struct {
				Subject string         `json:"subject"`
				Claims  map[string]any `json:"claims"`
			}
			require.NoError(t, json.Unmarshal(data, &fixture))
			principal, err := (MappingProfile{}).Project(fixture.Subject, fixture.Claims)
			require.NoError(t, err)
			require.Equal(t, fixture.Subject, principal.Subject)
		})
	}
}
