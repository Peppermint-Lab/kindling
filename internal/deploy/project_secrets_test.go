package deploy

import (
	"testing"

	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestBuildRuntimeEnvDecryptsProjectSecrets(t *testing.T) {
	env, err := buildRuntimeEnv([]queries.EnvironmentVariable{
		{
			ID:        pgUUID(uuid.New()),
			ProjectID: pgUUID(uuid.New()),
			Name:      "API_KEY",
			Value:     "enc:v1:opaque",
		},
		{
			ID:        pgUUID(uuid.New()),
			ProjectID: pgUUID(uuid.New()),
			Name:      "LEGACY",
			Value:     "plain",
		},
	}, fakeProjectSecretDecoder{
		values: map[string]string{
			"enc:v1:opaque": "decrypted",
			"plain":         "plain",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 2 || env[0] != "API_KEY=decrypted" || env[1] != "LEGACY=plain" {
		t.Fatalf("env = %#v", env)
	}
}

func TestBuildRuntimeEnvRejectsEncryptedValuesWithoutDecoder(t *testing.T) {
	_, err := buildRuntimeEnv([]queries.EnvironmentVariable{
		{
			ID:        pgUUID(uuid.New()),
			ProjectID: pgUUID(uuid.New()),
			Name:      "API_KEY",
			Value:     "enc:v1:opaque",
		},
	}, nil)
	if err == nil {
		t.Fatal("expected missing decoder error")
	}
}

type fakeProjectSecretDecoder struct {
	values map[string]string
	err    error
}

func (f fakeProjectSecretDecoder) DecryptProjectSecretValue(stored string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if v, ok := f.values[stored]; ok {
		return v, nil
	}
	return stored, nil
}
