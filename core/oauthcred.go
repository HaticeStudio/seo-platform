package core

import (
	"encoding/json"
	"errors"
)

// OAuthMaterial is the stored shape of an "oauth2" credential: enough to mint
// access tokens unattended. It exists only inside SecretStore material and in
// transit between the OAuth completion handler and provider token sources.
type OAuthMaterial struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	TokenURL     string `json:"token_url"`
	RefreshToken string `json:"refresh_token"`
}

func (m OAuthMaterial) Marshal() ([]byte, error) { return json.Marshal(m) }

// ParseOAuthMaterial validates and decodes stored oauth2 credential bytes.
func ParseOAuthMaterial(raw []byte) (OAuthMaterial, error) {
	var material OAuthMaterial
	if err := json.Unmarshal(raw, &material); err != nil {
		return OAuthMaterial{}, errors.New("invalid oauth credential material")
	}
	if material.RefreshToken == "" || material.ClientID == "" || material.TokenURL == "" {
		return OAuthMaterial{}, errors.New("incomplete oauth credential material")
	}
	return material, nil
}
