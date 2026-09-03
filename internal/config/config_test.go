package config

import "testing"

func TestAgentRejectsPublicPlainHTTP(t *testing.T) {
	t.Setenv("CLOUD_URL", "http://nvr.example.com")
	t.Setenv("SITE_ID", "site")
	if _, err := AgentFromEnv(); err == nil {
		t.Fatal("public HTTP cloud URL was accepted")
	}
	t.Setenv("AGENT_ALLOW_INSECURE_HTTP", "true")
	if _, err := AgentFromEnv(); err != nil {
		t.Fatalf("explicit development override rejected: %v", err)
	}
}

func TestAgentAllowsPrivatePlainHTTP(t *testing.T) {
	t.Setenv("CLOUD_URL", "http://192.168.2.10:8080")
	t.Setenv("SITE_ID", "site")
	if _, err := AgentFromEnv(); err != nil {
		t.Fatalf("private HTTP cloud URL rejected: %v", err)
	}
}
