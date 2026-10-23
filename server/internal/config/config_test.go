package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadAppliesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"asr":{"api_key":"asr-key"},
		"llm":{"api_key":"llm-key"},
		"tts":{"api_key":"tts-key","voice_id":"voice"}
	}`), 0o600))

	cfg, err := Load(path)

	require.NoError(t, err)
	require.Equal(t, defaultAddress, cfg.Address)
	require.Equal(t, defaultASRModel, cfg.ASR.Model)
	require.Equal(t, defaultLLMModel, cfg.LLM.Model)
	require.Equal(t, defaultTTSModel, cfg.TTS.Model)
}

func TestLoadRejectsMissingProviderKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"asr":{"api_key":"asr-key"}}`), 0o600))

	_, err := Load(path)

	require.ErrorContains(t, err, "llm.api_key is required")
}
