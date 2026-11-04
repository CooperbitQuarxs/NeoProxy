package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoKimiLogin runs the Kimi Code device-code sign-in and saves the account.
func DoKimiLogin(cfg *config.Config, options *LoginOptions) {
	doKimiLoginWithProvider(cfg, options, "kimi", "Kimi")
}

func doKimiLoginWithProvider(cfg *config.Config, options *LoginOptions, provider string, displayName string) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()
	authOpts := &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  map[string]string{},
		Prompt:    options.Prompt,
	}

	record, savedPath, err := manager.Login(context.Background(), provider, cfg, authOpts)
	if err != nil {
		log.Errorf("%s authentication failed: %v", displayName, err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Printf("%s authentication successful!\n", displayName)
}
